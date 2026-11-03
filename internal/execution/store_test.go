package execution_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/execution"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func TestCompletionRequiresSettlementAndIsImmutable(t *testing.T) {
	pool, ledger, scope := testutil.Database(t)
	testutil.Accounts(t, ledger, scope, 100, 100)
	ctx := context.Background()
	if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: "execution-request", PriceVersion: "mock-v1", Amount: 10}); err != nil {
		t.Fatal(err)
	}
	store := execution.New(pool)
	start := execution.Start{AttemptID: "attempt-1", RequestID: "execution-request", Model: "mock-text", Provider: "mock", Region: "local", Stream: true}
	if err := store.Begin(ctx, start); err != nil {
		t.Fatal(err)
	}
	if err := store.Begin(ctx, start); !errors.Is(err, execution.ErrConflict) {
		t.Fatal("duplicate begin authorized another attempt")
	}
	status := 200
	input, output := int64(2), int64(2)
	result := execution.Result{Outcome: "completed", UpstreamStatus: &status, InputTokens: &input, OutputTokens: &output}
	if err := store.Finish(ctx, start.AttemptID, result); !errors.Is(err, execution.ErrInvalid) {
		t.Fatal("completed metadata was accepted before accounting settlement")
	}
	if err := ledger.Dispatch(ctx, start.RequestID); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Settle(ctx, start.RequestID, 4); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := store.Finish(ctx, start.AttemptID, result); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var before, after time.Time
	var duration int64
	if err := pool.QueryRow(ctx, `SELECT finished_at,duration_ms FROM inference_attempts WHERE attempt_id=$1`, start.AttemptID).Scan(&before, &duration); err != nil || duration < 0 {
		t.Fatal("completed timing was not durable")
	}
	if err := store.Finish(ctx, start.AttemptID, result); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT finished_at FROM inference_attempts WHERE attempt_id=$1`, start.AttemptID).Scan(&after); err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("idempotent finish changed original timestamp")
	}
	if err := store.Finish(ctx, start.AttemptID, execution.Result{Outcome: "failed", ErrorCode: "metadata_finalize_failed"}); !errors.Is(err, execution.ErrConflict) {
		t.Fatal("late failure overwrote completed execution")
	}
}
func TestInvalidMetadataCannotPersistRawErrorsOrChangeState(t *testing.T) {
	pool, ledger, scope := testutil.Database(t)
	testutil.Accounts(t, ledger, scope, 100, 100)
	ctx := context.Background()
	if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: "invalid-metadata", PriceVersion: "mock-v1", Amount: 10}); err != nil {
		t.Fatal(err)
	}
	store := execution.New(pool)
	if err := store.Begin(ctx, execution.Start{AttemptID: "bad", RequestID: "invalid-metadata", Model: "mock-text", Provider: "mock", Region: "cn-mainland"}); !errors.Is(err, execution.ErrInvalid) {
		t.Fatal("mismatched provider region accepted")
	}
	if err := store.Begin(ctx, execution.Start{AttemptID: "valid", RequestID: "invalid-metadata", Model: "mock-text", Provider: "mock", Region: "local"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Finish(ctx, "valid", execution.Result{Outcome: "failed", ErrorCode: "raw sensitive upstream error"}); !errors.Is(err, execution.ErrInvalid) {
		t.Fatal("raw error text accepted")
	}
	if err := store.Finish(ctx, "valid", execution.Result{Outcome: "completed"}); !errors.Is(err, execution.ErrInvalid) {
		t.Fatal("completion without usage and upstream status accepted")
	}
	var outcome string
	var finished *time.Time
	if err := pool.QueryRow(ctx, `SELECT outcome,finished_at FROM inference_attempts WHERE attempt_id='valid'`).Scan(&outcome, &finished); err != nil || outcome != "unfinished" || finished != nil {
		t.Fatal("invalid writes finalized the attempt")
	}
	r, err := ledger.Get(ctx, "invalid-metadata")
	if err != nil || r.State != "reserved" || r.Actual != nil {
		t.Fatal("metadata validation changed budget state")
	}
}
