package httpapi_test

import (
	"bufio"
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
	"sync"
	"testing"
	"time"
)

func TestMessagesGovernanceCacheAuditAndHeaders(t *testing.T) {
	f, calls := responsesFixture(t, responsesFixtureOptions{cacheEvidence: true})
	o, p, key := providerProject(f)
	enableContent(f, o, p, 65536)
	payload := map[string]any{"model": "zhipu/fixture", "max_tokens": 8, "messages": []any{map[string]any{"role": "user", "content": []map[string]string{{"type": "text", "text": "syn"}, {"type": "text", "text": "thetic messages input"}}}}}
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
	if !strings.Contains(detail.Request.Content, `"protocol":"messages"`) || !strings.Contains(detail.Request.Content, "synthetic messages input") || detail.Response == nil || !strings.Contains(detail.Response.Content, `"cache_creation_input_tokens":5`) {
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
	for _, extra := range []map[string]any{{"stream": true}, {"tools": []any{}}, {"thinking": map[string]any{"type": "enabled"}}, {"messages": []any{map[string]any{"role": "user", "content": []any{map[string]string{"type": "image", "text": "unsupported block"}}}}}} {
		copy := map[string]any{}
		for k, v := range payload {
			copy[k] = v
		}
		for k, v := range extra {
			copy[k] = v
		}
		f.request(key.Secret, "POST", "/v1/messages", copy, 400)
	}
	for _, raw := range []string{
		`[{"type":"text","text":"x","cache_control":{"type":"ephemeral"}}]`,
		`[{"type":"text","text":"x","citations":[]}]`,
		`[{"type":"tool_result","text":"x"}]`,
	} {
		copy := map[string]any{}
		for k, v := range payload {
			copy[k] = v
		}
		copy["messages"] = []map[string]any{{"role": "user", "content": json.RawMessage(raw)}}
		f.request(key.Secret, "POST", "/v1/messages", copy, 400)
		copy["messages"] = payload["messages"]
		copy["system"] = json.RawMessage(raw)
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
	f.request(f.token("alice"), "PUT", policyPath, map[string]any{"expected_revision": 1, "rules": []keyword.Rule{{ID: "deny", Kind: "literal", Pattern: "synthetic messages", Direction: "input", Action: "block"}}}, 200)
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

func TestMessagesStreamProofAuditAndEarlyUsage(t *testing.T) {
	for _, mode := range []string{"complete", "missing-proof", "audit-failure", "checkpoint-failure", "length", "conflict"} {
		t.Run(mode, func(t *testing.T) {
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			f, calls := responsesFixture(t, responsesFixtureOptions{messagesStream: true, cacheEvidence: true, release: release, initialMode: mode, omitProof: mode == "missing-proof", extraDelta: mode == "checkpoint-failure", finishReason: map[bool]string{true: "length", false: "stop"}[mode == "length"]})
			t.Cleanup(unblock)
			o, p, key := providerProject(f)
			enableContent(f, o, p, 65536)
			if mode == "audit-failure" {
				if _, err := f.pool.Exec(context.Background(), `ALTER TABLE content_audit_payloads ADD CONSTRAINT reject_response CHECK(direction<>'response') NOT VALID`); err != nil {
					t.Fatal(err)
				}
			}
			request, _ := http.NewRequest("POST", f.server.URL+"/v1/messages", strings.NewReader(`{"model":"zhipu/fixture","max_tokens":8,"stream":true,"messages":[{"role":"user","content":"synthetic"}]}`))
			request.Header.Set("X-Api-Key", key.Secret)
			response, err := f.server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			reader := bufio.NewReader(response.Body)
			var early strings.Builder
			for range 32 {
				line, err := reader.ReadString('\n')
				if err != nil {
					t.Fatal("text not delivered before completion", err)
				}
				early.WriteString(line)
				if strings.HasPrefix(line, "data: ") && strings.Contains(line, `"type":"content_block_delta"`) {
					break
				}
			}
			if response.StatusCode != 200 || !strings.Contains(early.String(), "synthetic response text") || !strings.Contains(early.String(), `"output_tokens":1`) || !strings.Contains(early.String(), `"cache_creation_input_tokens":5`) || strings.Contains(early.String(), "message_stop") || response.Header.Get("X-Aigw-Initial-Usage") != "" {
				t.Fatal("initial evidence or live delivery contract invalid")
			}
			var parts int
			if err := f.pool.QueryRow(context.Background(), `SELECT parts FROM content_audit_checkpoints`).Scan(&parts); err != nil || parts != 2 {
				t.Fatal("initial events and delta must checkpoint before delivery", parts, err)
			}
			usage := parse[control.Usage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/usage", nil, 200))
			if usage.Spent != 0 || usage.Held != 416 {
				t.Fatal("provisional counters settled budget")
			}
			if mode == "checkpoint-failure" {
				if _, err := f.pool.Exec(context.Background(), `ALTER TABLE content_audit_checkpoint_parts ADD CONSTRAINT reject_future CHECK(false) NOT VALID`); err != nil {
					t.Fatal(err)
				}
			}
			unblock()
			tail, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			body := early.String() + string(tail)
			completed := strings.Contains(body, `"type":"message_stop"`)
			if completed != (mode == "complete" || mode == "length") || strings.Contains(body, "[DONE]") {
				t.Fatal("unproven or wrong terminal escaped", body)
			}
			if mode == "length" && !strings.Contains(body, `"stop_reason":"max_tokens"`) {
				t.Fatal("output bound termination lost")
			}
			if (mode == "audit-failure" || mode == "checkpoint-failure") && (!strings.Contains(body, "content_audit_unavailable") || !strings.Contains(body, `"type":"api_error"`)) {
				t.Fatal("audit failure not Messages-shaped")
			}
			if mode == "checkpoint-failure" && strings.Contains(body, "uncommitted-second") {
				t.Fatal("uncheckpointed text escaped")
			}
			usage = parse[control.Usage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/usage", nil, 200))
			if mode == "missing-proof" || mode == "checkpoint-failure" || mode == "conflict" {
				if usage.Spent != 0 || usage.Held != 416 {
					t.Fatal("unproven usage settled or refunded")
				}
			} else if usage.Spent != 33 || usage.Held != 0 {
				t.Fatal("known supplier charge lost")
			}
			if calls.Load() != 1 {
				t.Fatal("stream replayed")
			}
			if mode == "complete" {
				grantAuditor(f, o, p, "auditor", true)
				var id string
				if err := f.pool.QueryRow(context.Background(), `SELECT request_id FROM content_audit_records`).Scan(&id); err != nil {
					t.Fatal(err)
				}
				detail := parse[contentaudit.Detail](t, f.request(f.token("auditor"), "POST", projectPath(o, p)+"/content-audit/records/"+id+"/reveal", map[string]string{"reason": "verify Messages event audit"}, 200))
				if detail.Response == nil || detail.Response.Content != body || !strings.Contains(detail.Request.Content, `"protocol":"messages"`) {
					t.Fatal("audited public stream differs from delivered bytes")
				}
			}
		})
	}
}

func TestMessagesStreamMissingInitialEvidenceRetainsHold(t *testing.T) {
	for _, mode := range []string{"missing", "negative", "incomplete"} {
		t.Run(mode, func(t *testing.T) {
			f, calls := responsesFixture(t, responsesFixtureOptions{messagesStream: true, cacheEvidence: true, initialMode: mode})
			o, p, key := providerProject(f)
			body := f.request(key.Secret, "POST", "/v1/messages", map[string]any{"model": "zhipu/fixture", "max_tokens": 8, "stream": true, "messages": []map[string]string{{"role": "user", "content": "synthetic"}}}, 502)
			if strings.Contains(string(body), "synthetic response text") {
				t.Fatal("text leaked without initial evidence")
			}
			usage := parse[control.Usage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/usage", nil, 200))
			if usage.Spent != 0 || usage.Held != 416 || calls.Load() != 1 {
				t.Fatal("missing initial evidence incorrectly billed or retried")
			}
		})
	}
}

func TestMessagesStreamClientCancellationRetainsUnknownHold(t *testing.T) {
	release := make(chan struct{})
	f, calls := responsesFixture(t, responsesFixtureOptions{messagesStream: true, cacheEvidence: true, release: release})
	t.Cleanup(func() { close(release) })
	o, p, key := providerProject(f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", f.server.URL+"/v1/messages", strings.NewReader(`{"model":"zhipu/fixture","max_tokens":8,"stream":true,"messages":[{"role":"user","content":"synthetic"}]}`))
	req.Header.Set("X-Api-Key", key.Secret)
	res, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	reader := bufio.NewReader(res.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(line, "synthetic response text") {
			break
		}
	}
	cancel()
	res.Body.Close()
	uncertain := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var state string
		if err := f.pool.QueryRow(context.Background(), `SELECT state FROM budget_reservations WHERE request_id=$1`, res.Header.Get("X-Request-ID")).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "uncertain" {
			uncertain = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	usage := parse[control.Usage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/usage", nil, 200))
	if !uncertain || calls.Load() != 1 || usage.Held != 416 || usage.Spent != 0 {
		t.Fatal("client cancellation replayed, refunded or settled")
	}
}

func TestMessagesStreamKeywordAndBudgetGovernance(t *testing.T) {
	f, calls := responsesFixture(t, responsesFixtureOptions{messagesStream: true, cacheEvidence: true})
	o, p, key := providerProject(f)
	payload := map[string]any{"model": "zhipu/fixture", "max_tokens": 8, "stream": true, "messages": []map[string]string{{"role": "user", "content": "synthetic"}}}
	path := projectPath(o, p) + "/keywords"
	f.request(f.token("alice"), "PUT", path, map[string]any{"expected_revision": 0, "rules": []keyword.Rule{{ID: "redact", Kind: "literal", Pattern: "synthetic response text", Direction: "output", Action: "redact"}}}, 200)
	f.request(key.Secret, "POST", "/v1/messages", payload, 400)
	if calls.Load() != 0 {
		t.Fatal("unsupported streaming redaction dispatched")
	}
	f.request(f.token("alice"), "PUT", path, map[string]any{"expected_revision": 1, "rules": []keyword.Rule{{ID: "alert", Kind: "literal", Pattern: "synthetic response text", Direction: "output", Action: "alert"}}}, 200)
	body := string(f.request(key.Secret, "POST", "/v1/messages", payload, 200))
	if !strings.Contains(body, "synthetic response text") || !strings.Contains(body, `"type":"message_stop"`) {
		t.Fatal("stream policy bypass or completion lost")
	}
	var matches int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM keyword_matches WHERE rule_id='alert' AND direction='output' AND action='alert'`).Scan(&matches); err != nil || matches != 1 {
		t.Fatal("stream alert not recorded")
	}
	f.request(f.token("alice"), "PUT", path, map[string]any{"expected_revision": 2, "rules": []keyword.Rule{{ID: "deny", Kind: "literal", Pattern: "synthetic", Direction: "input", Action: "block"}}}, 200)
	f.request(key.Secret, "POST", "/v1/messages", payload, 403)
	f.request(f.token("alice"), "PUT", path, map[string]any{"expected_revision": 3, "rules": []keyword.Rule{}}, 200)
	if _, err := f.pool.Exec(context.Background(), `UPDATE budget_accounts SET limit_amount=spent WHERE organization_id=$1 AND project_id=$2`, o.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	f.request(key.Secret, "POST", "/v1/messages", payload, 429)
	f.request("invalid", "POST", "/v1/messages", payload, 401)
	usage := parse[control.Usage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/usage", nil, 200))
	if calls.Load() != 1 || usage.Spent != 33 || usage.Held != 0 {
		t.Fatal("policy or budget changed fees or dispatched forbidden traffic")
	}
}
