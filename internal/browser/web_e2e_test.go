//go:build web_e2e

package browser_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/admission"
	"github.com/jhzhfree/ai-governance-gateway/internal/browser"
	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/circuit"
	"github.com/jhzhfree/ai-governance-gateway/internal/console"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/execution"
	"github.com/jhzhfree/ai-governance-gateway/internal/gateway"
	"github.com/jhzhfree/ai-governance-gateway/internal/httpapi"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/keyword"
	"github.com/jhzhfree/ai-governance-gateway/internal/localauth"
	"github.com/jhzhfree/ai-governance-gateway/internal/mockprovider"
	"github.com/jhzhfree/ai-governance-gateway/internal/newapibridge"
	"github.com/jhzhfree/ai-governance-gateway/internal/observe"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func TestConsoleBrowser(t *testing.T)       { testConsoleBrowser(t, false) }
func TestNewAPIConsoleBrowser(t *testing.T) { testConsoleBrowser(t, true) }
func testConsoleBrowser(t *testing.T, governed bool) {
	pool, ledger, _ := testutil.Database(t)
	issuer := testutil.NewIssuer(t)
	catalog := testutil.MainlandManager(t, pool, identity.Principal{Issuer: issuer.URL, Subject: "alice"}, testutil.SupplierFixture(t))
	if err := catalog.Publish(context.Background(), identity.Principal{Issuer: issuer.URL, Subject: "alice"}, 0, testutil.MainlandConfig()); err != nil {
		t.Fatal(err)
	}
	store := control.NewWithCatalog(pool, combinedFixtureCatalog{catalog}).WithProviderManager(catalog)
	var evidenceOrg, evidenceProject string
	if !governed {
		actor := identity.Principal{Issuer: issuer.URL, Subject: "alice"}
		org, err := store.CreateOrganizationWithCurrency(context.Background(), actor, "恢复费用证据", "2026-11", provider.Currency, 10000)
		if err != nil {
			t.Fatal(err)
		}
		project, err := store.CreateProject(context.Background(), actor, org.ID, "完成事件与费用回执", 5000)
		if err != nil {
			t.Fatal(err)
		}
		evidenceOrg, evidenceProject = org.ID, project.ID
		for _, state := range []string{"awaiting_usage", "recorded", "receipted"} {
			testutil.SeedEmbeddedCompletion(t, pool, budget.Scope{Organization: org.ID, Project: project.ID, Period: org.Period, Currency: org.Currency}, "browser-evidence-"+state, state)
		}
	}
	var engine *newapibridge.ManagedRuntime
	var nativeUpstream string
	if governed {
		vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"synthetic","object":"chat.completion","model":"governance-model","choices":[{"index":0,"message":{"role":"assistant","content":"synthetic"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
		}))
		t.Cleanup(vendor.Close)
		nativeUpstream = vendor.URL
		nativeOrigin := testutil.NewAPIProcess(t, vendor.URL)
		manager, err := newapibridge.NewChannelManager(pool, nativeOrigin, "sk-syntheticchanneladminfixture", identity.Principal{Issuer: issuer.URL, Subject: "alice"}, true, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(manager.Close)
		engine, err = newapibridge.NewManagedRuntime(context.Background(), newapibridge.Config{Origin: nativeOrigin, ServiceToken: "sk-syntheticgovernancefixture", AllowLoopbackHTTP: true, MaxConcurrent: 4, Journal: newapibridge.NewJournal(pool)}, manager)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(engine.Close)
		store = control.NewWithCatalog(pool, engine).WithProviderManager(manager).WithNewAPIChannels(manager)
	}

	store.WithSystem(browserHostClient(t), identity.Principal{Issuer: issuer.URL, Subject: "alice"}, "browser-fixture")
	content := testutil.ContentAudit(t, pool)
	store.WithContentAudit(content)
	api, err := identity.NewOIDC(context.Background(), issuer.URL, "aigw-management")
	if err != nil {
		t.Fatal(err)
	}
	monitor := observe.New()
	mux := http.NewServeMux()
	server := httptest.NewUnstartedServer(mux)
	origin := "http://" + server.Listener.Addr().String()
	store.WithDeviceOrigin(origin)
	svc, err := browser.New(context.Background(), pool, api, issuer.URL, browser.Config{Origin: origin, ClientID: "web-e2e", AllowLoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	webDir, err := filepath.Abs("../../web")
	if err != nil {
		t.Fatal(err)
	}
	assets, err := console.New(filepath.Join(webDir, "dist"))
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/", assets)
	mux.Handle("/auth/", svc.Handler())
	mux.Handle("/api/v1/", httpapi.NewWithRuntime(store, api, svc, monitor))
	upstream := httptest.NewServer(http.HandlerFunc(mockprovider.Handler))
	t.Cleanup(upstream.Close)
	mockGateway := &gateway.Handler{DeviceProof: store.VerifyDeviceRequest, Keywords: keyword.New(pool), Content: content, ListModels: store.KeyModels, Monitor: monitor, Circuits: circuit.New(pool), Admission: admission.New(pool), Attempts: execution.New(pool), Ledger: ledger, Authorize: store.ResolveKey, Client: upstream.Client(), Upstream: upstream.URL + "/v1/chat/completions"}
	realGateway := &gateway.Handler{DeviceProof: store.VerifyDeviceRequest, Keywords: keyword.New(pool), Content: content, ListModels: store.KeyModels, Monitor: monitor, Circuits: circuit.New(pool), Admission: admission.New(pool), Attempts: execution.New(pool), Ledger: ledger, Authorize: store.ResolveKey, Providers: catalog}
	if governed {
		realGateway.Executor = engine
		realGateway.Providers = engine
		realGateway.Report = func(id, code string) { t.Logf("governed browser request outcome=%s", code) }
	}
	// Mixed mode exists in this synthetic fixture only, to regress the legacy UI.
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			realGateway.ServeHTTP(w, r)
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
		if err != nil {
			w.WriteHeader(400)
			return
		}
		r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(data))
		var in struct{ Model string }
		if json.Unmarshal(data, &in) != nil {
			w.WriteHeader(400)
			return
		}
		if in.Model == "mock-text" {
			mockGateway.ServeHTTP(w, r)
		} else {
			realGateway.ServeHTTP(w, r)
		}
	})
	server.Start()
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "npm", "run", "test:e2e")
	cmd.Dir = webDir
	cmd.Env = append(os.Environ(), "AIGW_E2E_BASE_URL="+origin)
	if !governed {
		cmd.Env = append(cmd.Env, "AIGW_EVIDENCE_ORG="+evidenceOrg, "AIGW_EVIDENCE_PROJECT="+evidenceProject)
	}
	if governed {
		cmd.Env = append(cmd.Env, "AIGW_E2E_AUTH_MODE=newapi", "AIGW_E2E_NEWAPI_UPSTREAM="+nativeUpstream)
	}
	output, err := cmd.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal("console browser tests failed")
	}
}

