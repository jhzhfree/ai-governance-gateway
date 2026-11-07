package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/jhzhfree/ai-governance-gateway/internal/contentaudit"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/keyword"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestMessagesGovernanceCacheAuditAndHeaders(t *testing.T) {
	f, calls := responsesFixture(t, responsesFixtureOptions{cacheEvidence: true})
	o, p, key := providerProject(f)
	enableContent(f, o, p, 65536)
	payload := map[string]any{"model": "zhipu/fixture", "max_tokens": 8, "messages": []any{map[string]any{"role": "user", "content": "synthetic messages input"}}}
	encoded, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", f.server.URL+"/v1/messages", bytes.NewReader(encoded))
	req.Header.Set("X-Api-Key", key.Secret)
	req.Header.Set("Anthropic-Version", "2023-06-01")
	res, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	var result provider.MessagesResult
	if res.StatusCode != 200 || json.Unmarshal(body, &result) != nil || result.Type != "message" || result.StopReason != "end_turn" || result.Usage.InputTokens != 2 || result.Usage.CacheReadInputTokens != 4 || result.Usage.CacheCreationInputTokens != 5 {
		t.Fatal("messages contract or original counters lost", res.StatusCode)
	}

	contractRequest, _ := http.NewRequest("POST", "http://fixture/v1/messages", bytes.NewReader(encoded))
	contractRequest.Header.Set("Content-Type", "application/json")
	contractRoute, params, err := f.contract.FindRoute(contractRequest)
	if err != nil {
		t.Fatal("messages OpenAPI route missing", err)
	}
	contractInput := &openapi3filter.RequestValidationInput{Request: contractRequest, PathParams: params, Route: contractRoute, Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc}}
	if err := openapi3filter.ValidateRequest(context.Background(), contractInput); err != nil {
		t.Fatal("messages request contract", err)
	}
	validation := &openapi3filter.ResponseValidationInput{RequestValidationInput: contractInput, Status: 200, Header: http.Header{"Content-Type": []string{"application/json"}}}
	validation.SetBodyBytes(body)
	if err := openapi3filter.ValidateResponse(context.Background(), validation); err != nil {
		t.Fatal("messages response contract", err)
	}
	usage := parse[control.Usage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/usage", nil, 200))
	if usage.Spent != 33 || usage.Held != 0 {
		t.Fatal("messages billing incorrect")
	}
	base := projectPath(o, p) + "/content-audit"
	var id string
	if err := f.pool.QueryRow(context.Background(), `SELECT request_id FROM content_audit_records`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	grantAuditor(f, o, p, "auditor", true)
	detail := parse[contentaudit.Detail](t, f.request(f.token("auditor"), "POST", base+"/records/"+id+"/reveal", map[string]string{"reason": "verify public messages audit"}, 200))
	if !strings.Contains(detail.Request.Content, `"protocol":"messages"`) || detail.Response == nil || !strings.Contains(detail.Response.Content, `"cache_creation_input_tokens":5`) {
		t.Fatal("public messages not auditable")
	}
	// Ambiguous credentials, unreviewed versions and beta features must not dispatch.
	for _, headers := range []map[string]string{{"Authorization": "Bearer " + key.Secret, "X-Api-Key": key.Secret}, {"X-Api-Key": key.Secret, "Anthropic-Version": "2026-10-21"}, {"X-Api-Key": key.Secret, "Anthropic-Beta": "unreviewed"}} {
		r, _ := http.NewRequest("POST", f.server.URL+"/v1/messages", bytes.NewReader(encoded))
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		response, err := f.server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 400 || !strings.Contains(string(raw), `"type":"error"`) || !strings.Contains(string(raw), `"type":"invalid_request_error"`) {
			t.Fatal("unsafe header accepted or wrong error envelope")
		}
	}
	for _, extra := range []map[string]any{{"stream": true}, {"tools": []any{}}, {"thinking": map[string]any{"type": "enabled"}}, {"messages": []any{map[string]any{"role": "user", "content": []any{map[string]string{"type": "text", "text": "unsupported block"}}}}}} {
		copy := map[string]any{}
		for k, v := range payload {
			copy[k] = v
		}
		for k, v := range extra {
			copy[k] = v
		}
		f.request(key.Secret, "POST", "/v1/messages", copy, 400)
	}
	f.request("invalid", "POST", "/v1/messages", payload, 401)
	if calls.Load() != 1 {
		t.Fatal("rejected request reached supplier")
	}

	policyPath := projectPath(o, p) + "/keywords"
	f.request(f.token("alice"), "PUT", policyPath, map[string]any{"expected_revision": 0, "rules": []keyword.Rule{{ID: "redact", Kind: "literal", Pattern: "synthetic response text", Direction: "output", Action: "redact"}}}, 200)
	redacted := f.request(key.Secret, "POST", "/v1/messages", payload, 200)
	if !strings.Contains(string(redacted), "[REDACTED]") || strings.Contains(string(redacted), "synthetic response text") {
		t.Fatal("messages output bypassed policy")
	}
	f.request(f.token("alice"), "PUT", policyPath, map[string]any{"expected_revision": 1, "rules": []keyword.Rule{{ID: "deny", Kind: "literal", Pattern: "synthetic", Direction: "input", Action: "block"}}}, 200)
	denied := f.request(key.Secret, "POST", "/v1/messages", payload, 403)
	if !strings.Contains(string(denied), `"type":"permission_error"`) {
		t.Fatal("policy error protocol lost")
	}
	f.request(f.token("alice"), "PUT", policyPath, map[string]any{"expected_revision": 2, "rules": []keyword.Rule{}}, 200)
	if _, err := f.pool.Exec(context.Background(), `UPDATE budget_accounts SET limit_amount=spent WHERE organization_id=$1 AND project_id=$2`, o.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	f.request(key.Secret, "POST", "/v1/messages", payload, 429)
	usage = parse[control.Usage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/usage", nil, 200))
	if calls.Load() != 2 || usage.Spent != 66 || usage.Held != 0 {
		t.Fatal("policy or budget changed known cost or dispatched forbidden request")
	}
}
func TestMessagesMissingEvidenceRetainsHold(t *testing.T) {
	f, calls := responsesFixture(t, responsesFixtureOptions{cacheEvidence: true, missingCache: true})
	o, p, key := providerProject(f)
	f.request(key.Secret, "POST", "/v1/messages", map[string]any{"model": "zhipu/fixture", "max_tokens": 8, "messages": []any{map[string]string{"role": "user", "content": "synthetic"}}}, 502)
	usage := parse[control.Usage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/usage", nil, 200))
	if calls.Load() != 1 || usage.Spent != 0 || usage.Held != 416 {
		t.Fatal("unknown cache usage incorrectly settled or refunded")
	}
}

func TestMessagesRequiresConfiguredRawCacheEvidence(t *testing.T) {
	f, calls := responsesFixture(t)
	_, _, key := providerProject(f)
	f.request(key.Secret, "POST", "/v1/messages", map[string]any{"model": "zhipu/fixture", "max_tokens": 8, "messages": []any{map[string]string{"role": "user", "content": "synthetic"}}}, 400)
	if calls.Load() != 0 {
		t.Fatal("missing review did not fail before dispatch")
	}
}
