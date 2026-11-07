package httpapi_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/contentaudit"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

type contentPage struct {
	Data []contentaudit.Meta `json:"data"`
	Next string              `json:"next_cursor"`
}

func enableContent(f fixture, o control.Organization, p control.Project, max int) {
	f.request(f.token("alice"), "PUT", projectPath(o, p)+"/content-audit/policy", map[string]any{"enabled": true, "retention_days": 1, "max_bytes": max, "consent_ref": "synthetic-consent-v1", "expected_revision": 0}, 200)
}
func grantAuditor(f fixture, o control.Organization, p control.Project, subject string, active bool) {
	f.request(f.token("alice"), "PUT", projectPath(o, p)+"/content-audit/auditors", map[string]any{"issuer": f.issuer.URL, "subject": subject, "active": active}, 204)
}
func TestContentAuditOptInEnvelopeAndIndependentReader(t *testing.T) {
	f := providerSetup(t, testutil.SupplierFixture(t))
	o, p, key := providerProject(f)
	base := projectPath(o, p) + "/content-audit"
	ctx := context.Background()
	f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("aliyun/fixture", false), 200)
	page := parse[contentPage](t, f.request(f.token("alice"), "GET", base+"/records", nil, 200))
	if len(page.Data) != 0 {
		t.Fatal("content captured without opt in")
	}
	enableContent(f, o, p, 65536)
	f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("aliyun/fixture", false), 200)
	page = parse[contentPage](t, f.request(f.token("alice"), "GET", base+"/records", nil, 200))
	if len(page.Data) != 1 || page.Data[0].State != "complete" || page.Data[0].PolicyRevision != 1 {
		t.Fatal("audit did not seal policy snapshot")
	}
	id := page.Data[0].RequestID
	path := base + "/records/" + id + "/reveal"
	f.request(f.token("alice"), "POST", path, map[string]string{"reason": "owner lacks explicit content grant"}, 403)
	grantAuditor(f, o, p, "auditor", true)
	detail := parse[contentaudit.Detail](t, f.request(f.token("auditor"), "POST", path, map[string]string{"reason": "核对合成测试内容"}, 200))
	if !strings.Contains(detail.Request.Content, "synthetic mainland protocol test") || detail.Response == nil || detail.Response.Truncated || detail.Request.Truncated {
		t.Fatal("content detail missing")
	}
	if strings.Contains(detail.Request.Content, key.Secret) || strings.Contains(detail.Response.Content, "synthetic-AIGW") {
		t.Fatal("credentials captured outside content")
	}
	f.request(f.token("auditor"), "GET", projectPath(o, p)+"/usage", nil, 404)
	f.request(f.token("auditor"), "POST", path, map[string]string{"reason": ""}, 400)
	var cipher []byte
	if err := f.pool.QueryRow(ctx, `SELECT ciphertext FROM content_audit_payloads WHERE request_id=$1 AND direction='request'`, id).Scan(&cipher); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cipher), "synthetic mainland protocol test") {
		t.Fatal("stored plaintext")
	}
	if _, err := f.pool.Exec(ctx, `UPDATE content_audit_payloads SET ciphertext=set_byte(ciphertext,0,get_byte(ciphertext,0)#1) WHERE request_id=$1 AND direction='response'`, id); err != nil {
		t.Fatal(err)
	}
	f.request(f.token("auditor"), "POST", path, map[string]string{"reason": "verify tamper denial"}, 503)
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM content_audit_access_log WHERE request_id=$1`, id).Scan(&count); err != nil || count != 2 {
		t.Fatal("content access attempts not logged before decryption")
	}
	grantAuditor(f, o, p, "auditor", false)
	f.request(f.token("auditor"), "POST", path, map[string]string{"reason": "revoked"}, 403)
	f.request(key.Secret, "GET", base+"/records", nil, 401)
}
func TestContentAuditSSETruncationAndSealFailureGateDone(t *testing.T) {
	long := strings.Repeat("synthetic-", 300)
	supplier := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"" + long + "\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\ndata: [DONE]\n\n"))
	})
	f := providerSetup(t, supplier)
	o, p, key := providerProject(f)
	base := projectPath(o, p) + "/content-audit"
	enableContent(f, o, p, 1024)
	grantAuditor(f, o, p, "auditor", true)
	response := f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("aliyun/fixture", true), 200)
	if !strings.HasSuffix(string(response), "data: [DONE]\n\n") {
		t.Fatal("sealed stream lost DONE")
	}
	page := parse[contentPage](t, f.request(f.token("alice"), "GET", base+"/records", nil, 200))
	detail := parse[contentaudit.Detail](t, f.request(f.token("auditor"), "POST", base+"/records/"+page.Data[0].RequestID+"/reveal", map[string]string{"reason": "verify bounded stream"}, 200))
	if detail.Response == nil || !detail.Response.Truncated || detail.Response.CapturedBytes > 1024 || detail.Response.ObservedBytes <= 1024 || len(detail.Response.SHA256) != 64 {
		t.Fatal("stream bounds or observed hash invalid")
	}
	if _, err := f.pool.Exec(context.Background(), `ALTER TABLE content_audit_payloads ADD CONSTRAINT reject_response CHECK(direction<>'response') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	response = f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("aliyun/fixture", true), 200)
	if strings.Contains(string(response), "[DONE]") || !strings.Contains(string(response), "content_audit_unavailable") {
		t.Fatal("seal failure acknowledged completed stream")
	}
	page = parse[contentPage](t, f.request(f.token("alice"), "GET", base+"/records?limit=1", nil, 200))
	if page.Data[0].State != "capturing" {
		t.Fatal("failed seal claimed complete")
	}
}
func TestContentPolicyRevisionAuditAndExpiryPurge(t *testing.T) {
	f := providerSetup(t, testutil.SupplierFixture(t))
	o, p, key := providerProject(f)
	base := projectPath(o, p) + "/content-audit"
	enableContent(f, o, p, 65536)
	grantAuditor(f, o, p, "auditor", true)
	f.request(f.token("alice"), "PUT", base+"/policy", map[string]any{"enabled": true, "retention_days": 2, "max_bytes": 65536, "consent_ref": "new", "expected_revision": 0}, 409)
	f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("aliyun/fixture", false), 200)
	page := parse[contentPage](t, f.request(f.token("alice"), "GET", base+"/records", nil, 200))
	id := page.Data[0].RequestID
	if _, err := f.pool.Exec(context.Background(), `UPDATE content_audit_records SET expires_at=clock_timestamp()-interval '1 second' WHERE request_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	f.request(f.token("auditor"), "POST", base+"/records/"+id+"/reveal", map[string]string{"reason": "expired content"}, 404)
	service := contentaudit.New(f.pool, nil)
	n, err := service.Purge(context.Background(), 10)
	if err != nil || n != 1 {
		t.Fatal("retention purge failed", err)
	}
	n, err = service.Purge(context.Background(), 10)
	if err != nil || n != 0 {
		t.Fatal("retention purge not idempotent")
	}
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM content_audit_payloads WHERE request_id=$1`, id).Scan(&count); err != nil || count != 0 {
		t.Fatal("expired ciphertext retained in active tables")
	}
	checkpoint, err := service.Verify(context.Background(), o.ID)
	if err != nil || !checkpoint.Valid || checkpoint.Sequence != 3 {
		t.Fatal("chain verification failed", err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE content_audit_chain SET digest=decode(repeat('00',32),'hex') WHERE organization_id=$1 AND sequence=1`, o.ID); err != nil {
		t.Fatal(err)
	}
	checkpoint, err = service.Verify(context.Background(), o.ID)
	if err != nil || checkpoint.Valid {
		t.Fatal("chain tampering not detected")
	}
}
func TestContentPolicyMutationRollsBackWithAuditAndRejectsMissingKeys(t *testing.T) {
	f := providerSetup(t, testutil.SupplierFixture(t))
	o, p, _ := providerProject(f)
	base := projectPath(o, p) + "/content-audit"
	if _, err := f.pool.Exec(context.Background(), `ALTER TABLE audit_events ADD CONSTRAINT reject_content_policy CHECK(action<>'content_audit.policy_changed') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	f.request(f.token("alice"), "PUT", base+"/policy", map[string]any{"enabled": true, "retention_days": 1, "max_bytes": 1024, "consent_ref": "consent", "expected_revision": 0}, 503)
	v := parse[control.ContentPolicy](t, f.request(f.token("alice"), "GET", base+"/policy", nil, 200))
	if v.Enabled || v.Revision != 0 {
		t.Fatal("audit failure committed content policy")
	}
	var leak map[string]any
	data, _ := json.Marshal(v)
	if json.Unmarshal(data, &leak) != nil || leak["configured"] != true {
		t.Fatal("key material readiness missing")
	}
}

func TestContentAccessLogFailureAndCrossProjectGrant(t *testing.T) {
	f := providerSetup(t, testutil.SupplierFixture(t))
	o, p, key := providerProject(f)
	other := f.project("alice", o)
	base := projectPath(o, p) + "/content-audit"
	enableContent(f, o, p, 65536)
	grantAuditor(f, o, p, "auditor", true)
	f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("aliyun/fixture", false), 200)
	page := parse[contentPage](t, f.request(f.token("auditor"), "GET", base+"/records", nil, 200))
	id := page.Data[0].RequestID
	f.request(f.token("auditor"), "POST", projectPath(o, other)+"/content-audit/records/"+id+"/reveal", map[string]string{"reason": "cross project"}, 403)
	f.member(o, "admin", "admin")
	f.request(f.token("admin"), "PUT", base+"/auditors", map[string]any{"issuer": f.issuer.URL, "subject": "admin", "active": true}, 403)
	if _, err := f.pool.Exec(context.Background(), `ALTER TABLE content_audit_access_log ADD CONSTRAINT reject_access CHECK(false) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	f.request(f.token("auditor"), "POST", base+"/records/"+id+"/reveal", map[string]string{"reason": "must log before content"}, 503)
}

func TestContentAuditCancellationRetainsPartialAndBudget(t *testing.T) {
	canceled := make(chan struct{})
	f := providerSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"synthetic partial\"}}]}\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	}))
	o, p, key := providerProject(f)
	enableContent(f, o, p, 65536)
	grantAuditor(f, o, p, "auditor", true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, "POST", f.server.URL+"/v1/chat/completions", strings.NewReader(mustJSON(t, realPayload("aliyun/fixture", true))))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+key.Secret)
	r.Header.Set("Content-Type", "application/json")
	resp, err := f.server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	id := resp.Header.Get("X-Request-ID")
	if _, err := bufio.NewReader(resp.Body).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	cancel()
	resp.Body.Close()
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream not canceled")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var state string
		if err := f.pool.QueryRow(context.Background(), `SELECT state FROM content_audit_records WHERE request_id=$1`, id).Scan(&state); err == nil && state == "canceled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("canceled capture not sealed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	detail := parse[contentaudit.Detail](t, f.request(f.token("auditor"), "POST", projectPath(o, p)+"/content-audit/records/"+id+"/reveal", map[string]string{"reason": "verify interrupted content"}, 200))
	if detail.Response == nil || !strings.Contains(detail.Response.Content, "synthetic partial") {
		t.Fatal("partial output missing")
	}
	var state string
	if err := f.pool.QueryRow(context.Background(), `SELECT state FROM budget_reservations WHERE request_id=$1`, id).Scan(&state); err != nil || state != "uncertain" {
		t.Fatal("canceled audit refunded budget")
	}
}

func TestContentCheckpointFailureBeforeDeliveryKeepsFinancialState(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint("stream-", stream), func(t *testing.T) {
			f := providerSetup(t, testutil.SupplierFixture(t))
			o, p, key := providerProject(f)
			enableContent(f, o, p, 65536)
			if _, err := f.pool.Exec(context.Background(), `ALTER TABLE content_audit_checkpoint_parts ADD CONSTRAINT reject_first_part CHECK(sequence<>1) NOT VALID`); err != nil {
				t.Fatal(err)
			}
			data := f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("aliyun/fixture", stream), 503)
			if strings.Contains(string(data), "synthetic response") || strings.Contains(string(data), "[DONE]") {
				t.Fatal("uncheckpointed output delivered")
			}
			page := parse[contentPage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/content-audit/records", nil, 200))
			if len(page.Data) != 1 || page.Data[0].State != "incomplete" || page.Data[0].Status != 503 {
				t.Fatal("checkpoint failure claimed complete")
			}
			record := parse[control.RequestRecord](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/requests/"+page.Data[0].RequestID, nil, 200))
			if (!stream && record.State != "settled") || (stream && record.State != "uncertain") {
				t.Fatal("audit failure refunded or incorrectly settled finance")
			}
		})
	}
}
func TestContentCheckpointCommitsBeforeSSEAndMidstreamFailureSuppressesDone(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	supplier := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"durable-first\"},\"finish_reason\":null}]}\n\n"))
		_ = http.NewResponseController(w).Flush()
		<-release
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"uncommitted-second\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\ndata: [DONE]\n\n"))
	})
	f := providerSetup(t, supplier)
	o, p, key := providerProject(f)
	enableContent(f, o, p, 65536)
	body, _ := json.Marshal(realPayload("aliyun/fixture", true))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", f.server.URL+"/v1/chat/completions", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key.Secret)
	response, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	first, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(first, "durable-first") {
		t.Fatal("first frame missing")
	}
	var observed int64
	var parts int
	if err := f.pool.QueryRow(ctx, `SELECT observed_bytes,parts FROM content_audit_checkpoints WHERE request_id=$1`, response.Header.Get("X-Request-ID")).Scan(&observed, &parts); err != nil || observed < int64(len(first)) || parts != 1 {
		t.Fatal("client received bytes before checkpoint commit")
	}
	if _, err := f.pool.Exec(ctx, `ALTER TABLE content_audit_checkpoint_parts ADD CONSTRAINT reject_second_part CHECK(sequence<>2) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
	remaining, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal("stream did not close cleanly")
	}
	if response.StatusCode != 200 || strings.Contains(string(remaining), "uncommitted-second") || strings.Contains(string(remaining), "[DONE]") || !strings.Contains(string(remaining), "content_audit_unavailable") {
		t.Fatal("failed checkpoint delivered uncommitted bytes or completion")
	}
}

