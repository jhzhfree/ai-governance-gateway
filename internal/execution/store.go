// Package execution persists bounded metadata without prompts or credentials.
package execution

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
)

var ErrInvalid = errors.New("invalid execution metadata")
var ErrConflict = errors.New("execution metadata already finalized or conflicting")

type Start struct {
	AttemptID string `json:"attempt_id"`
	RequestID string `json:"request_id"`
	Model     string `json:"model"`
	Provider  string `json:"provider"`
	Region    string `json:"region"`
	Stream    bool   `json:"stream"`
}
type Result struct {
	Outcome        string `json:"outcome"`
	ErrorCode      string `json:"error_code"`
	UpstreamStatus *int   `json:"upstream_status"`
	InputTokens    *int64 `json:"input_tokens"`
	OutputTokens   *int64 `json:"output_tokens"`
}
type Attempt struct {
	Start
	Result
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	DurationMS *int64     `json:"duration_ms"`
}
type Recorder interface {
	Begin(context.Context, Start) error
	Finish(context.Context, string, Result) error
}
type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool} }

var code = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
var model = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]{0,127}$`)

func ValidOutcome(v string) bool {
	switch v {
	case "", "unfinished", "completed", "failed", "canceled":
		return true
	}
	return false
}
func (s Start) valid() bool {
	if !model.MatchString(s.AttemptID) || !model.MatchString(s.RequestID) || !model.MatchString(s.Model) {
		return false
	}
	switch s.Provider {
	case "mock":
		return s.Region == "local"
	case "aliyun", "volcengine":
		return s.Region == "cn-beijing"
	default:
		return provider.ValidSupplierID(s.Provider) && s.Region == "cn-mainland"
	}
}
func (r Result) valid() bool {
	if !ValidOutcome(r.Outcome) || r.Outcome == "" || r.Outcome == "unfinished" || (r.Outcome == "completed" && r.ErrorCode != "") || (r.Outcome != "completed" && !code.MatchString(r.ErrorCode)) {
		return false
	}
	if r.Outcome == "completed" && (r.UpstreamStatus == nil || *r.UpstreamStatus != 200 || r.InputTokens == nil) {
		return false
	}
	return (r.UpstreamStatus == nil || (*r.UpstreamStatus >= 100 && *r.UpstreamStatus <= 599)) && (r.InputTokens == nil) == (r.OutputTokens == nil) && (r.InputTokens == nil || (*r.InputTokens >= 0 && *r.OutputTokens >= 0 && *r.InputTokens <= 9007199254740991 && *r.OutputTokens <= 9007199254740991))
}
func (s *Store) Begin(ctx context.Context, v Start) error {
	if !v.valid() {
		return ErrInvalid
	}
	result, err := s.pool.Exec(ctx, `INSERT INTO inference_attempts(attempt_id,request_id,model,provider,region,stream) SELECT $1,$2,$3,$4,$5,$6 FROM budget_reservations WHERE request_id=$2 AND state='reserved' ON CONFLICT DO NOTHING`, v.AttemptID, v.RequestID, v.Model, v.Provider, v.Region, v.Stream)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
func equal[T comparable](a, b *T) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}
func (s *Store) Finish(ctx context.Context, id string, v Result) error {
	if !v.valid() {
		return ErrInvalid
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var current Result
		err := tx.QueryRow(ctx, `SELECT outcome,error_code,upstream_status,input_tokens,output_tokens FROM inference_attempts WHERE attempt_id=$1 FOR UPDATE`, id).Scan(&current.Outcome, &current.ErrorCode, &current.UpstreamStatus, &current.InputTokens, &current.OutputTokens)
		if err != nil {
			return err
		}
		if current.Outcome != "unfinished" {
			if current.Outcome == v.Outcome && current.ErrorCode == v.ErrorCode && equal(current.UpstreamStatus, v.UpstreamStatus) && equal(current.InputTokens, v.InputTokens) && equal(current.OutputTokens, v.OutputTokens) {
				return nil
			}
			return ErrConflict
		}
		if v.Outcome == "completed" {
			var settled bool
			if err := tx.QueryRow(ctx, `SELECT r.state='settled' FROM budget_reservations r JOIN inference_attempts a ON a.request_id=r.request_id WHERE a.attempt_id=$1`, id).Scan(&settled); err != nil {
				return err
			}
			if !settled {
				return ErrInvalid
			}
		}
		_, err = tx.Exec(ctx, `WITH finish AS MATERIALIZED (SELECT clock_timestamp() AS at) UPDATE inference_attempts a SET outcome=$2,error_code=$3,upstream_status=$4,input_tokens=$5,output_tokens=$6,finished_at=GREATEST(f.at,a.started_at),duration_ms=GREATEST(0,FLOOR(EXTRACT(EPOCH FROM (f.at-a.started_at))*1000)::bigint) FROM finish f WHERE a.attempt_id=$1`, id, v.Outcome, v.ErrorCode, v.UpstreamStatus, v.InputTokens, v.OutputTokens)
		return err
	})
}
