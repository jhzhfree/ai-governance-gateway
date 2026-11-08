package httpapi_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/completion"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/execution"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func TestEmbeddedCompletionMetadataReportsAndScope(t *testing.T) {
	f := providerSetup(t, testutil.SupplierFixture(t))
	o, p, key := providerProject(f)
	ctx := context.Background()
	scope := budget.Scope{Organization: o.ID, Project: p.ID, Period: o.Period, Currency: o.Currency}
	base := projectPath(o, p)
	for _, state := range []string{"awaiting_usage", "recorded", "receipted"} {
		id := "evidence-" + state
		testutil.SeedEmbeddedCompletion(t, f.pool, scope, id, state)
		data := f.request(f.token("alice"), "GET", base+"/requests/"+id, nil, 200)
		v := parse[control.RequestRecord](t, data)
		e := v.EmbeddedCompletion
		if e == nil || e.Engine != "bifrost" || e.Protocol != "chat_v1" || e.State != state || e.InputPerMillion != 2000000 || e.OutputPerMillion != 4000000 || v.Attempt == nil || v.Attempt.Outcome != "unfinished" || v.Attempt.InputTokens != nil {
			t.Fatal("evidence lost snapshot or replaced client outcome")
		}
		if state == "awaiting_usage" {
			if e.InputTokens != nil || e.OutputTokens != nil || e.RecordedAt != nil {
				t.Fatal("missing evidence fabricated usage")
			}
		} else if e.InputTokens == nil || *e.InputTokens != 3 || e.OutputTokens == nil || *e.OutputTokens != 2 || e.RecordedAt == nil {
			t.Fatal("recorded counters missing")
		}
		if state == "receipted" {
			if e.Actual == nil || *e.Actual != 14 || e.ReceivedAt == nil || v.Actual == nil || *v.Actual != 14 || v.State != "settled" {
				t.Fatal("receipt and ledger disagree")
			}
		} else if e.Actual != nil || e.ReceivedAt != nil || v.Actual != nil || v.State != "uncertain" {
			t.Fatal("unposted evidence became settlement")
		}
		for _, secret := range []string{"raw_usage", "usage_sha256", "route_digest", "upstream_model", "synthetic-metadata-fixture", "synthetic metadata fixture", key.Secret} {
			if strings.Contains(string(data), secret) {
				t.Fatal("metadata exposed internal evidence or content")
			}
		}
	}
	page := parse[requestPage](t, f.request(f.token("alice"), "GET", base+"/requests", nil, 200))
	if len(page.Data) != 3 {
		t.Fatal("completion joins multiplied requests")
	}
	other := f.project("alice", o)
	f.request(f.token("alice"), "GET", projectPath(o, other)+"/requests/evidence-receipted", nil, 404)
	f.request(f.token("outsider"), "GET", base+"/requests/evidence-receipted", nil, 404)
	f.request(key.Secret, "GET", base+"/requests/evidence-receipted", nil, 401)
	f.member(o, "viewer", "viewer")
	f.request(f.token("viewer"), "GET", base+"/requests/evidence-receipted", nil, 200)
	foreign := parse[control.Organization](t, f.request(f.token("bob"), "POST", "/api/v1/organizations", map[string]any{"name": "Foreign evidence scope", "period": "2026-11", "currency": "CNY_MICRO", "limit_amount": 10000}, 201))
	foreignProject := f.project("bob", foreign)
	f.request(f.token("bob"), "GET", projectPath(foreign, foreignProject)+"/requests/evidence-receipted", nil, 404)
	if _, err := f.pool.Exec(ctx, `UPDATE budget_reservations SET created_at='2026-11-03T10:00:00Z' WHERE organization_id=$1`, o.ID); err != nil {
		t.Fatal(err)
	}
	reportPath := base + "/cost-report?from=2026-11-03&to=2026-11-04"
	checkReport := func(known int64, cost, input, output string) {
		t.Helper()
		r := parse[control.CostReport](t, f.request(f.token("alice"), "GET", reportPath, nil, 200))
		if len(r.Rows) != 1 || r.Rows[0].Admitted != 3 || r.Rows[0].UsageKnown != known || r.Rows[0].SettledAmount != cost || r.Rows[0].InputTokens != input || r.Rows[0].OutputTokens != output {
			t.Fatal("recovered cost report lost or double-counted original usage")
		}
	}
	checkReport(1, "14", "3", "2")
	// The request thread can also finish after posting. The report must select
	// one quantity source instead of adding attempt and completion counters.
	input, output := int64(3), int64(2)
	if err := execution.New(f.pool).Finish(ctx, "attempt-evidence-receipted", execution.Result{Outcome: "canceled", ErrorCode: "client_canceled", InputTokens: &input, OutputTokens: &output}); err != nil {
		t.Fatal(err)
	}
	checkReport(1, "14", "3", "2")
	// Imported quantities must be checked against a recovered receipt even when
	// the killed client attempt never recorded its counters.
	f.request(f.token("alice"), "POST", base+"/billing/imports", billImport("evidence-invoice,evidence-receipted,2026-11,CNY_MICRO,14,9,2"), 200)
	bills := parse[billPage](t, f.request(f.token("alice"), "GET", base+"/billing/records", nil, 200))
	if len(bills.Data) != 1 || len(bills.Data[0].Issues) != 1 || bills.Data[0].Issues[0] != "usage_mismatch" {
		t.Fatal("bill ignored recovered usage")
	}
	if n, err := completion.New(f.pool).Recover(ctx, time.Now().Add(time.Minute)); err != nil || n != 1 {
		t.Fatal("pending evidence not recovered", err)
	}
	checkReport(2, "28", "6", "4")
	v := parse[control.RequestRecord](t, f.request(f.token("alice"), "GET", base+"/requests/evidence-recorded", nil, 200))
	if v.EmbeddedCompletion == nil || v.EmbeddedCompletion.State != "receipted" || v.Attempt.Outcome != "unfinished" {
		t.Fatal("late receipt changed client semantics")
	}
}

