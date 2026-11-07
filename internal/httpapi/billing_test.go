package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/jhzhfree/ai-governance-gateway/internal/admission"
	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/execution"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

const csvHeader = "record_id,request_id,period,currency,amount,input_tokens,output_tokens\n"

type billPage struct {
	Data []control.BillingRecord `json:"data"`
	Next string                  `json:"next_cursor"`
}

func heldForBill(t *testing.T, f fixture, o control.Organization, p control.Project, id string, amount int64) {
	t.Helper()
	ctx := context.Background()
	ledger := budget.New(f.pool)
	if _, err := ledger.Reserve(ctx, budget.Quote{Scope: budget.Scope{Organization: o.ID, Project: p.ID, Period: o.Period, Currency: o.Currency}, RequestID: id, PriceVersion: "synthetic-price", Amount: amount}); err != nil {
		t.Fatal(err)
	}
	if err := execution.New(f.pool).Begin(ctx, execution.Start{AttemptID: "attempt-" + id, RequestID: id, Model: "aliyun/fixture", Provider: "aliyun", Region: "cn-beijing"}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Dispatch(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := ledger.MarkUncertain(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE budget_reservations SET updated_at=clock_timestamp()-interval '10 minutes' WHERE request_id=$1`, id); err != nil {
		t.Fatal(err)
	}
}
func billImport(row string) control.ImportInput {
	return control.ImportInput{Provider: "aliyun", AccountRef: "account-1", EvidenceRef: "final-invoice-1", CSV: csvHeader + row + "\n"}
}
func approveBill(id string) control.ReviewInput {
	return control.ReviewInput{Action: "approve", RequestID: id, ExpectedRevision: 0, ConfirmedFinal: true, Reason: "人工核对最终账单及请求归属"}
}
func TestBillImportTwoPersonAtomicSettlementAndIdempotence(t *testing.T) {
	f := providerSetup(t, testutil.SupplierFixture(t))
	o, p, _ := providerProject(f)
	base := projectPath(o, p) + "/billing"
	heldForBill(t, f, o, p, "bill-hold", 100)
	f.member(o, "reviewer", "admin")
	in := billImport("invoice-1,bill-hold,2026-11,CNY_MICRO,40,3,2")
	receipt := parse[control.ImportReceipt](t, f.request(f.token("alice"), "POST", base+"/imports", in, 200))
	repeat := parse[control.ImportReceipt](t, f.request(f.token("reviewer"), "POST", base+"/imports", in, 200))
	if !repeat.Duplicate || repeat.ID != receipt.ID {
		t.Fatal("duplicate import changed evidence")
	}
	before, err := budget.New(f.pool).Get(context.Background(), "bill-hold")
	if err != nil || before.State != "uncertain" || before.Actual != nil {
		t.Fatal("import mutated budget")
	}
	records := parse[billPage](t, f.request(f.token("alice"), "GET", base+"/records", nil, 200))
	if len(records.Data) != 1 || records.Data[0].Amount != "40" || records.Data[0].Issues[0] != "unsettled" {
		t.Fatal("import or matching failed")
	}
	path := base + "/records/" + records.Data[0].ID + "/review"
	body := approveBill("bill-hold")
	f.request(f.token("alice"), "POST", path, body, 403)
	f.request(f.token("reviewer"), "POST", path, body, 200)
	f.request(f.token("reviewer"), "POST", path, body, 200)
	record, err := budget.New(f.pool).Get(context.Background(), "bill-hold")
	if err != nil || record.State != "settled" || record.Actual == nil || *record.Actual != 40 {
		t.Fatal("review did not settle once")
	}
	var held, spent int64
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT held,spent FROM budget_accounts WHERE organization_id=$1 AND project_id=$2`, o.ID, p.ID).Scan(&held, &spent); err != nil || held != 0 || spent != 40 {
		t.Fatal("account settlement inconsistent")
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action='billing.approved'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("review audit not idempotent")
	}
	f.member(o, "viewer", "viewer")
	f.request(f.token("viewer"), "GET", base+"/records", nil, 200)
	f.request(f.token("viewer"), "POST", base+"/imports", in, 403)
	f.request(f.token("outsider"), "GET", base+"/records", nil, 404)
	changed := in
	changed.CSV = csvHeader + "invoice-1,bill-hold,2026-11,CNY_MICRO,41,3,2\n"
	f.request(f.token("alice"), "POST", base+"/imports", changed, 409)
}
func TestBillReviewRollbackZeroCostAndConcurrentApproval(t *testing.T) {
	f := providerSetup(t, testutil.SupplierFixture(t))
	o, p, _ := providerProject(f)
	base := projectPath(o, p) + "/billing"
	heldForBill(t, f, o, p, "zero-hold", 100)
	f.member(o, "reviewer", "admin")
	f.request(f.token("alice"), "POST", base+"/imports", billImport("zero-invoice,zero-hold,2026-11,CNY_MICRO,0,0,0"), 200)
	records := parse[billPage](t, f.request(f.token("alice"), "GET", base+"/records", nil, 200))
	id := records.Data[0].ID
	path := base + "/records/" + id + "/review"
	body := approveBill("zero-hold")
	if _, err := f.pool.Exec(context.Background(), `ALTER TABLE audit_events ADD CONSTRAINT reject_bill CHECK(action<>'billing.approved') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	f.request(f.token("reviewer"), "POST", path, body, 503)
	r, err := budget.New(f.pool).Get(context.Background(), "zero-hold")
	if err != nil || r.State != "uncertain" || r.Actual != nil {
		t.Fatal("failed audit committed financial mutation")
	}
	if _, err := f.pool.Exec(context.Background(), `ALTER TABLE audit_events DROP CONSTRAINT reject_bill`); err != nil {
		t.Fatal(err)
	}
	principal := identity.Principal{Issuer: f.issuer.URL, Subject: "reviewer"}
	errs := make(chan error, 12)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.store.ReviewBill(context.Background(), principal, o.ID, p.ID, id, body)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	r, err = budget.New(f.pool).Get(context.Background(), "zero-hold")
	if err != nil || r.Actual == nil || *r.Actual != 0 || r.State != "settled" {
		t.Fatal("confirmed zero bill did not clear hold")
	}
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM budget_outbox WHERE request_id='zero-hold' AND event_type='settled'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("concurrent review duplicated posting")
	}
}
func TestBillReviewBlocksUnsafeAndCrossProjectMappings(t *testing.T) {
	f := providerSetup(t, testutil.SupplierFixture(t))
	o, p, key := providerProject(f)
	other := f.project("alice", o)
	base := projectPath(o, p) + "/billing"
	f.member(o, "reviewer", "admin")
	heldForBill(t, f, o, p, "guarded-hold", 100)
	heldForBill(t, f, o, other, "foreign-hold", 20)
	for i, kind := range []string{"over_bound", "wrong_period", "foreign_project", "active", "recent", "provider_mismatch"} {
		t.Run(kind, func(t *testing.T) {
			local := f
			local.t = t
			requestID := "guarded-hold"
			period := "2026-11"
			amount := 40
			provider := "aliyun"
			if kind == "over_bound" {
				amount = 101
			}
			if kind == "wrong_period" {
				period = "2026-10"
			}
			if kind == "foreign_project" {
				requestID = "foreign-hold"
			}
			if kind == "provider_mismatch" {
				provider = "zhipu"
			}
			in := billImport(fmt.Sprintf("guard-%d,%s,%s,CNY_MICRO,%d,0,0", i, requestID, period, amount))
			in.Provider = provider
			local.request(local.token("alice"), "POST", base+"/imports", in, 200)
			records := parse[billPage](t, local.request(local.token("alice"), "GET", base+"/records?limit=1", nil, 200))
			path := base + "/records/" + records.Data[0].ID + "/review"
			var permit admission.Permit
			if kind == "active" {
				var err error
				permit, err = admission.New(f.pool).Acquire(context.Background(), o.ID, p.ID, "guarded-hold")
				if err != nil {
					t.Fatal(err)
				}
			}
			if kind == "recent" {
				if _, err := f.pool.Exec(context.Background(), `UPDATE budget_reservations SET updated_at=clock_timestamp() WHERE request_id='guarded-hold'`); err != nil {
					t.Fatal(err)
				}
			}
			local.request(local.token("reviewer"), "POST", path, approveBill(requestID), 409)
			local.request(local.token("alice"), "POST", path, control.ReviewInput{Action: "reject", Reason: "排除测试候选", ExpectedRevision: 0}, 200)
			if permit != nil {
				if err := permit.Release(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "recent" {
				if _, err := f.pool.Exec(context.Background(), `UPDATE budget_reservations SET updated_at=clock_timestamp()-interval '10 minutes' WHERE request_id='guarded-hold'`); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	f.request(key.Secret, "GET", base+"/records", nil, 401)
	f.request(f.token("alice"), "GET", base+"/records?decision=invalid", nil, 400)
}
func TestBillRejectPaginationAndImportRollback(t *testing.T) {
	f := providerSetup(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("billing contacted supplier") }))
	o, p, _ := providerProject(f)
	base := projectPath(o, p) + "/billing"
	in := billImport("rejected-1,,2026-11,CNY_MICRO,10,0,0")
	in.CSV += "rejected-2,,2026-11,CNY_MICRO,20,0,0\n"
	f.request(f.token("alice"), "POST", base+"/imports", in, 200)
	page := parse[billPage](t, f.request(f.token("alice"), "GET", base+"/records?limit=1", nil, 200))
	if page.Next == "" {
		t.Fatal("billing pagination absent")
	}
	next := parse[billPage](t, f.request(f.token("alice"), "GET", base+"/records?limit=1&after="+page.Next, nil, 200))
	if len(next.Data) != 1 || next.Data[0].ID == page.Data[0].ID {
		t.Fatal("billing pagination repeated record")
	}
	path := base + "/records/" + page.Data[0].ID + "/review"
	body := control.ReviewInput{Action: "reject", ExpectedRevision: 0, Reason: "证据不足，驳回重新核对"}
	f.request(f.token("alice"), "POST", path, body, 200)
	body.Action = "approve"
	body.ConfirmedFinal = true
	body.RequestID = "unknown"
	f.request(f.token("alice"), "POST", path, body, 409)
	if _, err := f.pool.Exec(context.Background(), `ALTER TABLE audit_events ADD CONSTRAINT reject_import CHECK(action<>'billing.imported') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	f.request(f.token("alice"), "POST", base+"/imports", billImport("rolled-back,,2026-11,CNY_MICRO,10,0,0"), 503)
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM billing_records`).Scan(&count); err != nil || count != 2 {
		t.Fatal("failed import partially committed")
	}
}

func TestBillDuplicateRequestAndTerminalDifferenceBlocked(t *testing.T) {
	f := providerSetup(t, testutil.SupplierFixture(t))
	o, p, _ := providerProject(f)
	base := projectPath(o, p) + "/billing"
	f.member(o, "reviewer", "admin")
	heldForBill(t, f, o, p, "duplicate-hold", 100)
	in := billImport("duplicate-a,duplicate-hold,2026-11,CNY_MICRO,40,0,0")
	in.CSV += "duplicate-b,duplicate-hold,2026-11,CNY_MICRO,40,0,0\n"
	f.request(f.token("alice"), "POST", base+"/imports", in, 200)
	records := parse[billPage](t, f.request(f.token("alice"), "GET", base+"/records", nil, 200))
	if len(records.Data) != 2 {
		t.Fatal("duplicate candidates missing")
	}
	for _, record := range records.Data {
		found := false
		for _, issue := range record.Issues {
			found = found || issue == "duplicate_request"
		}
		if !found {
			t.Fatal("duplicate request billing was not surfaced")
		}
	}
	first := base + "/records/" + records.Data[0].ID + "/review"
	second := base + "/records/" + records.Data[1].ID + "/review"
	f.request(f.token("reviewer"), "POST", first, approveBill("duplicate-hold"), 409)
	f.request(f.token("alice"), "POST", second, control.ReviewInput{Action: "reject", Reason: "排除重复账单证据"}, 200)
	f.request(f.token("reviewer"), "POST", first, approveBill("duplicate-hold"), 200)
	heldForBill(t, f, o, p, "terminal-hold", 100)
	if err := budget.New(f.pool).Settle(context.Background(), "terminal-hold", 40); err != nil {
		t.Fatal(err)
	}
	f.request(f.token("alice"), "POST", base+"/imports", billImport("terminal-diff,terminal-hold,2026-11,CNY_MICRO,30,0,0"), 200)
	page := parse[billPage](t, f.request(f.token("alice"), "GET", base+"/records?limit=1", nil, 200))
	path := base + "/records/" + page.Data[0].ID + "/review"
	f.request(f.token("reviewer"), "POST", path, approveBill("terminal-hold"), 409)
	r, err := budget.New(f.pool).Get(context.Background(), "terminal-hold")
	if err != nil || r.Actual == nil || *r.Actual != 40 {
		t.Fatal("terminal discrepancy rewrote settled amount")
	}
}

func TestReviewedSupplierHistoricalBillingAndScope(t *testing.T) {
	f := providerSetup(t, testutil.SupplierFixture(t))
	o, p, _ := providerProject(f)
	base := projectPath(o, p) + "/billing"
	in := billImport("reviewed-invoice,reviewed-hold,2026-11,CNY_MICRO,40,3,2")
	in.Provider = "reviewed-compatible"
	f.request(f.token("alice"), "POST", base+"/imports", in, 400)
	heldForBill(t, f, o, p, "reviewed-hold", 100)
	if _, err := f.pool.Exec(context.Background(), `UPDATE inference_attempts SET provider='reviewed-compatible',region='cn-mainland' WHERE request_id='reviewed-hold'`); err != nil {
		t.Fatal(err)
	}
	f.member(o, "reviewer", "admin")
	f.request(f.token("alice"), "POST", base+"/imports", in, 200)
	records := parse[billPage](t, f.request(f.token("alice"), "GET", base+"/records", nil, 200))
	if len(records.Data) != 1 || records.Data[0].Provider != "reviewed-compatible" {
		t.Fatal("supplier attribution lost")
	}
	review := base + "/records/" + records.Data[0].ID + "/review"
	f.request(f.token("alice"), "POST", review, approveBill("reviewed-hold"), 403)
	f.request(f.token("reviewer"), "POST", review, approveBill("reviewed-hold"), 200)
	f.request(f.token("reviewer"), "POST", review, approveBill("reviewed-hold"), 200)
	reservation, err := budget.New(f.pool).Get(context.Background(), "reviewed-hold")
	if err != nil || reservation.Actual == nil || *reservation.Actual != 40 || reservation.State != "settled" {
		t.Fatal("reviewed supplier did not settle once")
	}
	f.request(f.token("outsider"), "GET", base+"/records", nil, 404)
}
