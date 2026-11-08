//go:build web_e2e

package gatewaybridge

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/browser"
	"github.com/jhzhfree/ai-governance-gateway/internal/console"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/httpapi"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/observe"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func TestEmbeddedConsoleBrowser(t *testing.T) {
	issuer := testutil.NewIssuer(t)
	actor := identity.Principal{Issuer: issuer.URL, Subject: "alice"}
	cfg := testutil.MainlandConfig()
	cfg.Models = cfg.Models[:1]
	cfg.Models[0].MaxOutputTokens = 32
	f := embeddedProductFixtureAs(t, cfg, "/compatible-mode/v1/embeddings", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list","model":"fixture-model","data":[{"object":"embedding","index":0,"embedding":[0.1,-0.2]}],"usage":{"prompt_tokens":3,"total_tokens":3}}`)
	}), actor)
	store := control.NewWithCatalog(f.db, f.managed).WithProviderManager(f.source)
	oidc, err := identity.NewOIDC(context.Background(), issuer.URL, "aigw-management")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	server := httptest.NewUnstartedServer(mux)
	origin := "http://" + server.Listener.Addr().String()
	svc, err := browser.New(context.Background(), f.db, oidc, issuer.URL, browser.Config{Origin: origin, ClientID: "web-e2e", AllowLoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	webDir, err := filepath.Abs("../../../web")
	if err != nil {
		t.Fatal(err)
	}
	assets, err := console.New(filepath.Join(webDir, "dist"))
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/", assets)
	mux.Handle("/auth/", svc.Handler())
	mux.Handle("/api/v1/", httpapi.NewWithRuntime(store, oidc, svc, observe.New()))
	target, _ := url.Parse(f.server.URL)
	mux.Handle("/v1/", httputil.NewSingleHostReverseProxy(target))
	server.Start()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "npm", "run", "test:e2e")
	command.Dir = webDir
	command.Env = append(os.Environ(), "AIGW_E2E_BASE_URL="+origin, "AIGW_E2E_AUTH_MODE=embedded", "AIGW_E2E_EMBEDDING_KEY="+f.key)
	output, err := command.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal("embedded browser failed")
	}
	var events, receipts int
	if err := f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM engine_execution_bindings WHERE protocol='embeddings_v1'),(SELECT count(*) FROM engine_completion_receipts WHERE actual=6)`).Scan(&events, &receipts); err != nil || events != 1 || receipts != 1 || f.calls.Load() != 1 {
		t.Fatal("browser did not exercise durable embedding", err)
	}
}