func TestEmbeddedEmbeddingEvidenceScopesAndRecoveredReports(t *testing.T) {
	f := providerSetup(t, testutil.SupplierFixture(t))
	o, p, key := providerProject(f)
	ctx := context.Background()
	scope := budget.Scope{Organization: o.ID, Project: p.ID, Period: o.Period, Currency: o.Currency}
	base := projectPath(o, p)
	for _, state := range []string{"awaiting_usage", "recorded", "receipted"} {
		id := "embedding-" + state
		testutil.SeedEmbeddedEmbeddingCompletion(t, f.pool, scope, id, state)
		data := f.request(f.token("alice"), "GET", base+"/requests/"+id, nil, 200)
		record := parse[control.RequestRecord](t, data)
		e := record.EmbeddedCompletion
		if e == nil || e.Protocol != "embeddings_v1" || e.State != state || e.OutputPerMillion != 0 || record.Attempt == nil || record.Attempt.Outcome != "unfinished" {
			t.Fatal("embedding protocol or client outcome lost")
		}
		if state != "awaiting_usage" && (e.InputTokens == nil || *e.InputTokens != 3 || e.OutputTokens == nil || *e.OutputTokens != 0) {
			t.Fatal("input-only quantities incorrect")
		}
		if strings.Contains(string(data), "raw_usage") || strings.Contains(string(data), "synthetic metadata fixture") {
			t.Fatal("evidence content leaked")
		}
	}
	other := f.project("alice", o)
	f.request(f.token("alice"), "GET", projectPath(o, other)+"/requests/embedding-receipted", nil, 404)
	f.request(f.token("outsider"), "GET", base+"/requests/embedding-receipted", nil, 404)
	f.request(key.Secret, "GET", base+"/requests/embedding-receipted", nil, 401)
	if _, err := f.pool.Exec(ctx, `UPDATE budget_reservations SET created_at='2026-11-03T10:00:00Z' WHERE organization_id=$1`, o.ID); err != nil {
		t.Fatal(err)
	}
	check := func(cost, input string, known int64) {
		t.Helper()
		r := parse[control.CostReport](t, f.request(f.token("alice"), "GET", base+"/cost-report?from=2026-11-03&to=2026-11-04", nil, 200))
		if len(r.Rows) != 1 || r.Rows[0].SettledAmount != cost || r.Rows[0].InputTokens != input || r.Rows[0].OutputTokens != "0" || r.Rows[0].UsageKnown != known {
			t.Fatal("input-only report mismatch")
		}
	}
	check("6", "3", 1)
	if n, err := completion.New(f.pool).Recover(ctx, time.Now().Add(time.Minute)); err != nil || n != 1 {
		t.Fatal("embedding recovery failed", err)
	}
	check("12", "6", 2)
	f.request(f.token("alice"), "POST", base+"/billing/imports", billImport("embedding-invoice,embedding-recorded,2026-11,CNY_MICRO,6,3,1"), 200)
	bills := parse[billPage](t, f.request(f.token("alice"), "GET", base+"/billing/records", nil, 200))
	if len(bills.Data) != 1 || len(bills.Data[0].Issues) != 1 || bills.Data[0].Issues[0] != "usage_mismatch" {
		t.Fatal("embedding invoice output discrepancy ignored")
	}
}
