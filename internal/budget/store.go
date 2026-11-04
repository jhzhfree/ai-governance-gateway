// Package budget implements the PostgreSQL budget consistency prototype.
// Amounts use an explicit account currency and integer minor units, never floats.
package budget

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrExceeded  = errors.New("budget exceeded")
	ErrConflict  = errors.New("request conflicts with existing reservation")
	ErrState     = errors.New("invalid reservation transition")
	ErrCostBound = errors.New("actual cost exceeds reserved bound")
)

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

type Scope struct {
	Organization, Project, Period, Currency string
}

type Quote struct {
	Scope
	RequestID, PriceVersion string
	Amount                  int64
}

type Reservation struct {
	Quote
	State  string
	Actual *int64
}

func (q Quote) valid() bool {
	return q.Organization != "" && q.Project != "" && q.Period != "" && q.Currency != "" &&
		q.RequestID != "" && q.PriceVersion != "" && q.Amount > 0
}

// CreateAccount is deliberately create-only: setup must not reset a live budget.
// Pass an empty project for the organization account.
func (s *Store) CreateAccount(ctx context.Context, scope Scope, limit int64) error {
	if scope.Organization == "" || scope.Period == "" || scope.Currency == "" || limit < 0 {
		return errors.New("invalid account")
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO budget_accounts
		(organization_id,project_id,period_id,currency,limit_amount) VALUES ($1,$2,$3,$4,$5)`,
		scope.Organization, scope.Project, scope.Period, scope.Currency, limit)
	return err
}

func (s *Store) transaction(ctx context.Context, id string, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// All state mutations serialize per request. Hash collisions only serialize
		// unrelated requests; they cannot weaken the accounting constraints.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, id); err != nil {
			return err
		}
		return fn(tx)
	})
}

func read(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id string) (Reservation, error) {
	var r Reservation
	err := db.QueryRow(ctx, `SELECT request_id,organization_id,project_id,period_id,currency,
		price_version,reserved,state,actual FROM budget_reservations WHERE request_id=$1`, id).Scan(
		&r.RequestID, &r.Organization, &r.Project, &r.Period, &r.Currency, &r.PriceVersion, &r.Amount, &r.State, &r.Actual)
	return r, err
}

func (s *Store) Get(ctx context.Context, id string) (Reservation, error) {
	return read(ctx, s.pool, id)
}

func lockAccounts(ctx context.Context, tx pgx.Tx, scope Scope) error {
	rows, err := tx.Query(ctx, `SELECT project_id FROM budget_accounts
		WHERE organization_id=$1 AND project_id IN ('',$2) AND period_id=$3 AND currency=$4
		ORDER BY project_id FOR UPDATE`, scope.Organization, scope.Project, scope.Period, scope.Currency)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != 2 {
		return errors.New("organization and project accounts must both exist")
	}
	return nil
}

func event(ctx context.Context, tx pgx.Tx, id, state string) error {
	_, err := tx.Exec(ctx, `INSERT INTO budget_outbox(request_id,event_type) VALUES($1,$2)`, id, state)
	return err
}

// Reserve atomically holds the same amount against the organization and project.
// created=false is an idempotent replay, never permission to dispatch again.
func (s *Store) Reserve(ctx context.Context, q Quote) (created bool, err error) {
	if !q.valid() {
		return false, errors.New("invalid quote")
	}
	err = s.transaction(ctx, q.RequestID, func(tx pgx.Tx) error {
		existing, err := read(ctx, tx, q.RequestID)
		if err == nil {
			if existing.Quote != q {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := lockAccounts(ctx, tx, q.Scope); err != nil {
			return err
		}
		result, err := tx.Exec(ctx, `UPDATE budget_accounts SET held=held+$5
			WHERE organization_id=$1 AND project_id IN ('',$2) AND period_id=$3 AND currency=$4
			AND $5 <= limit_amount-spent-held`, q.Organization, q.Project, q.Period, q.Currency, q.Amount)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 2 {
			return ErrExceeded
		} // transaction rolls back either update
		_, err = tx.Exec(ctx, `INSERT INTO budget_reservations
			(request_id,organization_id,project_id,period_id,currency,price_version,reserved,state)
			VALUES($1,$2,$3,$4,$5,$6,$7,'reserved')`, q.RequestID, q.Organization, q.Project, q.Period, q.Currency, q.PriceVersion, q.Amount)
		if err != nil {
			return err
		}
		created = true
		return event(ctx, tx, q.RequestID, "reserved")
	})
	if err != nil {
		created = false
	}
	return
}

func (s *Store) change(ctx context.Context, id, target string) error {
	return s.transaction(ctx, id, func(tx pgx.Tx) error {
		r, err := read(ctx, tx, id)
		if err != nil {
			return err
		}
		if r.State == target {
			return nil
		}
		if (target == "dispatched" && r.State != "reserved") ||
			(target == "uncertain" && r.State != "dispatched") {
			return ErrState
		}
		if _, err := tx.Exec(ctx, `UPDATE budget_reservations SET state=$2,updated_at=clock_timestamp() WHERE request_id=$1`, id, target); err != nil {
			return err
		}
		return event(ctx, tx, id, target)
	})
}

func (s *Store) Dispatch(ctx context.Context, id string) error {
	return s.change(ctx, id, "dispatched")
}
func (s *Store) MarkUncertain(ctx context.Context, id string) error {
	return s.change(ctx, id, "uncertain")
}

func (s *Store) finish(ctx context.Context, id, target string, actual int64) error {
	return s.transaction(ctx, id, func(tx pgx.Tx) error { return finishTx(ctx, tx, id, target, actual) })
}

// ReconcileTx participates in the caller's evidence/review/audit transaction.
// The caller must authenticate reviewers and verify scope and final evidence.
func ReconcileTx(ctx context.Context, tx pgx.Tx, id string, actual int64) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, id); err != nil {
		return err
	}
	return finishTx(ctx, tx, id, "settled", actual)
}
func finishTx(ctx context.Context, tx pgx.Tx, id, target string, actual int64) error {
	r, err := read(ctx, tx, id)
	if err != nil {
		return err
	}
	if actual < 0 || actual > r.Amount {
		return ErrCostBound
	}
	if r.State == target && r.Actual != nil && *r.Actual == actual {
		return nil
	}
	if (target == "released" && r.State != "reserved") ||
		(target != "released" && r.State != "dispatched" && r.State != "uncertain") {
		return ErrState
	}
	if err := lockAccounts(ctx, tx, r.Scope); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE budget_accounts SET held=held-$5,spent=spent+$6
		WHERE organization_id=$1 AND project_id IN ('',$2) AND period_id=$3 AND currency=$4 AND held >= $5`,
		r.Organization, r.Project, r.Period, r.Currency, r.Amount, actual)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 2 {
		return errors.New("budget ledger inconsistent")
	}
	if _, err := tx.Exec(ctx, `UPDATE budget_reservations SET state=$2,actual=$3,updated_at=clock_timestamp() WHERE request_id=$1`, id, target, actual); err != nil {
		return err
	}
	return event(ctx, tx, id, target)
}

