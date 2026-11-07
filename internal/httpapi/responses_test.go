package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/getkin/kin-openapi/openapi3filter"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jhzhfree/ai-governance-gateway/internal/contentaudit"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/keyword"
	"github.com/jhzhfree/ai-governance-gateway/internal/newapibridge"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func responsesFixture(t *testing.T) (fixture, *atomic.Int32) {
	var calls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var payload struct {
			Model     string                           `json:"model"`
			MaxTokens int                              `json:"max_tokens"`
			Messages  []struct{ Role, Content string } `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&payload) != nil || r.URL.Path != "/internal/governance/v1/chat/completions" || payload.MaxTokens != 16 || len(payload.Messages) != 2 || payload.Messages[0].Role != "system" {
			t.Error("incorrect governed text conversion")
		}
		for key, value := range map[string]string{"X-Aigw-Config-Version": "1", "X-Aigw-Channel-Revision": r.Header.Get("X-Aigw-Channel-Revision"), "X-Aigw-Admission-Version": "2", "X-Aigw-Input-Limit": r.Header.Get("X-Aigw-Max-Input-Tokens"), "X-Aigw-Evidence-Version": "1", "X-Aigw-Request-Id": r.Header.Get("X-Aigw-Request-Id"), "X-Aigw-Attempt-Id": r.Header.Get("X-Aigw-Attempt-Id"), "X-Aigw-Usage-Source": "upstream", "X-Aigw-Physical-Attempts": "1"} {
			w.Header().Set(key, value)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"backend","object":"chat.completion","model":"backend-model","choices":[{"index":0,"message":{"role":"assistant","content":"synthetic response text"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`)
	}))
	t.Cleanup(backend.Close)
	executor, err := newapibridge.New(newapibridge.Config{Origin: backend.URL, ServiceToken: "synthetic-internal-token", Models: map[string]string{"zhipu/fixture": "backend-model"}, MaxConcurrent: 1, AllowLoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { executor.Close() })
	cfg := testutil.MainlandConfig()
	for i := range cfg.Models {
		cfg.Models[i].NewAPIChannelID = 1
		cfg.Models[i].NewAPIChannelRevision = strings.Repeat("a", 64)
		cfg.Models[i].UpstreamModel = "backend-model"
		cfg.Models[i].MaxOutputTokens = 32
	}
	f := providerExecutorSetupConfig(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("native bypass") }), executor, &cfg)
	return f, &calls
}

func TestResponsesGovernanceAuditAndStatelessBoundary(t *testing.T) {
	f, calls := responsesFixture(t)
	o, p, key := providerProject(f)
	enableContent(f, o, p, 65536)
	payload := map[string]any{"model": "zhipu/fixture", "input": "synthetic response input", "instructions": "synthetic instructions", "max_output_tokens": 16, "store": false}
	body := f.request(key.Secret, "POST", "/v1/responses", payload, 200)
	if !strings.Contains(string(body), `"object":"response"`) || !strings.Contains(string(body), `"type":"output_text"`) || !strings.Contains(string(body), `"input_tokens":2`) {
		t.Fatal("responses output contract lost")
	}
	encoded, _ := json.Marshal(payload)
	request, _ := http.NewRequest("POST", "http://fixture/v1/responses", bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	contractRoute, params, err := f.contract.FindRoute(request)
	if err != nil {
		t.Fatal("responses operation absent from OpenAPI")
	}
	contractInput := &openapi3filter.RequestValidationInput{Request: request, PathParams: params, Route: contractRoute, Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc}}
	if err := openapi3filter.ValidateRequest(context.Background(), contractInput); err != nil {
		t.Fatal("responses request violates OpenAPI")
	}
	validation := &openapi3filter.ResponseValidationInput{RequestValidationInput: contractInput, Status: 200, Header: http.Header{"Content-Type": []string{"application/json"}}}
	validation.SetBodyBytes(body)
	if err := openapi3filter.ValidateResponse(context.Background(), validation); err != nil {
		t.Fatal("responses output violates OpenAPI")
	}
	usage := parse[control.Usage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/usage", nil, 200))
	if usage.Spent != 16 || usage.Held != 0 {
		t.Fatal("responses settlement incorrect")
	}
	base := projectPath(o, p) + "/content-audit"
	page := parse[contentPage](t, f.request(f.token("alice"), "GET", base+"/records", nil, 200))
	if len(page.Data) != 1 {
		t.Fatal("audit missing")
	}
	grantAuditor(f, o, p, "auditor", true)
	detail := parse[contentaudit.Detail](t, f.request(f.token("auditor"), "POST", base+"/records/"+page.Data[0].RequestID+"/reveal", map[string]string{"reason": "verify responses protocol audit"}, 200))
	if !strings.Contains(detail.Request.Content, `"protocol":"responses"`) || detail.Response == nil || !strings.Contains(detail.Response.Content, `"object":"response"`) {
		t.Fatal("audit protocol semantics lost")
	}
	f.request("invalid-project-key", "POST", "/v1/responses", payload, 401)
	for field, value := range map[string]any{"store": true, "stream": true, "background": true, "previous_response_id": "resp_previous", "tools": []any{}, "input": []string{"unsupported array"}} {
		copy := map[string]any{}
		for k, v := range payload {
			copy[k] = v
		}
		copy[field] = value
		f.request(key.Secret, "POST", "/v1/responses", copy, 400)
	}
	delete(payload, "store")
	f.request(key.Secret, "POST", "/v1/responses", payload, 400)
	f.request(key.Secret, "GET", "/v1/responses/resp_previous", nil, 404)
	if calls.Load() != 1 {
		t.Fatal("denied or unsupported response request dispatched")
	}
	payload["store"] = false
	policyPath := projectPath(o, p) + "/keywords"
	f.request(f.token("alice"), "PUT", policyPath, map[string]any{"expected_revision": 0, "rules": []keyword.Rule{{ID: "redact", Kind: "literal", Pattern: "synthetic response text", Direction: "output", Action: "redact"}}}, 200)
	redacted := f.request(key.Secret, "POST", "/v1/responses", payload, 200)
	if !strings.Contains(string(redacted), "[REDACTED]") || strings.Contains(string(redacted), "synthetic response text") {
		t.Fatal("responses bypassed output policy")
	}
	f.request(f.token("alice"), "PUT", policyPath, map[string]any{"expected_revision": 1, "rules": []keyword.Rule{{ID: "deny", Kind: "literal", Pattern: "synthetic", Direction: "input", Action: "block"}}}, 200)
	f.request(key.Secret, "POST", "/v1/responses", payload, 403)
	f.request(f.token("alice"), "PUT", policyPath, map[string]any{"expected_revision": 2, "rules": []keyword.Rule{}}, 200)
	if _, err := f.pool.Exec(context.Background(), `UPDATE budget_accounts SET limit_amount=spent WHERE organization_id=$1 AND project_id=$2`, o.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	f.request(key.Secret, "POST", "/v1/responses", payload, 429)
	usage = parse[control.Usage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/usage", nil, 200))
	if calls.Load() != 2 || usage.Spent != 32 || usage.Held != 0 {
		t.Fatal("policy or budget refusal dispatched or lost billing")
	}
}
