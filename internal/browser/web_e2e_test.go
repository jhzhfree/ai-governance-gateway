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
	"github.com/jhzhfree/ai-governance-gateway/internal/circuit"
	"github.com/jhzhfree/ai-governance-gateway/internal/console"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/execution"
	"github.com/jhzhfree/ai-governance-gateway/internal/gateway"
	"github.com/jhzhfree/ai-governance-gateway/internal/httpapi"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/keyword"
	"github.com/jhzhfree/ai-governance-gateway/internal/mockprovider"
	"github.com/jhzhfree/ai-governance-gateway/internal/observe"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func TestConsoleBrowser(t *testing.T) {
	pool, ledger, _ := testutil.Database(t)
	issuer := testutil.NewIssuer(t)
	catalog := testutil.MainlandManager(t, pool, identity.Principal{Issuer: issuer.URL, Subject: "alice"}, testutil.SupplierFixture(t))
	if err := catalog.Publish(context.Background(), identity.Principal{Issuer: issuer.URL, Subject: "alice"}, 0, testutil.MainlandConfig()); err != nil {
		t.Fatal(err)
	}
	store := control.NewWithCatalog(pool, combinedFixtureCatalog{catalog}).WithProviderManager(catalog)
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
	mockGateway := &gateway.Handler{Keywords: keyword.New(pool), Content: content, ListModels: store.KeyModels, Monitor: monitor, Circuits: circuit.New(pool), Admission: admission.New(pool), Attempts: execution.New(pool), Ledger: ledger, Authorize: store.ResolveKey, Client: upstream.Client(), Upstream: upstream.URL + "/v1/chat/completions"}
	realGateway := &gateway.Handler{Keywords: keyword.New(pool), Content: content, ListModels: store.KeyModels, Monitor: monitor, Circuits: circuit.New(pool), Admission: admission.New(pool), Attempts: execution.New(pool), Ledger: ledger, Authorize: store.ResolveKey, Providers: catalog}
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
