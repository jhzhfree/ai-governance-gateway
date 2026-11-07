package httpapi_test

import (
	"context"
	"encoding/json"
	"github.com/jhzhfree/ai-governance-gateway/internal/contentaudit"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/keyword"
	"github.com/jhzhfree/ai-governance-gateway/internal/newapibridge"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddingGovernanceAuditAndKeywordBoundary(t *testing.T) {
	calls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/internal/governance/v1/embeddings" {
			t.Error("wrong protocol path")
		}
		var payload map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&payload) != nil || len(payload["input"]) == 0 || len(payload["messages"]) != 0 || len(payload["max_tokens"]) != 0 {
			t.Error("invalid embedding wire body")
		}
		for key, value := range map[string]string{"X-Aigw-Config-Version": "1", "X-Aigw-Channel-Revision": r.Header.Get("X-Aigw-Channel-Revision"), "X-Aigw-Protocol": "embeddings", "X-Aigw-Admission-Version": "2", "X-Aigw-Input-Limit": r.Header.Get("X-Aigw-Max-Input-Tokens"), "X-Aigw-Evidence-Version": "1", "X-Aigw-Request-Id": r.Header.Get("X-Aigw-Request-Id"), "X-Aigw-Attempt-Id": r.Header.Get("X-Aigw-Attempt-Id"), "X-Aigw-Usage-Source": "upstream", "X-Aigw-Physical-Attempts": "1"} {
			w.Header().Set(key, value)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list","model":"backend-model","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":2,"total_tokens":2}}`)
	}))
	defer backend.Close()
	executor, err := newapibridge.New(newapibridge.Config{Origin: backend.URL, ServiceToken: "synthetic-internal-token", Models: map[string]string{"zhipu/fixture": "backend-model"}, MaxConcurrent: 1, AllowLoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	defer executor.Close()
	cfg := testutil.MainlandConfig()
	for i := range cfg.Models {
		cfg.Models[i].API = "embeddings"
		cfg.Models[i].NewAPIChannelID = 1
		cfg.Models[i].NewAPIChannelRevision = strings.Repeat("a", 64)
		cfg.Models[i].UpstreamModel = "backend-model"
		cfg.Models[i].MaxOutputTokens = 0
		cfg.Models[i].OutputPerMillion = 0
	}
	f := providerExecutorSetupConfig(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("native embedding bypass") }), executor, &cfg)
	o, p, key := providerProject(f)
	enableContent(f, o, p, 65536)
	payload := map[string]any{"model": "zhipu/fixture", "input": "synthetic embedding text", "dimensions": 2, "encoding_format": "float"}
	body := f.request(key.Secret, "POST", "/v1/embeddings", payload, 200)
	if !strings.Contains(string(body), `"embedding":[0.1,0.2]`) {
		t.Fatal("vector lost")
	}
	usage := parse[control.Usage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/usage", nil, 200))
	if usage.Spent != 4 || usage.Held != 0 {
		t.Fatal("input-only settlement failed")
	}
	base := projectPath(o, p) + "/content-audit"
	page := parse[contentPage](t, f.request(f.token("alice"), "GET", base+"/records", nil, 200))
	if len(page.Data) != 1 {
		t.Fatal("embedding content audit missing")
	}
	grantAuditor(f, o, p, "auditor", true)
	detail := parse[contentaudit.Detail](t, f.request(f.token("auditor"), "POST", base+"/records/"+page.Data[0].RequestID+"/reveal", map[string]string{"reason": "verify synthetic embedding audit"}, 200))
	if !strings.Contains(detail.Request.Content, "synthetic embedding text") || !strings.Contains(detail.Request.Content, "embeddings") || detail.Response == nil || !strings.Contains(detail.Response.Content, "0.1") {
		t.Fatal("embedding audit semantics lost")
	}
	f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("zhipu/fixture", false), 400)
	payload["input"] = []int{1, 2}
	f.request(key.Secret, "POST", "/v1/embeddings", payload, 400)
	payload["input"] = "synthetic embedding text"
	f.request(f.token("alice"), "PUT", projectPath(o, p)+"/keywords", map[string]any{"expected_revision": 0, "rules": []keyword.Rule{{ID: "input", Kind: "literal", Pattern: "synthetic", Direction: "input", Action: "block"}}}, 200)
	f.request(key.Secret, "POST", "/v1/embeddings", payload, 403)
	f.request(f.token("alice"), "PUT", projectPath(o, p)+"/keywords", map[string]any{"expected_revision": 1, "rules": []keyword.Rule{{ID: "output", Kind: "literal", Pattern: "synthetic", Direction: "output", Action: "alert"}}}, 200)
	f.request(key.Secret, "POST", "/v1/embeddings", payload, 403)
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM inference_attempts`).Scan(&count); err != nil || count != 1 || calls != 1 {
		t.Fatal("rejected embedding dispatched")
	}
}
