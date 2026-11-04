package httpapi_test

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"

	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
)

type alertPage struct {
	Data []control.BudgetAlert `json:"data"`
	Next string                `json:"next_cursor"`
}

func TestBudgetAlertsAtomicCrossingBackfillAndAcknowledgment(t *testing.T) {
	f := readyManaged(t)
	o, p, _ := providerProject(f)
	ctx := context.Background()
	token := f.token("alice")
	base := projectPath(o, p) + "/budget/alerts"
	orgBase := orgPath(o) + "/budget/alerts"
	initial := parse[control.AlertPolicy](t, f.request(token, "GET", base+"/policy", nil, 200))
	if initial.Enabled || initial.Revision != 0 {
		t.Fatal("alerts enabled by default")
	}
	policy := func(rev int64, enabled bool, thresholds ...int) any {
		return map[string]any{"enabled": enabled, "thresholds": thresholds, "expected_revision": rev, "reason": "synthetic threshold policy"}
	}
	f.request(token, "PUT", base+"/policy", policy(0, true, 80, 100), 200)
	f.request(token, "PUT", orgBase+"/policy", policy(0, true, 50), 200)
	ledger := budget.New(f.pool)
	scope := budget.Scope{Organization: o.ID, Project: p.ID, Period: o.Period, Currency: o.Currency}
	reserve := func(id string, amount int64) {
		t.Helper()
		if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: id, PriceVersion: "synthetic", Amount: amount}); err != nil {
			t.Fatal(err)
		}
	}
	reserve("alert-short-lived", 4000)
	if err := ledger.Release(ctx, "alert-short-lived"); err != nil {
		t.Fatal(err)
	}
	alerts := parse[alertPage](t, f.request(token, "GET", base, nil, 200))
	if len(alerts.Data) != 1 || alerts.Data[0].Threshold != 80 || alerts.Data[0].Held != "4000" || alerts.Data[0].Spent != "0" || alerts.Data[0].AcknowledgedAt != nil {
		t.Fatal("short-lived crossing lost or malformed")
	}
	reserve("alert-second", 5000)
	alerts = parse[alertPage](t, f.request(token, "GET", base, nil, 200))
	if len(alerts.Data) != 2 || alerts.Data[0].Threshold != 100 {
		t.Fatal("threshold not deduplicated")
	}
	orgAlerts := parse[alertPage](t, f.request(token, "GET", orgBase, nil, 200))
	if len(orgAlerts.Data) != 1 || orgAlerts.Data[0].Held != "5000" {
		t.Fatal("organization crossing missing")
	}
	f.request(token, "PUT", base+"/policy", policy(1, false, 80, 100), 200)
	f.request(token, "PUT", base+"/policy", policy(2, true, 80, 100), 200)
	if len(parse[alertPage](t, f.request(token, "GET", base, nil, 200)).Data) != 2 {
		t.Fatal("reenable created duplicates")
	}
	f.request(token, "PUT", base+"/policy", policy(0, true, 80), 409)
	for _, v := range []any{policy(3, true, 80, 80), policy(3, true, 81), policy(3, true, 100, 80), map[string]any{"enabled": true, "thresholds": []int{80}, "expected_revision": 3}} {
		f.request(token, "PUT", base+"/policy", v, 400)
	}
	f.member(o, "viewer", "viewer")
	f.request(f.token("viewer"), "GET", base, nil, 200)
	f.request(f.token("viewer"), "GET", orgBase, nil, 403)
	f.request(f.token("outsider"), "GET", base, nil, 404)
	path := base + "/" + strconv.FormatInt(alerts.Data[0].ID, 10) + "/acknowledge"
	f.request(f.token("viewer"), "POST", path, map[string]string{"reason": "checked"}, 403)
	f.request(token, "POST", path, map[string]string{"reason": "checked usage"}, 204)
	f.request(token, "POST", path, map[string]string{"reason": "overwrite"}, 409)
	ack := parse[alertPage](t, f.request(token, "GET", base, nil, 200)).Data[0]
	if ack.Actor == nil || ack.Actor.Subject != "alice" || ack.Reason == nil || *ack.Reason != "checked usage" {
		t.Fatal("acknowledgment missing")
	}
	reservation, err := ledger.Get(ctx, "alert-second")
	if err != nil || reservation.State != "reserved" {
		t.Fatal("acknowledgment changed budget")
	}
	f.request(token, "POST", orgBase+"/"+strconv.FormatInt(ack.ID, 10)+"/acknowledge", map[string]string{"reason": "wrong scope"}, 404)
}
func TestBudgetAlertFailureRollsBackLedgerPolicyAndAcknowledgment(t *testing.T) {
	f := readyManaged(t)
	o, p, _ := providerProject(f)
	ctx := context.Background()
	token := f.token("alice")
	base := projectPath(o, p) + "/budget/alerts"
	set := func(rev int64, enabled bool, want int) {
		t.Helper()
		f.request(token, "PUT", base+"/policy", map[string]any{"enabled": enabled, "thresholds": []int{80}, "expected_revision": rev, "reason": "checked policy"}, want)
	}
	ledger := budget.New(f.pool)
	quote := budget.Quote{Scope: budget.Scope{Organization: o.ID, Project: p.ID, Period: o.Period, Currency: o.Currency}, RequestID: "alert-rollback", PriceVersion: "synthetic", Amount: 4000}
	set(0, true, 200)
	if _, err := f.pool.Exec(ctx, `ALTER TABLE budget_alerts ADD CONSTRAINT reject_alert CHECK(threshold<>80) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Reserve(ctx, quote); err == nil {
		t.Fatal("failed alert allowed ledger commit")
	}
	account := parse[control.BudgetAccount](t, f.request(token, "GET", projectPath(o, p)+"/budget", nil, 200))
	if account.Held != 0 {
		t.Fatal("alert failure leaked hold")
	}
	if _, err := f.pool.Exec(ctx, `ALTER TABLE budget_alerts DROP CONSTRAINT reject_alert`); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Reserve(ctx, quote); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `ALTER TABLE audit_events ADD CONSTRAINT reject_alert_audit CHECK(action NOT IN ('budget_alert.policy_changed','budget_alert.acknowledged')) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	set(1, false, 503)
	after := parse[control.AlertPolicy](t, f.request(token, "GET", base+"/policy", nil, 200))
	if !after.Enabled || after.Revision != 1 {
		t.Fatal("policy audit failure committed")
	}
	a := parse[alertPage](t, f.request(token, "GET", base, nil, 200)).Data[0]
	f.request(token, "POST", base+"/"+strconv.FormatInt(a.ID, 10)+"/acknowledge", map[string]string{"reason": "checked"}, 503)
	if parse[alertPage](t, f.request(token, "GET", base, nil, 200)).Data[0].AcknowledgedAt != nil {
		t.Fatal("ack audit failure committed")
	}
}
func TestBudgetAlertsConcurrentReservationsPeriodAndLimitChanges(t *testing.T) {
	f := readyManaged(t)
	o, p, _ := providerProject(f)
	ctx := context.Background()
	token := f.token("alice")
	base := projectPath(o, p) + "/budget/alerts"
	ledger := budget.New(f.pool)
	scope := budget.Scope{Organization: o.ID, Project: p.ID, Period: o.Period, Currency: o.Currency}
	if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: "alert-backfill", PriceVersion: "synthetic", Amount: 3000}); err != nil {
		t.Fatal(err)
	}
	set := map[string]any{"enabled": true, "thresholds": []int{50, 80, 100}, "expected_revision": 0, "reason": "backfill existing balances"}
	f.request(token, "PUT", base+"/policy", set, 200)
	if len(parse[alertPage](t, f.request(token, "GET", base, nil, 200)).Data) != 1 {
		t.Fatal("enable missed current balances")
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: fmt.Sprint("alert-concurrent-", i), PriceVersion: "synthetic", Amount: 100}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if len(parse[alertPage](t, f.request(token, "GET", base, nil, 200)).Data) != 2 {
		t.Fatal("concurrent crossing duplicated")
	}
	// Lowering an unused margin can cross a new threshold without a new request.
	f.request(token, "PUT", projectPath(o, p)+"/budget", map[string]any{"period": o.Period, "expected_revision": 0, "limit": 4000, "reason": "reduce unused margin"}, 200)
	if len(parse[alertPage](t, f.request(token, "GET", base, nil, 200)).Data) != 3 {
		t.Fatal("budget limit change missed crossing")
	}
	f.request(token, "POST", orgPath(o)+"/budget/period", map[string]string{"expected_period": o.Period, "next_period": "2026-12"}, 204)
	scope.Period = "2026-12"
	if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: "alert-new-period", PriceVersion: "synthetic", Amount: 4000}); err != nil {
		t.Fatal(err)
	}
	all := parse[alertPage](t, f.request(token, "GET", base, nil, 200))
	if len(all.Data) != 6 || all.Data[0].Period != "2026-12" {
		t.Fatal("new period did not rearm")
	}
}

