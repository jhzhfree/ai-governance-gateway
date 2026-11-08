package completion_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/completion"
	"github.com/jhzhfree/ai-governance-gateway/internal/execution"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

const usage = `{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}`

func TestChatUsageKeepsOnlyOriginalNumericEvidence(t *testing.T) {
	valid := []string{usage, `{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5,"prompt_cache_hit_tokens":1,"prompt_cache_miss_tokens":2,"prompt_tokens_details":{"cached_tokens":1},"completion_tokens_details":{"reasoning_tokens":1}}`}
	for _, raw := range valid {
		c, err := completion.ChatUsage([]byte(raw))
		if err != nil || c.Input != 3 || c.Output != 2 {
			t.Fatal("valid counters rejected")
		}
	}
	for _, raw := range []string{`{}`, `null`, usage + ` {}`, strings.Replace(usage, `"prompt_tokens":3`, `"prompt_tokens":3,"prompt_tokens":4`, 1), strings.Replace(usage, `"prompt_tokens":3`, `"prompt_tokens":null`, 1), strings.Replace(usage, `"completion_tokens":2`, `"completion_tokens":2.0`, 1), strings.Replace(usage, `"total_tokens":5`, `"total_tokens":6`, 1), strings.TrimSuffix(usage, "}") + `,"prompt_tokens_details":{"content":"synthetic private input"}}`, strings.TrimSuffix(usage, "}") + `,"completion_tokens_details":{"reasoning_tokens":1,"reasoning_tokens":2}}`, strings.TrimSuffix(usage, "}") + `,"prompt_cache_hit_tokens":2}`, strings.TrimSuffix(usage, "}") + `,"prompt_cache_hit_tokens":2,"prompt_cache_miss_tokens":2}`} {
		if _, err := completion.ChatUsage([]byte(raw)); err == nil {
			t.Fatal("invalid or content-bearing evidence accepted")
		}
	}
}

