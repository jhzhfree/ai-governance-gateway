package httpapi_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
)

type requestPage struct {
	Data []control.RequestRecord `json:"data"`
	Next string                  `json:"next_cursor"`
}

func TestInferenceRequestLedgerContractAndIsolation(t *testing.T) {
	f := setup(t)
	o := f.org("alice")
	p := f.project("alice", o)
	key := f.key("alice", o, p)
	f.request(key.Secret, "POST", "/v1/chat/completions", inference(), 200)
	path := projectPath(o, p) + "/requests"
	page := parse[requestPage](t, f.request(f.token("alice"), "GET", path, nil, 200))
	if len(page.Data) != 1 || page.Data[0].State != "settled" || page.Data[0].Actual == nil || *page.Data[0].Actual != 4 {
		t.Fatal("successful inference missing durable accounting record")
	}
	record := page.Data[0]
	if record.Attempt == nil || record.Attempt.Outcome != "completed" || record.Attempt.Model != "mock-text" || record.Attempt.Provider != "mock" || record.Attempt.RequestID != record.RequestID || record.Attempt.DurationMS == nil || record.Attempt.FinishedAt == nil || record.Attempt.UpstreamStatus == nil || *record.Attempt.UpstreamStatus != 200 {
		t.Fatal("durable request execution metadata missing")
	}

	detail := f.request(f.token("alice"), "GET", path+"/"+record.RequestID, nil, 200)
	for _, secret := range []string{key.Secret, `"messages"`, `"content"`, `"digest"`, `"token"`} {
		if strings.Contains(string(detail), secret) {
			t.Fatal("record leaked payload or credential")
		}
	}
	other := f.project("alice", o)
	f.request(f.token("alice"), "GET", projectPath(o, other)+"/requests/"+record.RequestID, nil, 404)
	f.request(f.token("outsider"), "GET", path, nil, 404)
	f.request(key.Secret, "GET", path, nil, 401)
	f.member(o, "viewer", "viewer")
	f.request(f.token("viewer"), "GET", path+"/"+record.RequestID, nil, 200)
	f.member(o, "dev", "developer")
	f.request(f.token("dev"), "GET", path, nil, 404)
	f.request(f.token("alice"), "PUT", projectPath(o, p)+"/members", map[string]string{"issuer": f.issuer.URL, "subject": "dev", "role": "developer"}, 204)
	f.request(f.token("dev"), "GET", path, nil, 200)
	f.request(f.token("dev"), "GET", projectPath(o, other)+"/requests", nil, 404)
	foreignOrg := f.org("bob")
	foreignProject := f.project("bob", foreignOrg)
	f.request(f.token("bob"), "GET", projectPath(foreignOrg, foreignProject)+"/requests/"+record.RequestID, nil, 404)
}
func TestRequestHistoryCursorTiesFiltersAndUncertainHold(t *testing.T) {
	f := setup(t)
	o := f.org("alice")
	p := f.project("alice", o)
	ledger := budget.New(f.pool)
	ctx := context.Background()
	scope := budget.Scope{Organization: o.ID, Project: p.ID, Period: o.Period, Currency: o.Currency}
	for _, id := range []string{"request-a", "request-b", "request-c"} {
		if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: id, PriceVersion: "mock-v1", Amount: 10}); err != nil {
			t.Fatal(err)
		}
		if err := ledger.Dispatch(ctx, id); err != nil {
			t.Fatal(err)
		}
		if err := ledger.MarkUncertain(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.pool.Exec(ctx, `UPDATE budget_reservations SET created_at='2026-11-01T00:00:00Z' WHERE organization_id=$1`, o.ID); err != nil {
		t.Fatal(err)
	}
	path := projectPath(o, p) + "/requests"
	first := parse[requestPage](t, f.request(f.token("alice"), "GET", path+"?limit=2&state=uncertain", nil, 200))
	if len(first.Data) != 2 || first.Data[0].RequestID != "request-c" || first.Data[1].RequestID != "request-b" || first.Next == "" {
		t.Fatal("unstable equal-time ordering")
	}
	second := parse[requestPage](t, f.request(f.token("alice"), "GET", path+"?limit=2&state=uncertain&after="+first.Next, nil, 200))
	if len(second.Data) != 1 || second.Data[0].RequestID != "request-a" || second.Next != "" || second.Data[0].Actual != nil {
		t.Fatal("cursor duplicated/skipped records or fabricated zero charge")
	}
	empty := parse[requestPage](t, f.request(f.token("alice"), "GET", path+"?state=settled", nil, 200))
	if len(empty.Data) != 0 {
		t.Fatal("state filter ignored")
	}
	all := parse[requestPage](t, f.request(f.token("alice"), "GET", path+"?state=", nil, 200))
	if len(all.Data) != 3 {
		t.Fatal("empty state filter must return all states")
	}
	usage := parse[control.Usage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/usage", nil, 200))
	if usage.Held != 30 || usage.Spent != 0 {
		t.Fatal("read changed conservative holds")
	}
	for _, query := range []string{"?after=invalid", "?after=eDph", "?state=success", "?outcome=success", "?limit=101"} {
		f.request(f.token("alice"), "GET", path+query, nil, 400)
	}
	f.request(f.token("alice"), "GET", path+"/unknown", nil, 404)
}
