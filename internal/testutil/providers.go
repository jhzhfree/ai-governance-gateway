package testutil

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
)

// MainlandConfig uses synthetic evidence/rates/bounds only. Never deploy this fixture.
func MainlandConfig() provider.Config {
	review := provider.Residency{Region: "cn-mainland", EvidenceRef: "synthetic-test-only", ExpiresAt: time.Now().Add(time.Hour)}
	cfg := provider.Config{Version: 1, DeploymentResidency: review}
	for _, p := range provider.Profiles() {
		cfg.Models = append(cfg.Models, provider.Model{ID: p.ID + "/fixture", Provider: p.ID, UpstreamModel: "fixture-model", PriceVersion: "fixture-v1", InputPerMillion: 2000000, OutputPerMillion: 4000000, MaxInputTokens: 128, MaxOutputTokens: 8, Residency: review})
	}
	return cfg
}

type mappedTransport struct {
	inner http.RoundTripper
	url   *url.URL
}

func (t mappedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	destination := *r.URL
	destination.Scheme = t.url.Scheme
	destination.Host = t.url.Host
	copy.URL = &destination
	copy.Host = r.URL.Host
	return t.inner.RoundTrip(copy)
}

// MainlandCatalog redirects only this in-process test transport to a trusted
// loopback TLS fixture. Production profiles do not expose endpoint overrides.
func MainlandCatalog(t testing.TB, handler http.Handler) (*provider.Catalog, provider.Config) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	u, _ := url.Parse(server.URL)
	client := server.Client()
	client.Transport = mappedTransport{client.Transport, u}
	cfg := MainlandConfig()
	catalog, err := provider.New(cfg, func(name string) string { return "synthetic-" + name }, client)
	if err != nil {
		t.Fatal(err)
	}
	return catalog, cfg
}
func SupplierFixture(t testing.TB) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Helper()
		var profile provider.Profile
		found := false
		for _, p := range provider.Profiles() {
			u, _ := url.Parse(p.Endpoint)
			if u.Host == r.Host && u.Path == r.URL.Path {
				profile = p
				found = true
			}
		}
		if !found || r.Header.Get("Authorization") != "Bearer synthetic-"+profile.KeyEnvironment || r.Header.Get("Cookie") != "" {
			t.Error("supplier routing/credential isolation failed")
			w.WriteHeader(401)
			return
		}
		var request struct {
			provider.Request
			StreamOptions *struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
			EnableThinking *bool `json:"enable_thinking"`
			Thinking       *struct {
				Type string `json:"type"`
			} `json:"thinking"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			w.WriteHeader(400)
			return
		}
		if request.Model != "fixture-model" {
			t.Error("public model alias was not translated")
		}
		if profile.ID == "aliyun" && (request.EnableThinking == nil || *request.EnableThinking) {
			t.Error("thinking mode not explicitly disabled")
		}
		if profile.ID != "aliyun" && (request.Thinking == nil || request.Thinking.Type != "disabled") {
			t.Error("thinking mode not explicitly disabled")
		}
		if request.Stream && profile.IncludeUsage && (request.StreamOptions == nil || !request.StreamOptions.IncludeUsage) {
			t.Error("final usage not requested")
		}
		if profile.ID == "zhipu" && request.StreamOptions != nil {
			t.Error("unsupported stream options sent to native usage profile")
		}
		if !request.Stream {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"id":"fixture-completion","object":"chat.completion","model":"fixture-model","choices":[{"index":0,"message":{"role":"assistant","content":"synthetic reply"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if profile.ID == "deepseek" {
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"synthetic\"},\"finish_reason\":null}],\"usage\":null}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5,\"prompt_cache_hit_tokens\":1,\"prompt_cache_miss_tokens\":2}}\n\ndata: [DONE]\n\n")
			return
		}

		_, _ = fmt.Fprint(w, "data: {\"id\":\"fixture-stream\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"synthetic\"},\"finish_reason\":null}]}\n\n")
		_ = http.NewResponseController(w).Flush()
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\ndata: [DONE]\n\n")
	})
}

// MainlandManager redirects only synthetic test traffic; the production Manager has no URL override.
func MainlandManager(t testing.TB, pool *pgxpool.Pool, admin identity.Principal, handler http.Handler) *provider.Manager {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	u, _ := url.Parse(server.URL)
	client := server.Client()
	client.Transport = mappedTransport{client.Transport, u}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(map[string]string{"test-v1": base64.StdEncoding.EncodeToString(key)})
	return provider.NewManager(pool, func(name string) string {
		switch name {
		case "AIGW_PROVIDER_ENCRYPTION_KEYS":
			return string(encoded)
		case "AIGW_PROVIDER_ENCRYPTION_ACTIVE_KEY":
			return "test-v1"
		}
		return "synthetic-" + name
	}, client, admin)
}
