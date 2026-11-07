package httpapi_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/gateway"
	"github.com/jhzhfree/ai-governance-gateway/internal/newapibridge"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

type configuredCacheExecutor struct{ gateway.ProviderExecutor }

func TestCacheTariffRequestLedgerContractAndMissingEvidence(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(strconv.Itoa(version), func(t *testing.T) { testCacheTariffRequest(t, version) })
	}
}
func testCacheTariffRequest(t *testing.T, version int) {
	tariff := strconv.Itoa(version)
	spent, held := int64(33), int64(416)
	if version == 2 {
		spent, held = 39, 800
	}
	var missing atomic.Bool
	identifiers := make(chan [2]string, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Aigw-Tariff-Version") != tariff {
			t.Error("cache tariff not negotiated")
		}
		for key, value := range map[string]string{"X-Aigw-Tariff-Version": tariff, "X-Aigw-Config-Version": "1", "X-Aigw-Channel-Revision": r.Header.Get("X-Aigw-Channel-Revision"), "X-Aigw-Admission-Version": "2", "X-Aigw-Input-Limit": r.Header.Get("X-Aigw-Max-Input-Tokens"), "X-Aigw-Evidence-Version": "1", "X-Aigw-Request-Id": r.Header.Get("X-Aigw-Request-Id"), "X-Aigw-Attempt-Id": r.Header.Get("X-Aigw-Attempt-Id"), "X-Aigw-Usage-Source": "upstream", "X-Aigw-Physical-Attempts": "1"} {
			w.Header().Set(key, value)
		}
		identifiers <- [2]string{r.Header.Get("X-Aigw-Request-Id"), r.Header.Get("X-Aigw-Attempt-Id")}
		w.Header().Set("Content-Type", "application/json")
		details := `,"prompt_tokens_details":{"cached_tokens":4,"cached_creation_tokens":5}`
		if version == 2 && !missing.Load() {
			details = `,"prompt_tokens_details":{"cached_tokens":4,"cached_creation_tokens":5,"cached_creation_tokens_details":{"ephemeral_5m_input_tokens":3,"ephemeral_1h_input_tokens":2}}`
		}
		if missing.Load() && version == 1 {
			details = ""
		}
		io.WriteString(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"synthetic"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14`+details+`}}`)
	}))
	defer upstream.Close()
	cfg := testutil.MainlandConfig()
	for i := range cfg.Models {
		cfg.Models[i].NewAPIChannelID = 1
		cfg.Models[i].NewAPIChannelRevision = strings.Repeat("a", 64)
		cfg.Models[i].UpstreamModel = "backend-model"
		cfg.Models[i].CachePricing = &provider.CachePricing{ReadPerMillion: 500000, WritePerMillion: 3000000}
		if version == 2 {
			cfg.Models[i].CachePricing.WriteTTL = &provider.CacheWriteTTL{FiveMinutePerMillion: 3000000, OneHourPerMillion: 6000000}
		}
	}
	forward := &configuredCacheExecutor{}
	f := providerExecutorSetupConfig(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("native execution bypass") }), forward, &cfg)
	executor, err := newapibridge.New(newapibridge.Config{Origin: upstream.URL, ServiceToken: "synthetic-cache-token", Models: map[string]string{"zhipu/fixture": "backend-model"}, Channels: map[string]int{"zhipu/fixture": 1}, MaxConcurrent: 1, AllowLoopbackHTTP: true, Journal: newapibridge.NewJournal(f.pool)})
	if err != nil {
		t.Fatal(err)
	}
	defer executor.Close()
	forward.ProviderExecutor = executor
	o, p, key := providerProject(f)
	models := f.request(f.token("alice"), "GET", "/api/v1/models", nil, 200)
	if !strings.Contains(string(models), `"cache_pricing"`) {
		t.Fatal("reviewed cache rates absent from catalog")
	}
	f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("zhipu/fixture", false), 200)
	ids := <-identifiers
	event := newapibridge.Event{AttemptID: ids[1], RequestID: ids[0], State: "completed", Model: "backend-model", ChannelID: 1, PromptTokens: 11, CompletionTokens: 3, TotalTokens: 14, CacheDetailsVersion: version, CacheReadTokens: 4, CacheWriteTokens: 5}
	if version == 2 {
		event.CacheWrite5mTokens, event.CacheWrite1hTokens = 3, 2
	}
	if err := newapibridge.NewJournal(f.pool).Apply(context.Background(), upstream.URL, event); err != nil {
		t.Fatal(err)
	}
	path := projectPath(o, p) + "/requests/" + ids[0]
	record := parse[control.RequestRecord](t, f.request(f.token("alice"), "GET", path, nil, 200))
	if record.Actual == nil || *record.Actual != spent || record.Reserved != held || record.CacheBilling == nil || record.CacheBilling.DetailsVersion != version || record.CacheBilling.ReadTokens == nil || *record.CacheBilling.ReadTokens != 4 || record.CacheBilling.WriteTokens == nil || *record.CacheBilling.WriteTokens != 5 || record.CacheBilling.WritePerMillion != 3000000 {
		t.Fatal("request detail lost immutable cache tariff or original facts")
	}
	if version == 2 {
		b := record.CacheBilling
		if b.FiveMinuteTokens == nil || *b.FiveMinuteTokens != 3 || b.OneHourTokens == nil || *b.OneHourTokens != 2 || b.OneHourPerMillion == nil || *b.OneHourPerMillion != 6000000 {
			t.Fatal("TTL facts or frozen rates lost")
		}
	} else if record.CacheBilling.FiveMinuteTokens != nil || record.CacheBilling.OneHourPerMillion != nil {
		t.Fatal("legacy duration invented")
	}
	f.request(f.token("outsider"), "GET", path, nil, 404)
	missing.Store(true)
	f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("zhipu/fixture", false), 502)
	usage := parse[control.Usage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/usage", nil, 200))
	if usage.Spent != spent || usage.Held != held {
		t.Fatal("unknown cache usage was silently settled or refunded")
	}
}