type combinedFixtureCatalog struct{ real provider.Routing }

func (c combinedFixtureCatalog) Models() []provider.ModelInfo {
	return append([]provider.ModelInfo{provider.MockInfo()}, c.real.Models()...)
}

func TestLocalConsoleBrowser(t *testing.T) {
	pool, ledger, _ := testutil.Database(t)
	mux := http.NewServeMux()
	server := httptest.NewUnstartedServer(mux)
	origin := "http://" + server.Listener.Addr().String()
	local, err := localauth.New(pool, origin, true)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := local.Initialize(context.Background(), "admin", "synthetic-initial-local-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	catalog := testutil.MainlandManager(t, pool, local.Principal(admin), testutil.SupplierFixture(t))
	if err := catalog.Publish(context.Background(), local.Principal(admin), 0, testutil.MainlandConfig()); err != nil {
		t.Fatal(err)
	}
	store := control.NewWithCatalog(pool, catalog).WithProviderManager(catalog).WithLocalIdentity(local)
	store.WithDeviceOrigin(origin)
	content := testutil.ContentAudit(t, pool)
	store.WithContentAudit(content)
	sessions, err := browser.NewLocal(pool, local, origin, true)
	if err != nil {
		t.Fatal(err)
	}
	webDir, err := filepath.Abs("../../web")
	if err != nil {
		t.Fatal(err)
	}
	assets, err := console.New(filepath.Join(webDir, "dist"))
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/", assets)
	mux.Handle("/auth/", sessions.Handler())
	mux.Handle("/api/v1/", httpapi.NewWithSession(store, local, sessions))
	mux.Handle("/v1/", &gateway.Handler{DeviceProof: store.VerifyDeviceRequest, Content: content, Keywords: keyword.New(pool), Providers: catalog, Ledger: ledger, Admission: admission.New(pool), Circuits: circuit.New(pool), Attempts: execution.New(pool), Authorize: store.ResolveKey, ListModels: store.KeyModels})
	server.Start()
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "npm", "run", "test:e2e")
	cmd.Dir = webDir
	cmd.Env = append(os.Environ(), "AIGW_E2E_BASE_URL="+origin, "AIGW_E2E_AUTH_MODE=local")
	output, err := cmd.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal("local console browser tests failed")
	}
}