func TestBudgetAlertEnableRacesReservationWithoutMissingCrossing(t *testing.T) {
	f := readyManaged(t)
	o, p, _ := providerProject(f)
	ctx := context.Background()
	ledger := budget.New(f.pool)
	scope := budget.Scope{Organization: o.ID, Project: p.ID, Period: o.Period, Currency: o.Currency}
	actor := identity.Principal{Issuer: f.issuer.URL, Subject: "alice"}
	start := make(chan struct{})
	errs := make(chan error, 2)
	go func() {
		<-start
		errs <- f.store.SetAlertPolicy(ctx, actor, o.ID, p.ID, control.AlertPolicy{Enabled: true, Thresholds: []int{80}, Revision: 0}, "concurrent enable")
	}()
	go func() {
		<-start
		_, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: "race-enable", PriceVersion: "synthetic", Amount: 4000})
		errs <- err
	}()
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if len(parse[alertPage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/budget/alerts", nil, 200)).Data) != 1 {
		t.Fatal("configuration race lost crossing")
	}
}
func TestBudgetAlertPaginationAndZeroLimit(t *testing.T) {
	f := readyManaged(t)
	o, p, _ := providerProject(f)
	ctx := context.Background()
	token := f.token("alice")
	base := projectPath(o, p) + "/budget/alerts"
	f.request(token, "PUT", projectPath(o, p)+"/budget", map[string]any{"period": o.Period, "expected_revision": 0, "limit": 0, "reason": "pause project quota"}, 200)
	f.request(token, "PUT", base+"/policy", map[string]any{"enabled": true, "thresholds": []int{80}, "expected_revision": 0, "reason": "zero quota"}, 200)
	if len(parse[alertPage](t, f.request(token, "GET", base, nil, 200)).Data) != 0 {
		t.Fatal("zero limit generated percentage alert")
	}
	// Seed synthetic historical cycles; financial data and alerts are fixture-only.
	if _, err := f.pool.Exec(ctx, `INSERT INTO budget_accounts(organization_id,project_id,period_id,currency,limit_amount,spent) SELECT $1,$2,'historical-'||n,'CNY_MICRO',100,80 FROM generate_series(1,53) n`, o.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO budget_alerts(organization_id,project_id,period_id,currency,threshold,policy_revision,limit_amount,spent,held) SELECT $1,$2,'historical-'||n,'CNY_MICRO',80,1,100,80,0 FROM generate_series(1,53) n`, o.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	first := parse[alertPage](t, f.request(token, "GET", base, nil, 200))
	if len(first.Data) != 50 || first.Next == "" {
		t.Fatal("page unbounded")
	}
	second := parse[alertPage](t, f.request(token, "GET", base+"?after="+first.Next, nil, 200))
	if len(second.Data) != 3 || second.Next != "" || first.Data[49].ID <= second.Data[0].ID {
		t.Fatal("descending cursor skipped or repeated")
	}
}
