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

	"github.com/jhzhfree/ai-governance-gateway/internal/browser"
	"github.com/jhzhfree/ai-governance-gateway/internal/console"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/execution"
	"github.com/jhzhfree/ai-governance-gateway/internal/gateway"
	"github.com/jhzhfree/ai-governance-gateway/internal/httpapi"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/mockprovider"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func TestConsoleBrowser(t *testing.T) {
	pool, ledger, _ := testutil.Database(t)
	catalog, _ := testutil.MainlandCatalog(t, testutil.SupplierFixture(t))
	store := control.NewWithCatalog(pool, combinedFixtureCatalog{catalog})
	issuer := testutil.NewIssuer(t)
	api, err := identity.NewOIDC(context.Background(), issuer.URL, "aigw-management")
	if err != nil {
		t.Fatal(err)
	}
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
	mux.Handle("/api/v1/", httpapi.NewWithSession(store, api, svc))
	upstream := httptest.NewServer(http.HandlerFunc(mockprovider.Handler))
	t.Cleanup(upstream.Close)
	mockGateway := &gateway.Handler{Attempts: execution.New(pool), Ledger: ledger, Authorize: store.ResolveKey, Client: upstream.Client(), Upstream: upstream.URL + "/v1/chat/completions"}
	realGateway := &gateway.Handler{Attempts: execution.New(pool), Ledger: ledger, Authorize: store.ResolveKey, Providers: catalog}
	// Mixed mode exists in this synthetic fixture only, to regress the legacy UI.
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
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

type combinedFixtureCatalog struct{ real *provider.Catalog }

func (c combinedFixtureCatalog) Models() []provider.ModelInfo {
	return append([]provider.ModelInfo{provider.MockInfo()}, c.real.Models()...)
}
