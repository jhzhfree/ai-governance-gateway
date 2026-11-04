package circuit_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jhzhfree/ai-governance-gateway/internal/circuit"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func acquire(t *testing.T, s *circuit.Store, key string) circuit.Permit {
	t.Helper()
	p, e := s.Acquire(context.Background(), key)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func complete(t *testing.T, p circuit.Permit, o circuit.Outcome) {
	t.Helper()
	if err := p.Complete(context.Background(), o); err != nil {
		t.Fatal(err)
	}
}
func TestSharedProbeFencingAndCrashRecovery(t *testing.T) {
	pool, _, _ := testutil.Database(t)
	ctx := context.Background()
	other, err := pgxpool.NewWithConfig(ctx, pool.Config().Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	stores := []*circuit.Store{circuit.New(pool), circuit.New(other)}
	key := "synthetic-route"
	late := acquire(t, stores[0], key)
	for i := 0; i < circuit.Threshold; i++ {
		complete(t, acquire(t, stores[i%2], key), circuit.Failure)
	}
	complete(t, late, circuit.Success) // Old completion must not close the newly opened circuit.
	if _, err := stores[1].Acquire(ctx, key); err == nil {
		t.Fatal("late success reopened circuit")
	} else {
		var denied *circuit.Denied
		if !errors.As(err, &denied) || denied.RetryAfter < 1 || denied.RetryAfter > 30 {
			t.Fatal("invalid circuit denial")
		}
	}
	complete(t, acquire(t, stores[1], "other-route"), circuit.Success)
	if _, err := pool.Exec(ctx, `UPDATE supplier_circuits SET retry_at=clock_timestamp()-interval '1 second' WHERE route_key=$1`, key); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	probes := make(chan circuit.Permit, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p, err := stores[i%2].Acquire(ctx, key)
			if err == nil {
				probes <- p
				return
			}
			var denied *circuit.Denied
			if !errors.As(err, &denied) {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	close(probes)
	if len(probes) != 1 {
		t.Fatal("multiple instances admitted concurrent probes")
	}
	old := <-probes
	if _, err := pool.Exec(ctx, `UPDATE supplier_circuits SET retry_at=clock_timestamp()-interval '1 second' WHERE route_key=$1`, key); err != nil {
		t.Fatal(err)
	}
	replacement := acquire(t, stores[1], key)
	complete(t, old, circuit.Success)
	if _, err := stores[0].Acquire(ctx, key); err == nil {
		t.Fatal("expired probe completion closed replacement probe")
	}
	complete(t, replacement, circuit.Success)
	complete(t, replacement, circuit.Failure) // Completion is idempotent.
	complete(t, acquire(t, stores[0], key), circuit.Neutral)
	var state string
	var failures int
	if err := pool.QueryRow(ctx, `SELECT state,failures FROM supplier_circuits WHERE route_key=$1`, key).Scan(&state, &failures); err != nil || state != "closed" || failures != 0 {
		t.Fatal("probe did not restore circuit")
	}
}
func TestNeutralAndSuccessClassificationAndReadOnlySnapshot(t *testing.T) {
	pool, _, _ := testutil.Database(t)
	ctx := context.Background()
	s := circuit.New(pool)
	key := "classification"
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := circuit.Read(ctx, tx, key, time.Now())
	if err != nil || snapshot.State != "unobserved" {
		t.Fatal("missing route misrepresented")
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM supplier_circuits`).Scan(&count); err != nil || count != 0 {
		t.Fatal("snapshot mutated state")
	}
	for i := 0; i < 4; i++ {
		complete(t, acquire(t, s, key), circuit.Failure)
	}
	complete(t, acquire(t, s, key), circuit.Neutral)
	complete(t, acquire(t, s, key), circuit.Success)
	for i := 0; i < 4; i++ {
		complete(t, acquire(t, s, key), circuit.Failure)
	}
	complete(t, acquire(t, s, key), circuit.Neutral)
	complete(t, acquire(t, s, key), circuit.Failure)
	if _, err := s.Acquire(ctx, key); err == nil {
		t.Fatal("threshold did not open circuit")
	}
	if _, err := pool.Exec(ctx, `UPDATE supplier_circuits SET retry_at=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	complete(t, acquire(t, s, key), circuit.Neutral)
	if _, err := s.Acquire(ctx, key); err == nil {
		t.Fatal("canceled probe claimed healthy")
	}
}
