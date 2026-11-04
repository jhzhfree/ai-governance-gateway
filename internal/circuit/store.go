// Package circuit isolates supplier routes across gateway instances without replay.
package circuit

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const Threshold = 5
const Cooldown = 30 * time.Second
const ProbeLease = 180 * time.Second

type Outcome int

const (
	Neutral Outcome = iota
	Success
	Failure
)

type Permit interface {
	Complete(context.Context, Outcome) error
}
type Gate interface {
	Acquire(context.Context, string) (Permit, error)
}
type Denied struct{ RetryAfter int }

func (d *Denied) Error() string { return "supplier circuit open" }

type Snapshot struct {
	State      string `json:"state"`
	RetryAfter int    `json:"retry_after"`
}
type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool} }
func Read(ctx context.Context, tx pgx.Tx, key string, at time.Time) (v Snapshot, err error) {
	var until *time.Time
	err = tx.QueryRow(ctx, `SELECT state,retry_at FROM supplier_circuits WHERE route_key=$1`, key).Scan(&v.State, &until)
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{State: "unobserved"}, nil
	}
	if until != nil {
		v.RetryAfter = max(0, int(math.Ceil(until.Sub(at).Seconds())))
	}
	return
}

type permit struct {
	store      *Store
	key        string
	generation int64
	done       atomic.Bool
}

func (p *permit) Complete(ctx context.Context, outcome Outcome) error {
	if p.done.Swap(true) {
		return nil
	}
	return pgx.BeginFunc(ctx, p.store.pool, func(tx pgx.Tx) error {
		var state string
		var generation int64
		var failures int
		if err := tx.QueryRow(ctx, `SELECT state,generation,failures FROM supplier_circuits WHERE route_key=$1 FOR UPDATE`, p.key).Scan(&state, &generation, &failures); err != nil {
			return err
		}
		if generation != p.generation || state == "open" {
			return nil
		}
		switch {
		case outcome == Success:
			if state == "half_open" {
				generation++
			}
			state = "closed"
			failures = 0
		case outcome == Failure:
			failures = min(Threshold, failures+1)
			if state == "half_open" || failures >= Threshold {
				state = "open"
				generation++
			}
		case state == "half_open":
			// A canceled or locally rejected probe is not evidence of health.
			state = "open"
			generation++
		default:
			return nil
		}
		_, err := tx.Exec(ctx, `UPDATE supplier_circuits SET state=$2,generation=$3,failures=$4,retry_at=CASE WHEN $2='closed' THEN NULL ELSE clock_timestamp()+$5::double precision*interval '1 second' END,updated_at=clock_timestamp() WHERE route_key=$1`, p.key, state, generation, failures, Cooldown.Seconds())
		return err
	})
}
func (s *Store) Acquire(ctx context.Context, key string) (Permit, error) {
	if key == "" || len(key) > 512 {
		return nil, errors.New("invalid circuit identity")
	}
	p := &permit{store: s, key: key}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO supplier_circuits(route_key,state) VALUES($1,'closed') ON CONFLICT DO NOTHING`, key); err != nil {
			return err
		}
		var state string
		var until *time.Time
		var now time.Time
		if err := tx.QueryRow(ctx, `SELECT state,generation,retry_at,clock_timestamp() FROM supplier_circuits WHERE route_key=$1 FOR UPDATE`, key).Scan(&state, &p.generation, &until, &now); err != nil {
			return err
		}
		if state == "closed" {
			return nil
		}
		// Read the clock after obtaining the row lock, not before waiting for it.
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return err
		}
		if until.After(now) {
			return &Denied{max(1, int(math.Ceil(until.Sub(now).Seconds())))}
		}
		p.generation++
		_, err := tx.Exec(ctx, `UPDATE supplier_circuits SET state='half_open',generation=$2,retry_at=clock_timestamp()+$3::double precision*interval '1 second',updated_at=clock_timestamp() WHERE route_key=$1`, key, p.generation, ProbeLease.Seconds())
		return err
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}
