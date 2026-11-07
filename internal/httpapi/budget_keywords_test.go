package httpapi_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/keyword"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func readyManaged(t *testing.T) fixture {
	f, _ := managedSetup(t)
	f.request(f.token("alice"), "PUT", "/api/v1/providers", map[string]any{"expected_revision": 0, "configuration": testutil.MainlandConfig()}, 200)
	return f
}
func TestBudgetAdjustmentPeriodAndLateSettlement(t *testing.T) {
	f := readyManaged(t)
	o, p, _ := providerProject(f)
	ctx := context.Background()
	admin := f.token("alice")
	base := projectPath(o, p) + "/budget"
	account := parse[control.BudgetAccount](t, f.request(admin, "GET", base, nil, 200))
	if account.Revision != 0 {
		t.Fatal("unexpected initial revision")
	}
	ledger := budget.New(f.pool)
	scope := budget.Scope{Organization: o.ID, Project: p.ID, Period: o.Period, Currency: o.Currency}
	if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: "prior-period", PriceVersion: "synthetic", Amount: 500}); err != nil {
		t.Fatal(err)
	}
	update := func(rev, limit int64) any {
		return map[string]any{"period": o.Period, "expected_revision": rev, "limit": limit, "reason": "controlled budget adjustment"}
	}
	f.request(f.token("bob"), "PUT", base, update(0, 600), 404)
	f.request(admin, "PUT", base, update(0, 499), 409)
	if _, err := f.pool.Exec(ctx, `INSERT INTO budget_accounts(organization_id,project_id,period_id,currency,limit_amount) VALUES($1,$2,$3,'MOCK',77)`, o.ID, p.ID, o.Period); err != nil {
		t.Fatal(err)
	}
	f.request(admin, "PUT", base, update(0, 600), 200)
	var otherLimit int64
	if err := f.pool.QueryRow(ctx, `SELECT limit_amount FROM budget_accounts WHERE organization_id=$1 AND project_id=$2 AND period_id=$3 AND currency='MOCK'`, o.ID, p.ID, o.Period).Scan(&otherLimit); err != nil || otherLimit != 77 {
		t.Fatal("budget adjustment crossed currency scope")
	}
	f.request(admin, "PUT", base, update(0, 700), 409)
	history := string(f.request(admin, "GET", base+"/changes?period="+o.Period, nil, 200))
	if !strings.Contains(history, "controlled budget adjustment") {
		t.Fatal("budget change history missing")
	}
	if _, err := f.pool.Exec(ctx, `ALTER TABLE audit_events ADD CONSTRAINT reject_budget CHECK(action<>'budget.limit_changed') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	f.request(admin, "PUT", base, update(1, 700), 503)
	after := parse[control.BudgetAccount](t, f.request(admin, "GET", base, nil, 200))
	if after.Limit != 600 || after.Revision != 1 {
		t.Fatal("failed audit changed budget")
	}
	f.request(admin, "POST", orgPath(o)+"/budget/period", map[string]string{"expected_period": o.Period, "next_period": "2026-12"}, 204)
	after = parse[control.BudgetAccount](t, f.request(admin, "GET", base, nil, 200))
	if after.Period != "2026-12" || after.Held != 0 || after.Spent != 0 || after.Limit != 600 {
		t.Fatal("new period copied spending or lost limit")
	}
	if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: "stale-period", PriceVersion: "synthetic", Amount: 1}); !errors.Is(err, budget.ErrConflict) {
		t.Fatal("stale authorization reserved an old period")
	}
	if err := ledger.Dispatch(ctx, "prior-period"); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Settle(ctx, "prior-period", 120); err != nil {
		t.Fatal(err)
	}
	var oldSpent, oldHeld int64
	if err := f.pool.QueryRow(ctx, `SELECT spent,held FROM budget_accounts WHERE organization_id=$1 AND project_id=$2 AND period_id=$3`, o.ID, p.ID, o.Period).Scan(&oldSpent, &oldHeld); err != nil || oldSpent != 120 || oldHeld != 0 {
		t.Fatal("late settlement did not retain original period")
	}
	after = parse[control.BudgetAccount](t, f.request(admin, "GET", base, nil, 200))
	if after.Spent != 0 {
		t.Fatal("late settlement charged new period")
	}
	f.request(admin, "POST", orgPath(o)+"/budget/period", map[string]string{"expected_period": o.Period, "next_period": "2027-01"}, 409)
}
func TestBudgetMonthlyRotationIsAtomicAndIdempotent(t *testing.T) {
	f := readyManaged(t)
	ctx := context.Background()
	p := identity.Principal{Issuer: f.issuer.URL, Subject: "alice"}
	org, err := f.store.CreateOrganizationWithCurrency(ctx, p, "Old cycle", "2000-01", "CNY_MICRO", 10000)
	if err != nil {
		t.Fatal(err)
	}
	project, err := f.store.CreateProject(ctx, p, org.ID, "Old project", 5000)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetBudgetCycle(ctx, p, org.ID, 0, true); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	out := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); out <- f.store.EnsureBudgetPeriod(ctx, org.ID) }()
	}
	wg.Wait()
	close(out)
	for err := range out {
		if err != nil {
			t.Fatal(err)
		}
	}
	account, err := f.store.Budget(ctx, p, org.ID, project.ID)
	if err != nil || account.Period != time.Now().UTC().Format("2006-01") || account.Limit != 5000 {
		t.Fatal("monthly budget did not rotate")
	}
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM budget_period_changes WHERE organization_id=$1`, org.ID).Scan(&n); err != nil || n != 1 {
		t.Fatal("rotation duplicated events")
	}
}
func TestKeywordInputBlocksBeforeBudgetAndAuditFailureRollsBack(t *testing.T) {
	f := readyManaged(t)
	o, p, key := providerProject(f)
	admin := f.token("alice")
	path := projectPath(o, p) + "/keywords"
	rules := []keyword.Rule{{ID: "deny", Kind: "literal", Pattern: "synthetic", Direction: "input", Action: "block"}}
	f.request(admin, "PUT", path, map[string]any{"expected_revision": 0, "rules": rules}, 200)
	f.request(f.token("bob"), "GET", path, nil, 404)
	f.request(admin, "PUT", path, map[string]any{"expected_revision": 0, "rules": rules}, 409)
	response := string(f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("aliyun/fixture", false), 403))
	if strings.Contains(response, "synthetic mainland") {
		t.Fatal("blocked content echoed")
	}
	usage := parse[control.Usage](t, f.request(admin, "GET", projectPath(o, p)+"/usage", nil, 200))
	if usage.Spent != 0 || usage.Held != 0 {
		t.Fatal("blocked input acquired budget")
	}
	matches := string(f.request(admin, "GET", path+"/matches", nil, 200))
	if !strings.Contains(matches, "deny") || strings.Contains(matches, "synthetic mainland") {
		t.Fatal("keyword event missing or copied text")
	}
	if _, err := f.pool.Exec(context.Background(), `ALTER TABLE audit_events ADD CONSTRAINT reject_keyword CHECK(action<>'keyword.policy_changed') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	f.request(admin, "PUT", path, map[string]any{"expected_revision": 1, "rules": []keyword.Rule{}}, 503)
	policy := parse[keyword.Policy](t, f.request(admin, "GET", path, nil, 200))
	if policy.Revision != 1 || len(policy.Rules) != 1 {
		t.Fatal("failed audit changed rule policy")
	}
}
func TestKeywordOutputRedactionPreservesBillingAndRefusesUnsafeStreaming(t *testing.T) {
	f := readyManaged(t)
	o, p, key := providerProject(f)
	path := projectPath(o, p) + "/keywords"
	admin := f.token("alice")
	// The synthetic supplier returns content containing this text.
	rules := []keyword.Rule{{ID: "hide", Kind: "regex", Pattern: ".", Direction: "output", Action: "redact"}}
	f.request(admin, "PUT", path, map[string]any{"expected_revision": 0, "rules": rules}, 200)
	body := string(f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("aliyun/fixture", false), 200))
	if !strings.Contains(body, "[REDACTED]") {
		t.Fatal("JSON response not redacted")
	}
	f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("aliyun/fixture", true), 400)
	usage := parse[control.Usage](t, f.request(admin, "GET", projectPath(o, p)+"/usage", nil, 200))
	if usage.Spent == 0 || usage.Held != 0 {
		t.Fatal("redaction changed supplier accounting")
	}
}
func TestKeywordStreamCrossBoundaryIsBoundedAndMetadataOnly(t *testing.T) {
	f := readyManaged(t)
	o, p, _ := providerProject(f)
	admin := identity.Principal{Issuer: f.issuer.URL, Subject: "alice"}
	ctx := context.Background()
	if err := f.store.SetKeywordPolicy(ctx, admin, o.ID, p.ID, 0, []keyword.Rule{{ID: "watch", Kind: "literal", Pattern: "敏感词", Direction: "output", Action: "alert"}}); err != nil {
		t.Fatal(err)
	}
	session, err := keyword.New(f.pool).Begin(ctx, o.ID, p.ID, "cross-chunk", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, chunk := range []string{"其他敏", "感", "词正文", "敏感词"} {
		if err := session.Stream(ctx, "content", chunk); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM keyword_matches WHERE request_id='cross-chunk'`).Scan(&n); err != nil || n != 1 {
		t.Fatal("cross-chunk detection lost or duplicated event")
	}
}

