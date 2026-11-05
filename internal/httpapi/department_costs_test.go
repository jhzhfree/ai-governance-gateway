package httpapi_test

import (
	"context"
	"encoding/csv"
	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"strings"
	"testing"
)

func TestDepartmentCostSnapshotsSurviveMovesRenamesAndLateSettlement(t *testing.T) {
	f := readyManaged(t)
	o, p, key := providerProject(f)
	f.member(o, "bob", "developer")
	ctx := context.Background()
	c := putDepartments(f, o, departmentFixture(f, o, p), 200)
	ledger := budget.New(f.pool)
	scope := budget.Scope{Organization: o.ID, Project: p.ID, Period: o.Period, Currency: o.Currency}
	q := budget.Quote{Scope: scope, RequestID: "department-open", PriceVersion: "synthetic", Amount: 100}
	if _, err := ledger.Reserve(ctx, q); err != nil {
		t.Fatal(err)
	}
	f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("aliyun/fixture", false), 200)
	// Snapshot labels can contain CSV formula prefixes; export uses JSON text.
	c.Departments[0].Name = "=renamed"
	c.Projects[0].DepartmentID = "sales"
	c = putDepartments(f, o, c, 200)
	if created, err := ledger.Reserve(ctx, q); err != nil || created {
		t.Fatal("idempotent replay changed department")
	}
	f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("aliyun/fixture", false), 200)
	c.Projects = []control.DepartmentProject{}
	putDepartments(f, o, c, 200)
	if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: "department-unassigned", PriceVersion: "synthetic", Amount: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO budget_reservations(request_id,organization_id,project_id,period_id,currency,price_version,reserved,actual,state) VALUES('department-legacy',$1,$2,$3,$4,'legacy',1,0,'released')`, o.ID, p.ID, o.Period, o.Currency); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE budget_reservations SET created_at='2026-11-02T00:00:00Z' WHERE organization_id=$1`, o.ID); err != nil {
		t.Fatal(err)
	}
	base := projectPath(o, p) + "/cost-report?from=2026-11-01&to=2026-11-03"
	read := func(filter string) control.CostReport {
		return parse[control.CostReport](t, f.request(f.token("alice"), "GET", base+filter, nil, 200))
	}
	old := read("&department_id=engineering")
	if len(old.Rows) != 2 {
		t.Fatal("historical ancestor filter lost records")
	}
	for _, r := range old.Rows {
		if r.DepartmentState != "assigned" || len(r.DepartmentPath) != 2 || r.DepartmentPath[0].Name != "研发" || r.DepartmentPath[1].ID != "platform" {
			t.Fatal("current tree rewrote historic snapshot")
		}
	}
	if err := ledger.Dispatch(ctx, q.RequestID); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Settle(ctx, q.RequestID, 40); err != nil {
		t.Fatal(err)
	}
	old = read("&department_id=engineering")
	var found bool
	for _, r := range old.Rows {
		if r.Model == nil {
			found = true
			if r.SettledAmount != "40" || r.OpenReserved != "0" {
				t.Fatal("late settlement changed department or amount")
			}
		}
	}
	if !found {
		t.Fatal("late settlement absent")
	}
	sales := read("&department_id=sales&department_state=assigned")
	if len(sales.Rows) != 1 || sales.Rows[0].DepartmentPath[0].ID != "sales" {
		t.Fatal("move did not affect new requests")
	}
	unassigned := read("&department_state=unassigned")
	if len(unassigned.Rows) != 1 || unassigned.Rows[0].Admitted != 1 || len(unassigned.Rows[0].DepartmentPath) != 0 {
		t.Fatal("unassigned misclassified")
	}
	historic := read("&department_state=historical")
	if len(historic.Rows) != 1 || historic.Rows[0].Admitted != 1 {
		t.Fatal("historical attribution invented")
	}
	all := read("")
	var n int64
	for _, r := range all.Rows {
		n += r.Admitted
	}
	if n != 5 {
		t.Fatal("department grouping duplicated amounts")
	}
	f.request(f.token("outsider"), "GET", base+"&department_id=engineering", nil, 404)
	f.request(f.token("bob"), "GET", base+"&department_id=engineering", nil, 404)
	f.request(f.token("bob"), "GET", orgPath(o)+"/cost-report?from=2026-11-01&to=2026-11-03&department_id=engineering", nil, 403)
	f.request(f.token("alice"), "GET", base+"&department_state=historical&department_id=engineering", nil, 400)
	f.request(f.token("alice"), "GET", base+"&department_id=engineering&department_id=sales", nil, 400)
	f.request(f.token("alice"), "GET", base+"&department_state=wrong", nil, 400)
	exported := f.request(f.token("alice"), "POST", projectPath(o, p)+"/cost-report/export?from=2026-11-01&to=2026-11-03&department_id=engineering", nil, 200)
	rows, err := csv.NewReader(strings.NewReader(string(exported))).ReadAll()
	if err != nil || len(rows) != 3 || rows[0][17] != "department_state" || !strings.Contains(rows[1][18], "研发") {
		t.Fatal("snapshot export missing")
	}
	if _, err := f.pool.Exec(ctx, `ALTER TABLE audit_events ADD CONSTRAINT reject_department_export CHECK(action<>'cost_report.exported') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	f.request(f.token("alice"), "POST", projectPath(o, p)+"/cost-report/export?from=2026-11-01&to=2026-11-03&department_id=engineering", nil, 503)
}
