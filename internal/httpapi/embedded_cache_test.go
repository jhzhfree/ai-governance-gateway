package httpapi_test

import (
	"context"
	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/completion"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
	"testing"
	"time"
)

func TestEmbeddedCacheEvidenceAndReportCoverage(t *testing.T) {
	for _, native := range []bool{false, true} {
		name := "chat"
		if native {
			name = "messages"
		}
		t.Run(name, func(t *testing.T) { embeddedCacheEvidenceAndReport(t, native) })
	}
}
func embeddedCacheEvidenceAndReport(t *testing.T, native bool) {
	seed := testutil.SeedEmbeddedCacheCompletion
	protocol := "deepseek_cache_v1"
	if native {
		seed = testutil.SeedEmbeddedMessagesCompletion
		protocol = "deepseek_messages_v1"
	}
	f := providerSetup(t, testutil.SupplierFixture(t))
	o, p, key := providerProject(f)
	ctx := context.Background()
	scope := budget.Scope{Organization: o.ID, Project: p.ID, Period: o.Period, Currency: o.Currency}
	base := projectPath(o, p)
	for _, state := range []string{"awaiting_usage", "recorded", "receipted"} {
		id := "cached-" + state
		seed(t, f.pool, scope, id, state)
		v := parse[control.RequestRecord](t, f.request(f.token("alice"), "GET", base+"/requests/"+id, nil, 200))
		e := v.EmbeddedCompletion
		if e == nil || e.Protocol != protocol || e.State != state || e.CacheReadPerMillion == nil || *e.CacheReadPerMillion != 500000 {
			t.Fatal("frozen tariff missing")
		}
		if state == "awaiting_usage" {
			if e.CacheReadTokens != nil || e.UncachedInputTokens != nil {
				t.Fatal("missing counts fabricated")
			}
		} else if e.CacheReadTokens == nil || e.UncachedInputTokens == nil || *e.CacheReadTokens != 2 || *e.UncachedInputTokens != 1 {
			t.Fatal("cache evidence changed")
		}
		if native && state != "awaiting_usage" {
			if e.CacheWriteTokens == nil || *e.CacheWriteTokens != 0 {
				t.Fatal("native explicit zero missing")
			}
		} else if e.CacheWriteTokens != nil {
			t.Fatal("unmeasured write count fabricated")
		}
		if state == "receipted" && (e.Actual == nil || *e.Actual != 11) {
			t.Fatal("wrong cache settlement")
		}
	}
	// Historical flat tariffs remain unmeasured, rather than synthetic zero cache hits.
	testutil.SeedEmbeddedCompletion(t, f.pool, scope, "cached-report-flat", "receipted")
	other := f.project("alice", o)
	f.request(f.token("alice"), "GET", projectPath(o, other)+"/requests/cached-receipted", nil, 404)
	f.request(f.token("outsider"), "GET", base+"/requests/cached-receipted", nil, 404)
	f.request(key.Secret, "GET", base+"/requests/cached-receipted", nil, 401)
	f.member(o, "viewer", "viewer")
	f.request(f.token("viewer"), "GET", base+"/requests/cached-receipted", nil, 200)
	if _, err := f.pool.Exec(ctx, `UPDATE budget_reservations SET created_at='2026-11-03T10:00:00Z' WHERE organization_id=$1`, o.ID); err != nil {
		t.Fatal(err)
	}
	check := func(known int64, cost, hit, miss string) {
		t.Helper()
		report := parse[control.CostReport](t, f.request(f.token("alice"), "GET", base+"/cost-report?from=2026-11-03&to=2026-11-04", nil, 200))
		if len(report.Rows) != 2 {
			t.Fatal("report joins multiplied rows")
		}
		for _, row := range report.Rows {
			if row.Provider != nil && *row.Provider == "deepseek" {
				if row.CacheMeasuredRequests != known || row.UsageKnown != known || row.SettledAmount != cost || row.CacheReadTokens != hit || row.UncachedInputTokens != miss || row.Admitted != 3 {
					t.Fatal("unposted cache counted or posted evidence doubled", row)
				}
			} else if row.CacheMeasuredRequests != 0 || row.CacheReadTokens != "0" || row.UncachedInputTokens != "0" {
				t.Fatal("flat tariff invented cache evidence")
			}
		}
	}
	check(1, "11", "2", "1")
	if n, err := completion.New(f.pool).Recover(ctx, time.Now().Add(time.Minute)); err != nil || n != 1 {
		t.Fatal("cache recovery failed", err)
	}
	check(2, "22", "4", "2")
	invoice := billImport("cache-invoice,cached-recorded,2026-11,CNY_MICRO,11,3,2")
	invoice.Provider = "deepseek"
	f.request(f.token("alice"), "POST", base+"/billing/imports", invoice, 200)
	bills := parse[billPage](t, f.request(f.token("alice"), "GET", base+"/billing/records", nil, 200))
	if len(bills.Data) != 1 || len(bills.Data[0].Issues) != 0 {
		t.Fatal("matching recovered cache bill disagrees")
	}
}
