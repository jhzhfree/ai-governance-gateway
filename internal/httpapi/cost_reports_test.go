package httpapi_test

import (
	"context"
	"encoding/csv"
	"strings"
	"testing"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
)

func TestCostReportStatesScopeDatesAndAuditedExport(t *testing.T) {
	f := readyManaged(t)
	o, p, key := providerProject(f)
	ctx := context.Background()
	admin := f.token("alice")
	base := projectPath(o, p) + "/cost-report"
	f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("aliyun/fixture", false), 200)
	ledger := budget.New(f.pool)
	scope := budget.Scope{Organization: o.ID, Project: p.ID, Period: o.Period, Currency: o.Currency}
	for _, id := range []string{"cost-open", "cost-estimated", "cost-released"} {
		if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: id, PriceVersion: "synthetic", Amount: 100}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ledger.Dispatch(ctx, "cost-estimated"); err != nil {
		t.Fatal(err)
	}
	if err := ledger.MarkUncertain(ctx, "cost-estimated"); err != nil {
		t.Fatal(err)
	}
	if err := ledger.EstimateFinal(ctx, "cost-estimated"); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Release(ctx, "cost-released"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE budget_reservations SET created_at='2026-11-03T23:59:59Z' WHERE organization_id=$1`, o.ID); err != nil {
		t.Fatal(err)
	}
	dates := "?from=2026-11-03&to=2026-11-04"
	report := parse[control.CostReport](t, f.request(admin, "GET", base+dates, nil, 200))
	if len(report.Rows) != 2 || report.ObservedAt.IsZero() {
		t.Fatal("missing cost attribution")
	}
	var settled, unknown *control.CostRow
	for i := range report.Rows {
		r := &report.Rows[i]
		if r.Model == nil {
			unknown = r
		} else {
			settled = r
		}
	}
	if unknown == nil || unknown.Admitted != 3 || unknown.EstimatedAmount != "100" || unknown.OpenReserved != "100" || unknown.Released != 1 || unknown.UsageKnown != 0 || unknown.Provider != nil {
		t.Fatal("unknown costs collapsed or invented attribution")
	}
	if settled == nil || settled.SettledAmount == "0" || settled.UsageKnown != 1 || settled.Admitted != 1 || settled.EstimatedAmount != "0" || settled.OpenReserved != "0" || settled.Currency != "CNY_MICRO" {
		t.Fatal("completed costs missing")
	}
	empty := parse[control.CostReport](t, f.request(admin, "GET", base+"?from=2026-11-04&to=2026-11-05", nil, 200))
	if len(empty.Rows) != 0 {
		t.Fatal("exclusive UTC date bound failed")
	}
	f.request(f.token("outsider"), "GET", base+dates, nil, 404)
	f.member(o, "viewer", "viewer")
	f.request(f.token("viewer"), "GET", base+dates, nil, 200)
	f.request(f.token("viewer"), "GET", orgPath(o)+"/cost-report"+dates, nil, 403)
	orgReport := parse[control.CostReport](t, f.request(admin, "GET", orgPath(o)+"/cost-report"+dates, nil, 200))
	if len(orgReport.Rows) != 2 {
		t.Fatal("organization report lost project")
	}
	for _, q := range []string{"", "?from=wrong&to=2026-11-04", "?from=2026-11-04&to=2026-11-04", "?from=2026-01-01&to=2026-11-04"} {
		f.request(admin, "GET", base+q, nil, 400)
	}
	data := f.request(admin, "POST", base+"/export"+dates, nil, 200)
	csvRows, err := csv.NewReader(strings.NewReader(string(data))).ReadAll()
	if err != nil || len(csvRows) != 3 {
		t.Fatal("invalid export")
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE organization_id=$1 AND action='cost_report.exported'`, o.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("export unaudited")
	}
	if _, err := f.pool.Exec(ctx, `ALTER TABLE audit_events ADD CONSTRAINT reject_cost_export CHECK(action<>'cost_report.exported') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	failed := f.request(admin, "POST", base+"/export"+dates, nil, 503)
	if strings.Contains(string(failed), "day_utc") {
		t.Fatal("audit failure returned export")
	}
	// Late settlement changes its original admission date, never another day.
	if err := ledger.Dispatch(ctx, "cost-open"); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Settle(ctx, "cost-open", 40); err != nil {
		t.Fatal(err)
	}
	updated := parse[control.CostReport](t, f.request(admin, "GET", base+dates, nil, 200))
	for _, row := range updated.Rows {
		if row.Model == nil && (row.OpenReserved != "0" || row.SettledAmount != "40") {
			t.Fatal("late settlement not reflected")
		}
	}
}

func TestCostReportPrecisionAndOverflowNeverTruncate(t *testing.T) {
	f := readyManaged(t)
	o, p, _ := providerProject(f)
	ctx := context.Background()
	admin := f.token("alice")
	base := projectPath(o, p) + "/cost-report?from=2026-11-01&to=2026-11-02"
	for _, period := range []string{"2026-09", "2026-10"} {
		if _, err := f.pool.Exec(ctx, `INSERT INTO budget_accounts(organization_id,project_id,period_id,currency,limit_amount,spent) VALUES($1,$2,$3,$4,9007199254740991,9007199254740991)`, o.ID, p.ID, period, o.Currency); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(ctx, `INSERT INTO budget_reservations(request_id,organization_id,project_id,period_id,currency,price_version,reserved,actual,state,created_at) VALUES($1,$2,$3,$4,$5,'synthetic',9007199254740991,9007199254740991,'settled','2026-11-01T00:00:00Z')`, period, o.ID, p.ID, period, o.Currency); err != nil {
			t.Fatal(err)
		}
	}
	report := parse[control.CostReport](t, f.request(admin, "GET", base, nil, 200))
	if len(report.Rows) != 1 || report.Rows[0].SettledAmount != "18014398509481982" {
		t.Fatal("sum lost integer precision")
	}
	// Artificial historical currencies create >1000 groups; API must refuse the
	// complete report instead of returning plausible but incomplete totals.
	for i := 0; i < 1001; i++ {
		currency := time.Unix(int64(i), 0).UTC().Format("150405")
		if _, err := f.pool.Exec(ctx, `INSERT INTO budget_accounts(organization_id,project_id,period_id,currency,limit_amount) VALUES($1,$2,'2026-11',$3,1)`, o.ID, p.ID, currency); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(ctx, `INSERT INTO budget_reservations(request_id,organization_id,project_id,period_id,currency,price_version,reserved,state,created_at) VALUES($1,$2,$3,'2026-11',$1,'synthetic',1,'reserved','2026-11-01T00:00:00Z')`, currency, o.ID, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	f.request(admin, "GET", base, nil, 400)
}