func (s *Store) Settle(ctx context.Context, id string, actual int64) error {
	return s.finish(ctx, id, "settled", actual)
}
func (s *Store) Release(ctx context.Context, id string) error {
	return s.finish(ctx, id, "released", 0)
}

// EstimateFinal is an explicit administrative decision after reconciliation,
// never an automatic timeout refund. It retains the full conservative charge.
func (s *Store) EstimateFinal(ctx context.Context, id string) error {
	r, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if r.State != "uncertain" && r.State != "estimated_final" {
		return ErrState
	}
	return s.finish(ctx, id, "estimated_final", r.Amount)
}

// Recover marks stale dispatches as uncertain, retaining their holds. Even a
// live slow request may be marked uncertain; later trusted settlement is valid.
// Reserved rows are NOT refunded: proving no dispatcher exists requires leases.
func (s *Store) Recover(ctx context.Context, before time.Time) (int, error) {
	rows, err := s.pool.Query(ctx, `SELECT request_id FROM budget_reservations WHERE state='dispatched' AND updated_at<$1`, before)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		if err := s.MarkUncertain(ctx, id); err != nil {
			if errors.Is(err, ErrState) {
				continue
			} // concurrently settled
			return n, fmt.Errorf("recover reservation: %w", err)
		}
		n++
	}
	return n, nil
}