func TestClientToolsAuditPreservesDeclarationsArgumentsAndResults(t *testing.T) {
	f := clientToolFixture(t)
	o, p, key := providerProject(f)
	enableContent(f, o, p, 65536)
	payload := clientToolPayload()
	payload["messages"] = []map[string]any{
		{"role": "user", "content": "synthetic query"},
		{"role": "assistant", "content": nil, "tool_calls": []map[string]any{{"id": "call_old", "type": "function", "function": map[string]string{"name": "lookup", "arguments": `{"q":"prior"}`}}}},
		{"role": "tool", "tool_call_id": "call_old", "content": "synthetic result"},
	}
	response := string(f.request(key.Secret, "POST", "/v1/chat/completions", payload, 200))
	if !strings.Contains(response, "call_new") || !strings.Contains(response, "synthetic secret") {
		t.Fatal("client tool response lost")
	}
	base := projectPath(o, p) + "/content-audit"
	page := parse[contentPage](t, f.request(f.token("alice"), "GET", base+"/records", nil, 200))
	if len(page.Data) != 1 || page.Data[0].State != "complete" {
		t.Fatal("tool audit incomplete")
	}
	grantAuditor(f, o, p, "auditor", true)
	detail := parse[contentaudit.Detail](t, f.request(f.token("auditor"), "POST", base+"/records/"+page.Data[0].RequestID+"/reveal", map[string]string{"reason": "verify synthetic client-tool audit"}, 200))
	for _, text := range []string{"tools", "parameters", "call_old", "tool_call_id", "synthetic result"} {
		if !strings.Contains(detail.Request.Content, text) {
			t.Fatalf("tool request audit lost %s", text)
		}
	}
	if detail.Response == nil || !strings.Contains(detail.Response.Content, "call_new") || !strings.Contains(detail.Response.Content, "synthetic secret") {
		t.Fatal("tool response arguments absent from encrypted audit")
	}
	var plaintext bool
	if err := f.pool.QueryRow(context.Background(), `SELECT bool_or(position('synthetic'::bytea in ciphertext)>0) FROM content_audit_payloads WHERE request_id=$1`, page.Data[0].RequestID).Scan(&plaintext); err != nil || plaintext {
		t.Fatal("tool audit stored plaintext")
	}
	usage := parse[control.Usage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/usage", nil, 200))
	if usage.Spent != 16 || usage.Held != 0 {
		t.Fatal("tool billing did not use verified token evidence")
	}
}

func TestInlineImageAuditRetainsAttachmentIdentityAndBoundedContent(t *testing.T) {
	f := clientToolFixture(t)
	o, p, key := providerProject(f)
	enableContent(f, o, p, 1024)
	payload := clientToolPayload()
	payload["messages"] = []map[string]any{{"role": "user", "content": []map[string]any{
		{"type": "text", "text": strings.Repeat("synthetic ", 256)},
		{"type": "image_url", "image_url": map[string]string{"url": testutil.InlinePNG}},
	}}}
	f.request(key.Secret, "POST", "/v1/chat/completions", payload, 200)
	base := projectPath(o, p) + "/content-audit"
	page := parse[contentPage](t, f.request(f.token("alice"), "GET", base+"/records", nil, 200))
	if len(page.Data) != 1 {
		t.Fatal("image audit record missing")
	}
	grantAuditor(f, o, p, "auditor", true)
	detail := parse[contentaudit.Detail](t, f.request(f.token("auditor"), "POST", base+"/records/"+page.Data[0].RequestID+"/reveal", map[string]string{"reason": "verify synthetic image identity"}, 200))
	if !detail.Request.Truncated || !strings.Contains(detail.Request.Content, `"sha256"`) || !strings.Contains(detail.Request.Content, `"mime":"image/png"`) || !strings.Contains(detail.Request.Content, `"width":1`) {
		t.Fatal("bounded capture lost image identity or truncation indication")
	}
	// URL references must be rejected before another reservation or outbound request.
	payload["messages"] = []map[string]any{{"role": "user", "content": []map[string]any{{"type": "image_url", "image_url": map[string]string{"url": "http://127.0.0.1/private"}}}}}
	f.request(key.Secret, "POST", "/v1/chat/completions", payload, 400)
	usage := parse[control.Usage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/usage", nil, 200))
	if usage.Spent != 16 || usage.Held != 0 {
		t.Fatal("rejected image URL acquired funds")
	}
}
