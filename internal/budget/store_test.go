package budget_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func quote(scope budget.Scope, id string, amount int64) budget.Quote {
	return budget.Quote{Scope: scope, RequestID: id, PriceVersion: "mock-v1", Amount: amount}
}

func balances(t *testing.T, p *pgxpool.Pool, scope budget.Scope, wantSpent, wantHeld int64) {
	t.Helper()
	rows, err := p.Query(context.Background(), `SELECT spent,held FROM budget_accounts WHERE organization_id=$1 AND project_id IN ('',$2) AND period_id=$3 AND currency=$4`, scope.Organization, scope.Project, scope.Period, scope.Currency)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var spent, held int64
		if err := rows.Scan(&spent, &held); err != nil {
			t.Fatal(err)
		}
		if spent != wantSpent || held != wantHeld {
			t.Errorf("spent/held=%d/%d, want %d/%d", spent, held, wantSpent, wantHeld)
		}
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expected two accounts, got %d", n)
	}
}

func TestConcurrentReservationNeverExceedsBothLimits(t *testing.T) {
	for _, limit := range []struct {
		name         string
		org, project int64
	}{{"organization", 70, 100}, {"project", 100, 70}} {
		t.Run(limit.name, func(t *testing.T) {
			p, s, scope := testutil.Database(t)
			testutil.Accounts(t, s, scope, limit.org, limit.project)
			var admitted atomic.Int64
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := 0; i < 50; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					created, err := budget.New(p).Reserve(context.Background(), quote(scope, fmt.Sprint(i), 10))
					if errors.Is(err, budget.ErrExceeded) {
						return
					}
					if err != nil {
						t.Error(err)
						return
					}
					if created {
						admitted.Add(1)
					}
				}(i)
			}
			close(start)
			wg.Wait()
			if admitted.Load() != 7 {
				t.Fatalf("admitted %d requests, want 7", admitted.Load())
			}
			balances(t, p, scope, 0, 70)
		})
	}
}