func TestReceiptRecoveryIsAtomicBoundAndIdempotent(t *testing.T) {
	db, ledger, scope := testutil.Database(t)
	ctx := context.Background()
	scope.Currency = provider.Currency
	testutil.Accounts(t, ledger, scope, 10000, 10000)
	cfg := testutil.MainlandConfig()
	for i := range cfg.Models {
		cfg.Models[i].MaxOutputTokens = 32
	}
	catalog, err := provider.New(cfg, func(string) string { return "synthetic" }, nil)
	if err != nil {
		t.Fatal(err)
	}
	route, _ := catalog.Find("aliyun/fixture")
	rotated, err := provider.New(cfg, func(string) string { return "different-synthetic-key" }, nil)
	if err != nil {
		t.Fatal(err)
	}
	rotatedRoute, _ := rotated.Find("aliyun/fixture")
	if completion.RouteDigest(route) != completion.RouteDigest(rotatedRoute) || route.ExecutionIdentity() == rotatedRoute.ExecutionIdentity() {
		t.Fatal("durable digest depends on credential or in-memory binding lost rotation")
	}
	req := provider.Request{Model: "aliyun/fixture", MaxTokens: 16, Messages: []provider.Message{{Role: "user", Content: "synthetic"}}}
	journal := completion.New(db)
	prepare := func(id, attempt string) {
		t.Helper()
		amount, err := route.Bound(16)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: id, PriceVersion: route.PriceVersion(), Amount: amount}); err != nil {
			t.Fatal(err)
		}
		if err := execution.New(db).Begin(ctx, execution.Start{AttemptID: attempt, RequestID: id, Model: req.Model, Provider: route.Provider(), Region: route.Region()}); err != nil {
			t.Fatal(err)
		}
		if err := journal.Bind(ctx, route, req, id, attempt); err == nil {
			t.Fatal("bound before durable dispatch")
		}
		if err := ledger.Dispatch(ctx, id); err != nil {
			t.Fatal(err)
		}
		if err := journal.Bind(ctx, route, req, id, attempt); err != nil {
			t.Fatal(err)
		}
		if err := journal.Bind(ctx, route, req, id, attempt); err == nil {
			t.Fatal("duplicate physical dispatch admitted")
		}
	}
	prepare("request_complete", "attempt_complete")
	if err := journal.Settle(ctx, "request_complete", "attempt_complete", 14); err == nil {
		t.Fatal("settled without completion")
	}
	if err := journal.Record(ctx, "request_wrong", "attempt_complete", completion.RouteDigest(route), []byte(usage)); err == nil {
		t.Fatal("cross-request evidence accepted")
	}
	wrong := completion.RouteDigest(route)
	wrong[0] ^= 1
	if err := journal.Record(ctx, "request_complete", "attempt_complete", wrong, []byte(usage)); err == nil {
		t.Fatal("wrong execution snapshot accepted")
	}
	for range 2 {
		if err := journal.Record(ctx, "request_complete", "attempt_complete", completion.RouteDigest(route), []byte(usage)); err != nil {
			t.Fatal(err)
		}
	}
	for _, conflicting := range []string{`{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}`, " " + usage} {
		if err := journal.Record(ctx, "request_complete", "attempt_complete", completion.RouteDigest(route), []byte(conflicting)); !errors.Is(err, completion.ErrConflict) {
			t.Fatal("valid but byte-different completion replaced immutable evidence", err)
		}
	}
	if err := journal.Settle(ctx, "request_complete", "attempt_complete", 15); err == nil {
		t.Fatal("live amount bypassed snapshot")
	}
	if _, err := db.Exec(ctx, `CREATE FUNCTION reject_embedded_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic receipt failure'; END $$; CREATE TRIGGER receipt_failure BEFORE INSERT ON engine_completion_receipts FOR EACH ROW EXECUTE FUNCTION reject_embedded_receipt()`); err != nil {
		t.Fatal(err)
	}
	if err := journal.Settle(ctx, "request_complete", "attempt_complete", 14); err == nil {
		t.Fatal("failed receipt committed")
	}
	r, err := ledger.Get(ctx, "request_complete")
	if err != nil || r.State != "dispatched" || r.Actual != nil {
		t.Fatal("receipt failure did not roll back ledger")
	}
	if _, err := db.Exec(ctx, `DROP TRIGGER receipt_failure ON engine_completion_receipts`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := completion.New(db).Settle(ctx, "request_complete", "attempt_complete", 14); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var reconciled bool
	if err := db.QueryRow(ctx, `SELECT reconciled FROM engine_completion_events WHERE attempt_id='attempt_complete'`).Scan(&reconciled); err != nil || !reconciled {
		t.Fatal("receipt did not retire pending event")
	}
	r, err = ledger.Get(ctx, "request_complete")
	if err != nil || r.State != "settled" || r.Actual == nil || *r.Actual != 14 {
		t.Fatal("concurrent receipt settlement failed")
	}
	var events int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM budget_outbox WHERE request_id='request_complete' AND event_type='settled'`).Scan(&events); err != nil || events != 1 {
		t.Fatal("duplicated ledger settlement", err)
	}
	prepare("request_missing", "attempt_missing")
	prepare("request_orphaned", "attempt_orphaned")
	// Reproduce the durable state if killed after Dispatch but before Bind.
	if _, err := db.Exec(ctx, `DELETE FROM engine_execution_bindings WHERE attempt_id='attempt_orphaned'`); err != nil {
		t.Fatal(err)
	}
	prepare("request_recover", "attempt_recover")
	prepare("request_poisoned", "attempt_poisoned")
	for _, suffix := range []string{"recover", "poisoned"} {
		if err := journal.Record(ctx, "request_"+suffix, "attempt_"+suffix, completion.RouteDigest(route), []byte(usage)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ctx, `UPDATE budget_reservations SET updated_at=clock_timestamp()-interval '4 minutes'; UPDATE engine_execution_bindings SET created_at=clock_timestamp()-interval '4 minutes'; UPDATE engine_completion_events SET created_at=clock_timestamp()-interval '4 minutes'; UPDATE engine_completion_events SET usage_sha256=decode(repeat('00',32),'hex') WHERE attempt_id='attempt_poisoned'`); err != nil {
		t.Fatal(err)
	}
	n, err := completion.New(db).Recover(ctx, time.Now().Add(-3*time.Minute))
	if n != 1 || !errors.Is(err, completion.ErrConflict) {
		t.Fatal("poison blocked independent recovery", n, err)
	}
	r, err = ledger.Get(ctx, "request_recover")
	if err != nil || r.State != "settled" || r.Actual == nil || *r.Actual != 14 {
		t.Fatal("recovery lost original price")
	}
	r, err = ledger.Get(ctx, "request_missing")
	if err != nil || r.State != "uncertain" || r.Actual != nil {
		t.Fatal("missing completion changed hold")
	}
	r, err = ledger.Get(ctx, "request_orphaned")
	if err != nil || r.State != "uncertain" || r.Actual != nil {
		t.Fatal("pre-binding crash gap lost hold")
	}
	r, err = ledger.Get(ctx, "request_poisoned")
	if err != nil || r.Actual != nil {
		t.Fatal("tampered evidence settled")
	}
}
