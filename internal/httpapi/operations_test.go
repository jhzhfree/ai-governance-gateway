package httpapi_test

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/jhzhfree/ai-governance-gateway/internal/admission"
	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/execution"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
)

func TestLimitsPermissionsRevisionAndAudit(t *testing.T) {
	f := setup(t)
	o := f.org("alice")
	p := f.project("alice", o)
	path := projectPath(o, p) + "/limits"
	initial := parse[admission.Snapshot](t, f.request(f.token("alice"), "GET", path, nil, 200))
	if initial.RequestsPerMinute != 60 || initial.MaxConcurrent != 4 || initial.Revision != 0 {
		t.Fatal("incorrect default limits")
	}
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM project_limits`).Scan(&count); err != nil || count != 0 {
		t.Fatal("read request created policy rows")
	}
	f.member(o, "viewer", "viewer")
	f.request(f.token("viewer"), "GET", path, nil, 200)
	body := map[string]any{"requests_per_minute": 10, "max_concurrent": 2, "expected_revision": 0}
	f.request(f.token("viewer"), "PUT", path, body, 403)
	f.request(f.token("outsider"), "GET", path, nil, 404)
	key := f.key("alice", o, p)
	f.request(key.Secret, "GET", path, nil, 401)
	updated := parse[admission.Snapshot](t, f.request(f.token("alice"), "PUT", path, body, 200))
	if updated.Revision != 1 || updated.RequestsPerMinute != 10 {
		t.Fatal("limits not updated")
	}
	f.request(f.token("alice"), "PUT", path, body, 409)
	for _, invalid := range []map[string]any{{"requests_per_minute": 0, "max_concurrent": 2, "expected_revision": 1}, {"requests_per_minute": 10, "max_concurrent": 65, "expected_revision": 1}, {"requests_per_minute": 10, "max_concurrent": 2}} {
		f.request(f.token("alice"), "PUT", path, invalid, 400)
	}
	if _, err := f.pool.Exec(context.Background(), `ALTER TABLE audit_events ADD CONSTRAINT reject_limits CHECK(action<>'project.limits_changed') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	f.request(f.token("alice"), "PUT", path, map[string]any{"requests_per_minute": 20, "max_concurrent": 3, "expected_revision": 1}, 503)
	after := parse[admission.Snapshot](t, f.request(f.token("alice"), "GET", path, nil, 200))
	if after.Revision != 1 || after.RequestsPerMinute != 10 {
		t.Fatal("audit failure committed limit changes")
	}
}
func TestInspectionFindsHoldsUnfinishedAndAccountDriftWithoutMutations(t *testing.T) {
	f := setup(t)
	o := f.org("alice")
	p := f.project("alice", o)
	ctx := context.Background()
	ledger := budget.New(f.pool)
	recorder := execution.New(f.pool)
	scope := budget.Scope{Organization: o.ID, Project: p.ID, Period: o.Period, Currency: o.Currency}
	if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: "inspection-hold", PriceVersion: "mock-v1", Amount: 10}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Begin(ctx, execution.Start{AttemptID: "inspection-attempt", RequestID: "inspection-hold", Model: "mock-text", Provider: "mock", Region: "local", Stream: true}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Dispatch(ctx, "inspection-hold"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE budget_reservations SET updated_at=clock_timestamp()-interval '10 minutes'; UPDATE inference_attempts SET started_at=clock_timestamp()-interval '10 minutes'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE budget_accounts SET held=11 WHERE organization_id=$1 AND project_id=$2`, o.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	path := projectPath(o, p) + "/inspection"
	report := parse[control.Inspection](t, f.request(f.token("alice"), "GET", path, nil, 200))
	if len(report.Data) != 1 || len(report.Data[0].Reasons) != 2 || report.Data[0].LeaseActive || report.Data[0].Request.Attempt == nil || report.Data[0].Request.Attempt.Outcome != "unfinished" {
		t.Fatal("inspection failed to distinguish unresolved execution/hold")
	}
	if len(report.Accounts) != 2 {
		t.Fatal("owner cannot inspect both account levels")
	}
	found := false
	for _, a := range report.Accounts {
		if a.Scope == "project" {
			found = true
			if a.Consistent || a.StoredHeld != "11" || a.ComputedHeld != "10" {
				t.Fatal("account drift not detected exactly")
			}
		}
	}
	if !found {
		t.Fatal("project account absent")
	}
	f.member(o, "viewer", "viewer")
	visible := parse[control.Inspection](t, f.request(f.token("viewer"), "GET", path, nil, 200))
	if len(visible.Accounts) != 1 || visible.Accounts[0].Scope != "project" {
		t.Fatal("project read leaked organization aggregate")
	}
	other := f.project("alice", o)
	empty := parse[control.Inspection](t, f.request(f.token("alice"), "GET", projectPath(o, other)+"/inspection", nil, 200))
	if len(empty.Data) != 0 {
		t.Fatal("inspection leaked another project's requests")
	}
	f.request(f.token("outsider"), "GET", path, nil, 404)
	for _, query := range []string{"?older_than_seconds=0", "?older_than_seconds=2592001", "?after=bad", "?limit=101"} {
		f.request(f.token("alice"), "GET", path+query, nil, 400)
	}
	usage, err := f.store.Usage(ctx, identity.Principal{Issuer: f.issuer.URL, Subject: "alice"}, o.ID, p.ID)
	if err != nil || usage.Held != 11 {
		t.Fatal("inspection silently repaired or refunded a balance")
	}
	r, err := ledger.Get(ctx, "inspection-hold")
	if err != nil || r.State != "dispatched" || r.Actual != nil {
		t.Fatal("inspection mutated execution accounting")
	}
}

func TestRateDenialPrecedesBudgetAndExecution(t *testing.T) {
	f := setup(t)
	o := f.org("alice")
	p := f.project("alice", o)
	f.request(f.token("alice"), "PUT", projectPath(o, p)+"/limits", map[string]any{"requests_per_minute": 1, "max_concurrent": 4, "expected_revision": 0}, 200)
	key := f.key("alice", o, p)
	body := map[string]any{"model": "mock-text", "stream": true, "max_tokens": 8, "messages": []map[string]string{{"role": "user", "content": "test"}}}
	f.request(key.Secret, "POST", "/v1/chat/completions", body, 200)
	before := parse[struct {
		Data []control.RequestRecord `json:"data"`
	}](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/requests", nil, 200))
	req, err := http.NewRequest("POST", f.server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"mock-text","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"test"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key.Secret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 429 {
		t.Fatal("rate denial missing")
	}
	retry, err := strconv.Atoi(resp.Header.Get("Retry-After"))
	if err != nil || retry < 1 || retry > 60 {
		t.Fatal("retry delay invalid")
	}
	after := parse[struct {
		Data []control.RequestRecord `json:"data"`
	}](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/requests", nil, 200))
	if len(before.Data) != 1 || len(after.Data) != 1 || after.Data[0].RequestID != before.Data[0].RequestID {
		t.Fatal("denied admission created budget or execution record")
	}
}