func TestIdempotentReservationAndSettlement(t *testing.T) {
	p, s, scope := testutil.Database(t)
	testutil.Accounts(t, s, scope, 100, 100)
	q := quote(scope, "same-request", 10)
	var created atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.Reserve(context.Background(), q)
			if err != nil {
				t.Error(err)
			}
			if ok {
				created.Add(1)
			}
		}()
	}
	wg.Wait()
	if created.Load() != 1 {
		t.Fatalf("created=%d", created.Load())
	}
	if err := s.Dispatch(context.Background(), q.RequestID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Settle(context.Background(), q.RequestID, 4); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	balances(t, p, scope, 4, 0)
	var events int
	if err := p.QueryRow(context.Background(), `SELECT count(*) FROM budget_outbox WHERE request_id=$1`, q.RequestID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 3 {
		t.Fatalf("outbox=%d, want reserve/dispatch/settle once each", events)
	}
	changed := q
	changed.Amount = 11
	if _, err := s.Reserve(context.Background(), changed); !errors.Is(err, budget.ErrConflict) {
		t.Fatalf("conflicting quote: %v", err)
	}
	changed = q
	changed.Organization = "other-org"
	if _, err := s.Reserve(context.Background(), changed); !errors.Is(err, budget.ErrConflict) {
		t.Fatalf("cross-organization collision: %v", err)
	}
	if err := s.Settle(context.Background(), q.RequestID, 5); !errors.Is(err, budget.ErrState) {
		t.Fatalf("conflicting settlement: %v", err)
	}
}

func TestSiblingProjectsShareOrganizationLimit(t *testing.T) {
	p, s, scope := testutil.Database(t)
	testutil.Accounts(t, s, scope, 100, 70)
	other := scope
	other.Project = "project-other"
	if err := s.CreateAccount(context.Background(), other, 70); err != nil {
		t.Fatal(err)
	}
	var admitted atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			chosen := scope
			if i%2 == 0 {
				chosen = other
			}
			created, err := budget.New(p).Reserve(context.Background(), quote(chosen, fmt.Sprintf("sibling-%d", i), 10))
			if errors.Is(err, budget.ErrExceeded) {
				return
			}
			if err != nil {
				t.Error(err)
				return
			}
			if created {
				admitted.Add(1)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if admitted.Load() != 10 {
		t.Fatalf("admitted=%d want 10", admitted.Load())
	}
	var organizationHeld, projectHeld int64
	if err := p.QueryRow(context.Background(), `SELECT held FROM budget_accounts WHERE project_id=''`).Scan(&organizationHeld); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(context.Background(), `SELECT sum(held) FROM budget_accounts WHERE project_id<>''`).Scan(&projectHeld); err != nil {
		t.Fatal(err)
	}
	if organizationHeld != 100 || projectHeld != organizationHeld {
		t.Fatalf("organization=%d projects=%d", organizationHeld, projectHeld)
	}
}

func TestTransitionsAndRecoveryRetainUncertainHolds(t *testing.T) {
	p, s, scope := testutil.Database(t)
	testutil.Accounts(t, s, scope, 100, 100)
	ctx := context.Background()
	for _, id := range []string{"in-flight", "not-dispatched"} {
		if _, err := s.Reserve(ctx, quote(scope, id, 10)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Dispatch(ctx, "in-flight"); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ctx, "in-flight"); !errors.Is(err, budget.ErrState) {
		t.Fatalf("must not refund dispatched request: %v", err)
	}
	// A new pool sees only persisted state, simulating process restart.
	reopened, err := pgxpool.NewWithConfig(ctx, p.Config())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted := budget.New(reopened)
	n, err := restarted.Recover(ctx, time.Now().Add(time.Second))
	if err != nil || n != 1 {
		t.Fatalf("recover=%d, %v", n, err)
	}
	balances(t, p, scope, 0, 20)
	r, err := restarted.Get(ctx, "in-flight")
	if err != nil || r.State != "uncertain" {
		t.Fatalf("state=%s err=%v", r.State, err)
	}
	if err := restarted.Settle(ctx, "in-flight", 11); !errors.Is(err, budget.ErrCostBound) {
		t.Fatalf("cost bound: %v", err)
	}
	balances(t, p, scope, 0, 20)
	if err := restarted.EstimateFinal(ctx, "in-flight"); err != nil {
		t.Fatal(err)
	}
	if err := restarted.EstimateFinal(ctx, "in-flight"); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Release(ctx, "not-dispatched"); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Release(ctx, "not-dispatched"); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Dispatch(ctx, "not-dispatched"); !errors.Is(err, budget.ErrState) {
		t.Fatalf("dispatch released: %v", err)
	}
	balances(t, p, scope, 10, 0)
}

func TestPeriodAndPriceStayBoundToReservation(t *testing.T) {
	p, s, scope := testutil.Database(t)
	testutil.Accounts(t, s, scope, 100, 100)
	q := quote(scope, "old-period", 10)
	ctx := context.Background()
	if _, err := s.Reserve(ctx, q); err != nil {
		t.Fatal(err)
	}
	next := scope
	next.Period = "2026-12"
	testutil.Accounts(t, s, next, 100, 100)
	changed := q
	changed.Period = next.Period
	if _, err := s.Reserve(ctx, changed); !errors.Is(err, budget.ErrConflict) {
		t.Fatal(err)
	}
	changed = q
	changed.PriceVersion = "mock-v2"
	if _, err := s.Reserve(ctx, changed); !errors.Is(err, budget.ErrConflict) {
		t.Fatal(err)
	}
	if err := s.Dispatch(ctx, q.RequestID); err != nil {
		t.Fatal(err)
	}
	if err := s.Settle(ctx, q.RequestID, 4); err != nil {
		t.Fatal(err)
	}
	balances(t, p, scope, 4, 0)
	balances(t, p, next, 0, 0)
}

func TestCanceledTransactionAndOutboxFailureRollBack(t *testing.T) {
	p, s, scope := testutil.Database(t)
	testutil.Accounts(t, s, scope, 100, 100)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Reserve(ctx, quote(scope, "cancelled", 10)); err == nil {
		t.Fatal("canceled operation succeeded")
	}
	// Force the transactional outbox insert to fail after the hold and row insert.
	if _, err := p.Exec(context.Background(), `ALTER TABLE budget_outbox ADD CONSTRAINT reject_reserved CHECK(event_type <> 'reserved')`); err != nil {
		t.Fatal(err)
	}
	if created, err := s.Reserve(context.Background(), quote(scope, "rollback", 10)); err == nil || created {
		t.Fatal("outbox failure must roll back reservation")
	}
	balances(t, p, scope, 0, 0)
	var rows int
	if err := p.QueryRow(context.Background(), `SELECT count(*) FROM budget_reservations`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("rolled back row count=%d", rows)
	}
}
