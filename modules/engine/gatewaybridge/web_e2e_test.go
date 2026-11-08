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
	"strings"
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
	for _, protocol := range []string{"embeddings", "cache", "messages", "messages-stream"} {
		t.Run(protocol, func(t *testing.T) { embeddedConsoleBrowser(t, protocol) })
	}
}
func embeddedConsoleBrowser(t *testing.T, protocol string) {
	issuer := testutil.NewIssuer(t)
	actor := identity.Principal{Issuer: issuer.URL, Subject: "alice"}
	cfg := testutil.MainlandConfig()
	cfg.Models = cfg.Models[:1]
	cfg.Models[0].MaxOutputTokens = 32
	path := "/compatible-mode/v1/embeddings"
	if protocol == "cache" || strings.HasPrefix(protocol, "messages") {
		cfg.Models[0].ID, cfg.Models[0].Provider = "deepseek/fixture", "deepseek"
		path = "/chat/completions"
	}
	if strings.HasPrefix(protocol, "messages") {
		path = "/anthropic/v1/messages"
	}
	f := embeddedProductFixtureAs(t, cfg, path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if protocol == "messages-stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, testutil.EmbeddedMessagesStream(`{"input_tokens":1,"output_tokens":0,"cache_read_input_tokens":2,"cache_creation_input_tokens":0}`, `{"input_tokens":1,"output_tokens":2,"cache_read_input_tokens":2,"cache_creation_input_tokens":0}`, "synthetic native response"))
			return
		}
		if strings.HasPrefix(protocol, "messages") {
			io.WriteString(w, `{"id":"msg_browser","type":"message","role":"assistant","model":"fixture-model","content":[{"type":"text","text":"synthetic native response"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":2,"cache_read_input_tokens":2,"cache_creation_input_tokens":0}}`)
			return
		}
		if protocol == "cache" {
			io.WriteString(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"synthetic cache response"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5,"prompt_cache_hit_tokens":2,"prompt_cache_miss_tokens":1}}`)
			return
		}
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
	command.Env = append(os.Environ(), "AIGW_E2E_BASE_URL="+origin, "AIGW_E2E_AUTH_MODE=embedded", "AIGW_E2E_EMBEDDING_KEY="+f.key, "AIGW_E2E_EMBEDDED_PROTOCOL="+protocol)
	output, err := command.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal("embedded browser failed")
	}
	wantProtocol, wantCost := "embeddings_v1", 6
	if protocol == "cache" {
		wantProtocol, wantCost = "deepseek_cache_v1", 11
	}
	if strings.HasPrefix(protocol, "messages") {
		wantProtocol, wantCost = "deepseek_messages_v1", 11
	}
	var events, receipts int
	if err := f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM engine_execution_bindings WHERE protocol=$1),(SELECT count(*) FROM engine_completion_receipts WHERE actual=$2)`, wantProtocol, wantCost).Scan(&events, &receipts); err != nil || events != 1 || receipts != 1 || f.calls.Load() != 1 {
		t.Fatal("browser did not exercise durable embedding", err)
	}
}
