package admission_test

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jhzhfree/ai-governance-gateway/internal/admission"
	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/execution"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func project(t *testing.T) (*pgxpool.Pool, *control.Store, identity.Principal, control.Organization, control.Project) {
	t.Helper()
	pool, _, _ := testutil.Database(t)
	store := control.New(pool)
	owner := identity.Principal{Issuer: "https://fixture.example", Subject: "owner"}
	org, err := store.CreateOrganization(context.Background(), owner, "Admission fixture", "2026-11", 10000)
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.CreateProject(context.Background(), owner, org.ID, "Project", 5000)
	if err != nil {
		t.Fatal(err)
	}
	return pool, store, owner, org, p
}
func TestConcurrencyAcrossIndependentPoolsAndProjects(t *testing.T) {
	pool, store, owner, org, p := project(t)
	ctx := context.Background()
	if _, err := store.SetLimits(ctx, owner, org.ID, p.ID, admission.Policy{RequestsPerMinute: 100, MaxConcurrent: 3}); err != nil {
		t.Fatal(err)
	}
	other, err := pgxpool.NewWithConfig(ctx, pool.Config().Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	gates := []*admission.Store{admission.New(pool), admission.New(other)}
	var wg sync.WaitGroup
	permits := make(chan admission.Permit, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			permit, err := gates[i%2].Acquire(ctx, org.ID, p.ID, rand.Text())
			if err == nil {
				permits <- permit
				return
			}
			var denied *admission.Denied
			if !errors.As(err, &denied) || denied.Code != "project_concurrency_limited" || denied.RetryAfter < 1 {
				t.Error("unexpected distributed admission result", err)
			}
		}(i)
	}
	wg.Wait()
	close(permits)
	if len(permits) != 3 {
		t.Fatal("shared project concurrency exceeded or under-admitted")
	}
	held := []admission.Permit{}
	for permit := range permits {
		held = append(held, permit)
	}
	if err := held[0].Release(ctx); err != nil {
		t.Fatal(err)
	}
	replacement, err := gates[1].Acquire(ctx, org.ID, p.ID, rand.Text())
	if err != nil {
		t.Fatal("released slot not reusable")
	}
	defer replacement.Release(ctx)
	if err := held[0].Release(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Limits(ctx, owner, org.ID, p.ID)
	if err != nil || snapshot.ActiveRequests != 3 {
		t.Fatal("late duplicate release removed another permit")
	}
	second, err := store.CreateProject(ctx, owner, org.ID, "Other project", 1000)
	if err != nil {
		t.Fatal(err)
	}
	independent, err := gates[0].Acquire(ctx, org.ID, second.ID, rand.Text())
	if err != nil {
		t.Fatal("another project's permits leaked into quota")
	}
	independent.Release(ctx)
	for _, permit := range held[1:] {
		permit.Release(ctx)
	}
}
func TestSlidingWindowAndPolicyRevision(t *testing.T) {
	pool, store, owner, org, p := project(t)
	ctx := context.Background()
	gate := admission.New(pool)
	if _, err := store.SetLimits(ctx, owner, org.ID, p.ID, admission.Policy{RequestsPerMinute: 3, MaxConcurrent: 4}); err != nil {
		t.Fatal(err)
	}
	first := ""
	for i := 0; i < 3; i++ {
		id := rand.Text()
		if i == 0 {
			first = id
		}
		permit, err := gate.Acquire(ctx, org.ID, p.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		permit.Release(ctx)
	}
	_, err := gate.Acquire(ctx, org.ID, p.ID, rand.Text())
	var denied *admission.Denied
	if !errors.As(err, &denied) || denied.Code != "project_rate_limited" || denied.RetryAfter < 1 || denied.RetryAfter > 60 {
		t.Fatal("rolling window did not deny fourth admission")
	}
	if _, err := pool.Exec(ctx, `UPDATE admission_events SET admitted_at=clock_timestamp()-interval '61 seconds' WHERE request_id=$1`, first); err != nil {
		t.Fatal(err)
	}
	permit, err := gate.Acquire(ctx, org.ID, p.ID, rand.Text())
	if err != nil {
		t.Fatal("expired event still consumed rate quota")
	}
	defer permit.Release(ctx)
	if _, err := store.SetLimits(ctx, owner, org.ID, p.ID, admission.Policy{RequestsPerMinute: 100, MaxConcurrent: 4, Revision: 0}); !errors.Is(err, admission.ErrConflict) {
		t.Fatal("stale revision overwrote policy")
	}
}
func TestExpiredLeaseRestoresAdmissionWithoutRefundOrReplay(t *testing.T) {
	pool, store, owner, org, p := project(t)
	ctx := context.Background()
	gate := admission.New(pool)
	if _, err := store.SetLimits(ctx, owner, org.ID, p.ID, admission.Policy{RequestsPerMinute: 100, MaxConcurrent: 1}); err != nil {
		t.Fatal(err)
	}
	id := rand.Text()
	old, err := gate.Acquire(ctx, org.ID, p.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	ledger := budget.New(pool)
	scope := budget.Scope{Organization: org.ID, Project: p.ID, Period: org.Period, Currency: org.Currency}
	if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: id, PriceVersion: "mock-v1", Amount: 10}); err != nil {
		t.Fatal(err)
	}
	recorder := execution.New(pool)
	if err := recorder.Begin(ctx, execution.Start{AttemptID: rand.Text(), RequestID: id, Model: "mock-text", Provider: "mock", Region: "local", Stream: true}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Dispatch(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE admission_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE request_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	next, err := gate.Acquire(ctx, org.ID, p.ID, rand.Text())
	if err != nil {
		t.Fatal("crashed worker's expired lease blocked future admission")
	}
	defer next.Release(ctx)
	old.Release(ctx)
	v, err := store.Limits(ctx, owner, org.ID, p.ID)
	if err != nil || v.ActiveRequests != 1 {
		t.Fatal("stale release removed new lease")
	}
	r, err := ledger.Get(ctx, id)
	if err != nil || r.State != "dispatched" || r.Amount != 10 || r.Actual != nil {
		t.Fatal("lease expiration changed financial facts")
	}
	var outcome string
	if err := pool.QueryRow(ctx, `SELECT outcome FROM inference_attempts WHERE request_id=$1`, id).Scan(&outcome); err != nil || outcome != "unfinished" {
		t.Fatal("lease expiration fabricated execution completion")
	}
}