func TestKeywordFailClosedAndOverlappingRedaction(t *testing.T) {
	f := readyManaged(t)
	o, p, key := providerProject(f)
	ctx := context.Background()
	admin := identity.Principal{Issuer: f.issuer.URL, Subject: "alice"}
	rules := []keyword.Rule{{ID: "first", Kind: "literal", Pattern: "ab", Direction: "output", Action: "redact"}, {ID: "second", Kind: "literal", Pattern: "bcd", Direction: "output", Action: "redact"}}
	if err := f.store.SetKeywordPolicy(ctx, admin, o.ID, p.ID, 0, rules); err != nil {
		t.Fatal(err)
	}
	session, err := keyword.New(f.pool).Begin(ctx, o.ID, p.ID, "overlap", false)
	if err != nil {
		t.Fatal(err)
	}
	if value, err := session.Process(ctx, "output", "abcde"); err != nil || value != "[REDACTED]e" {
		t.Fatal("overlapping rules leaked suffix", err)
	}
	// Force hit journaling to fail before any response text may be forwarded.
	if err := f.store.SetKeywordPolicy(ctx, admin, o.ID, p.ID, 1, []keyword.Rule{{ID: "output-alert", Kind: "literal", Pattern: "synthetic", Direction: "output", Action: "alert"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `ALTER TABLE keyword_matches ADD CONSTRAINT reject_new_matches CHECK(request_id='overlap') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	response := string(f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("aliyun/fixture", true), 503))
	if strings.Contains(response, "[DONE]") || strings.Contains(response, "synthetic") {
		t.Fatal("stream disclosed content after failed keyword audit")
	}
}

func TestClientToolKeywordChecksDecodedArgumentsBeforeDispatch(t *testing.T) {
	f := clientToolFixture(t)
	o, p, key := providerProject(f)
	path := projectPath(o, p) + "/keywords"
	f.request(f.token("alice"), "PUT", path, map[string]any{"expected_revision": 0, "rules": []keyword.Rule{{ID: "deny-tool", Kind: "literal", Pattern: "secret", Direction: "input", Action: "block"}}}, 200)
	payload := clientToolPayload()
	payload["messages"] = []map[string]any{
		{"role": "assistant", "tool_calls": []map[string]any{{"id": "call_old", "type": "function", "function": map[string]string{"name": "lookup", "arguments": `{"q":"\u0073ecret"}`}}}},
		{"role": "tool", "tool_call_id": "call_old", "content": "result"},
	}
	f.request(key.Secret, "POST", "/v1/chat/completions", payload, 403)
	usage := parse[control.Usage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/usage", nil, 200))
	if usage.Spent != 0 || usage.Held != 0 {
		t.Fatal("blocked tool arguments acquired funds")
	}
	f.request(f.token("alice"), "PUT", path, map[string]any{"expected_revision": 1, "rules": []keyword.Rule{{ID: "redact-tool", Kind: "literal", Pattern: "secret", Direction: "output", Action: "redact"}}}, 200)
	response := string(f.request(key.Secret, "POST", "/v1/chat/completions", clientToolPayload(), 200))
	if strings.Contains(response, "synthetic secret") || !strings.Contains(response, "REDACTED") || !strings.Contains(response, "lookup") {
		t.Fatal("structured argument redaction failed")
	}
	usage = parse[control.Usage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/usage", nil, 200))
	if usage.Spent != 16 || usage.Held != 0 {
		t.Fatal("redaction changed supplier charge")
	}
}
