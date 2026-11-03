// Package admission implements shared project quotas in PostgreSQL.
package admission

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const MaxRequestDuration = 120 * time.Second
const LeaseDuration = 180 * time.Second

var ErrInvalid = errors.New("invalid project limits")
var ErrConflict = errors.New("project limits revision changed")

type Policy struct {
	RequestsPerMinute int   `json:"requests_per_minute"`
	MaxConcurrent     int   `json:"max_concurrent"`
	Revision          int64 `json:"revision"`
}

func DefaultPolicy() Policy { return Policy{RequestsPerMinute: 60, MaxConcurrent: 4} }
func (p Policy) Valid() bool {
	return p.RequestsPerMinute >= 1 && p.RequestsPerMinute <= 10000 && p.MaxConcurrent >= 1 && p.MaxConcurrent <= 64 && p.Revision >= 0 && p.Revision < 9007199254740991
}

type Snapshot struct {
	Policy
	ActiveRequests int       `json:"active_requests"`
	RecentRequests int       `json:"recent_requests"`
	ObservedAt     time.Time `json:"observed_at"`
}
type Denied struct {
	Code       string
	RetryAfter int
}

func (d *Denied) Error() string { return d.Code }

type Permit interface{ Release(context.Context) error }
type Gate interface {
	Acquire(context.Context, string, string, string) (Permit, error)
}
type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool} }
func lock(ctx context.Context, tx pgx.Tx, org, project string) error {
	key, _ := json.Marshal([]string{org, project})
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "aigw:admission:"+string(key))
	return err
}
func Read(ctx context.Context, tx pgx.Tx, org, project string, at time.Time) (v Snapshot, err error) {
	v.Policy = DefaultPolicy()
	v.ObservedAt = at
	err = tx.QueryRow(ctx, `SELECT requests_per_minute,max_concurrent,revision FROM project_limits WHERE organization_id=$1 AND project_id=$2`, org, project).Scan(&v.RequestsPerMinute, &v.MaxConcurrent, &v.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return
	}
	err = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM admission_leases WHERE organization_id=$1 AND project_id=$2 AND expires_at>$3),(SELECT count(*) FROM admission_events WHERE organization_id=$1 AND project_id=$2 AND admitted_at>$3::timestamptz-interval '60 seconds')`, org, project, at).Scan(&v.ActiveRequests, &v.RecentRequests)
	return
}

// Update uses the same project lock as admission; callers own role checks/audit.
func Update(ctx context.Context, tx pgx.Tx, org, project string, p Policy) error {
	if !p.Valid() {
		return ErrInvalid
	}
	if err := lock(ctx, tx, org, project); err != nil {
		return err
	}
	current := DefaultPolicy()
	err := tx.QueryRow(ctx, `SELECT requests_per_minute,max_concurrent,revision FROM project_limits WHERE organization_id=$1 AND project_id=$2`, org, project).Scan(&current.RequestsPerMinute, &current.MaxConcurrent, &current.Revision)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if current.Revision != p.Revision {
		return ErrConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO project_limits(organization_id,project_id,requests_per_minute,max_concurrent,revision) VALUES($1,$2,$3,$4,$5) ON CONFLICT(organization_id,project_id) DO UPDATE SET requests_per_minute=excluded.requests_per_minute,max_concurrent=excluded.max_concurrent,revision=excluded.revision`, org, project, p.RequestsPerMinute, p.MaxConcurrent, p.Revision+1)
	return err
}

type lease struct {
	pool *pgxpool.Pool
	id   string
}

func (l lease) Release(ctx context.Context) error {
	_, err := l.pool.Exec(ctx, `DELETE FROM admission_leases WHERE lease_id=$1`, l.id)
	return err
}
func (s *Store) Acquire(ctx context.Context, org, project, request string) (permit Permit, err error) {
	if org == "" || project == "" || request == "" {
		return nil, ErrInvalid
	}
	id := rand.Text()
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lock(ctx, tx, org, project); err != nil {
			return err
		}
		p := DefaultPolicy()
		if _, err := tx.Exec(ctx, `INSERT INTO project_limits(organization_id,project_id,requests_per_minute,max_concurrent) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, org, project, p.RequestsPerMinute, p.MaxConcurrent); err != nil {
			return err
		}
		var now time.Time
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return err
		}
		v, err := Read(ctx, tx, org, project, now)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM admission_events WHERE organization_id=$1 AND project_id=$2 AND admitted_at<=$3::timestamptz-interval '60 seconds'`, org, project, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM admission_leases WHERE organization_id=$1 AND project_id=$2 AND expires_at<=$3`, org, project, now); err != nil {
			return err
		}
		if v.ActiveRequests >= v.MaxConcurrent {
			var earliest time.Time
			if err := tx.QueryRow(ctx, `SELECT min(expires_at) FROM admission_leases WHERE organization_id=$1 AND project_id=$2 AND expires_at>$3`, org, project, now).Scan(&earliest); err != nil {
				return err
			}
			return &Denied{Code: "project_concurrency_limited", RetryAfter: max(1, int(math.Ceil(earliest.Sub(now).Seconds())))}
		}
		if v.RecentRequests >= v.RequestsPerMinute {
			var boundary time.Time
			if err := tx.QueryRow(ctx, `SELECT admitted_at FROM admission_events WHERE organization_id=$1 AND project_id=$2 AND admitted_at>$3::timestamptz-interval '60 seconds' ORDER BY admitted_at DESC OFFSET $4 LIMIT 1`, org, project, now, v.RequestsPerMinute-1).Scan(&boundary); err != nil {
				return err
			}
			return &Denied{Code: "project_rate_limited", RetryAfter: max(1, int(math.Ceil(boundary.Add(time.Minute).Sub(now).Seconds())))}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO admission_events VALUES($1,$2,$3,$4)`, request, org, project, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO admission_leases VALUES($1,$2,$3,$4,$5)`, id, request, org, project, now.Add(LeaseDuration)); err != nil {
			return err
		}
		return nil
	})
	if err == nil {
		permit = lease{s.pool, id}
	}
	return
}
