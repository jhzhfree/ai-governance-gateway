package newapibridge_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/execution"
	"github.com/jhzhfree/ai-governance-gateway/internal/newapibridge"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func TestRecoveryReceiptAtomicReplayAndBinding(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		pool, ledger, scope := testutil.Database(t)
		ctx := context.Background()
		scope.Currency = provider.Currency
		testutil.Accounts(t, ledger, scope, 10000, 10000)
		cfgDirectory := testutil.MainlandConfig()
		for i := range cfgDirectory.Models {
			cfgDirectory.Models[i].NewAPIChannelID = 1
			cfgDirectory.Models[i].UpstreamModel = "backend-model"
		}
		directory, err := provider.NewGoverned(cfgDirectory)
		if err != nil {
			t.Fatal(err)
		}
		route, _ := directory.Find("zhipu/fixture")
		const requestID = "Request_1234567890"
		const attemptID = "Attempt_1234567890"
		const backend = "https://synthetic.invalid"
		amount, err := route.Bound(8)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: requestID, PriceVersion: route.PriceVersion(), Amount: amount}); err != nil {
			t.Fatal(err)
		}
		if err := execution.New(pool).Begin(ctx, execution.Start{AttemptID: attemptID, RequestID: requestID, Model: "zhipu/fixture", Provider: "zhipu", Region: "cn-mainland"}); err != nil {
			t.Fatal(err)
		}
		if err := ledger.Dispatch(ctx, requestID); err != nil {
			t.Fatal(err)
		}
		journal := newapibridge.NewJournal(pool)
		if err := journal.Bind(ctx, backend, "backend-model", requestID, attemptID, strings.Repeat("a", 64), route, 8); err != nil {
			t.Fatal(err)
		}
		if err := journal.Bind(ctx, backend, "backend-model", requestID, attemptID, strings.Repeat("a", 64), route, 8); err == nil {
			t.Fatal("duplicate binding permitted dispatch")
		}
		e := newapibridge.Event{AttemptID: attemptID, RequestID: requestID, State: "completed", Model: "backend-model", ChannelID: 1, PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5}
		if cancelled {
			e.State = "cancelled"
			e.Model = ""
			e.ChannelID = 0
			e.PromptTokens = 0
			e.CompletionTokens = 0
			e.TotalTokens = 0
		}
		if err := journal.Apply(ctx, "https://wrong.invalid", e); err == nil {
			t.Fatal("wrong backend accepted")
		}

		fresh, err := journal.Stale(ctx, backend)
		if err != nil || len(fresh) != 0 {
			t.Fatal("fresh request selected for cancellation")
		}
		if _, err := pool.Exec(ctx, `UPDATE newapi_execution_bindings SET created_at=clock_timestamp()-interval '4 minutes' WHERE attempt_id=$1`, attemptID); err != nil {
			t.Fatal(err)
		}
		stale, err := journal.Stale(ctx, backend)
		if err != nil || len(stale) != 1 || stale[0].AttemptID != attemptID {
			t.Fatal("expired binding not eligible for proof check")
		}
		badChannel := e
		badChannel.ChannelID++
		if journal.Apply(ctx, backend, badChannel) == nil {
			t.Fatal("unapproved channel accepted")
		}
		// Force a receipt write failure after ledger settlement: both must roll back.
		if _, err := pool.Exec(ctx, `ALTER TABLE newapi_execution_receipts ADD CONSTRAINT fail_receipt CHECK(actual<0)`); err != nil {
			t.Fatal(err)
		}
		if err := journal.Apply(ctx, backend, e); err == nil {
			t.Fatal("receipt failure ignored")
		}
		r, err := ledger.Get(ctx, requestID)
		if err != nil || r.State != "dispatched" {
			t.Fatal("receipt failure changed ledger")
		}
		if _, err := pool.Exec(ctx, `ALTER TABLE newapi_execution_receipts DROP CONSTRAINT fail_receipt`); err != nil {
			t.Fatal(err)
		}
		if err := ledger.MarkUncertain(ctx, requestID); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for range 5 {
			wg.Go(func() {
				if err := newapibridge.NewJournal(pool).Apply(ctx, backend, e); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		r, err = ledger.Get(ctx, requestID)
		want := int64(16)
		if cancelled {
			want = 0
		}
		if err != nil || r.State != "settled" || r.Actual == nil || *r.Actual != want {
			t.Fatal("recovery did not settle exactly once")
		}
		bad := e
		bad.RequestID = "WrongRequest12345"
		if journal.Apply(ctx, backend, bad) == nil {
			t.Fatal("wrong request accepted")
		}
		bad = e
		bad.CompletionTokens++
		bad.TotalTokens++
		if journal.Apply(ctx, backend, bad) == nil {
			t.Fatal("conflicting evidence accepted")
		}
	}
}
