package newapibridge

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
)

// Journal persists before network dispatch. Recovery never reads today's price.
type Journal struct{ pool *pgxpool.Pool }

func NewJournal(pool *pgxpool.Pool) *Journal { return &Journal{pool: pool} }
func (j *Journal) Bind(ctx context.Context, backend, model, requestID, attemptID, fingerprint string, route *provider.Route, maxOutput int) error {
	if j == nil || j.pool == nil || route == nil || !idPattern.MatchString(requestID) || !idPattern.MatchString(attemptID) || len(fingerprint) != 64 {
		return ErrInvalid
	}
	p := route.PriceSnapshot()
	_, upstream := route.ExecutionModel()
	if model != upstream || route.GovernanceChannelID() <= 0 {
		return ErrInvalid
	}
	if _, err := route.Bound(maxOutput); err != nil {
		return ErrInvalid
	}
	var readPrice, writePrice any
	if p.CachePricing != nil {
		readPrice = p.CachePricing.ReadPerMillion
		writePrice = p.CachePricing.WritePerMillion
	}
	result, err := j.pool.Exec(ctx, `INSERT INTO newapi_execution_bindings(attempt_id,request_id,backend,model,fingerprint,price_version,input_price,output_price,max_input,max_output,channel_id,cache_read_price,cache_write_price)
 SELECT a.attempt_id,a.request_id,$3,$4,$5,$6,$7,$8,$9,$10,$13,$14,$15 FROM inference_attempts a JOIN budget_reservations r ON r.request_id=a.request_id
 WHERE a.attempt_id=$1 AND a.request_id=$2 AND a.model=$11 AND a.outcome='unfinished' AND r.state='dispatched' AND r.price_version=$6 AND r.currency=$12
 ON CONFLICT DO NOTHING`, attemptID, requestID, backend, model, fingerprint, p.PriceVersion, p.InputPerMillion, p.OutputPerMillion, p.MaxInputTokens, maxOutput, p.ID, provider.Currency, route.GovernanceChannelID(), readPrice, writePrice)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrUnavailable
	}
	return nil
}

// Event is returned only by the authenticated data-plane outbox endpoint.
// Original field names match the new-api model's JSON contract.
type Event struct {
	CacheDetailsVersion                         int
	CacheReadTokens, CacheWriteTokens           int64
	AttemptID                                   string
	OwnerID                                     int
	RequestID, State, Model                     string
	ChannelID                                   int
	PromptTokens, CompletionTokens, TotalTokens int64
	CreatedAt, AcknowledgedAt                   int64
}

func (j *Journal) Apply(ctx context.Context, backend string, e Event) error {
	if j == nil || !idPattern.MatchString(e.AttemptID) || !idPattern.MatchString(e.RequestID) || (e.State != "completed" && e.State != "cancelled") || e.PromptTokens < 0 || e.CompletionTokens < 0 || e.PromptTokens > 1<<31-1 || e.CompletionTokens > 1<<31-1 || e.TotalTokens != e.PromptTokens+e.CompletionTokens {
		return ErrInvalid
	}
	if e.CacheDetailsVersion < 0 || e.CacheDetailsVersion > 1 || e.CacheReadTokens < 0 || e.CacheWriteTokens < 0 || e.CacheReadTokens > e.PromptTokens || e.CacheWriteTokens > e.PromptTokens-e.CacheReadTokens || (e.CacheDetailsVersion == 0 && (e.CacheReadTokens != 0 || e.CacheWriteTokens != 0)) {
		return ErrInvalid
	}
	return pgx.BeginFunc(ctx, j.pool, func(tx pgx.Tx) error {
		var requestID, model string
		var inPrice, outPrice, maxInput, maxOutput int64
		var channelID int
		var readPrice, writePrice pgtype.Int8
		if err := tx.QueryRow(ctx, `SELECT request_id,model,input_price,output_price,max_input,max_output,channel_id,cache_read_price,cache_write_price FROM newapi_execution_bindings WHERE attempt_id=$1 AND backend=$2 FOR UPDATE`, e.AttemptID, backend).Scan(&requestID, &model, &inPrice, &outPrice, &maxInput, &maxOutput, &channelID, &readPrice, &writePrice); err != nil {
			return err
		}
		if requestID != e.RequestID || e.PromptTokens > maxInput || e.CompletionTokens > maxOutput {
			return ErrInvalid
		}
		if e.State == "completed" && (e.Model != model || e.ChannelID != channelID || e.PromptTokens < 1) {
			return ErrInvalid
		}
		if e.State == "cancelled" && (e.CacheDetailsVersion != 0 || e.PromptTokens != 0 || e.CompletionTokens != 0 || e.ChannelID != 0 || e.Model != "") {
			return ErrInvalid
		}
		var cache *provider.CachePricing
		if readPrice.Valid != writePrice.Valid {
			return ErrInvalid
		}
		if readPrice.Valid {
			if e.State == "completed" && e.CacheDetailsVersion != 1 {
				return ErrInvalid
			}
			cache = &provider.CachePricing{ReadPerMillion: readPrice.Int64, WritePerMillion: writePrice.Int64}
		}
		actual, err := provider.TokenCost(e.PromptTokens, e.CompletionTokens, e.CacheReadTokens, e.CacheWriteTokens, inPrice, outPrice, cache)
		if err != nil {
			return ErrInvalid
		}

		var state string
		var input, output, oldActual, oldRead, oldWrite int64
		var oldVersion int
		err = tx.QueryRow(ctx, `SELECT state,input_tokens,output_tokens,actual,cache_details_version,cache_read_tokens,cache_write_tokens FROM newapi_execution_receipts WHERE attempt_id=$1`, e.AttemptID).Scan(&state, &input, &output, &oldActual, &oldVersion, &oldRead, &oldWrite)
		if err == nil {
			if state == e.State && input == e.PromptTokens && output == e.CompletionTokens && oldActual == actual && oldVersion == e.CacheDetailsVersion && oldRead == e.CacheReadTokens && oldWrite == e.CacheWriteTokens {
				return nil
			}
			return ErrInvalid
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := budget.ReconcileTx(ctx, tx, requestID, actual); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO newapi_execution_receipts(attempt_id,state,input_tokens,output_tokens,actual,cache_details_version,cache_read_tokens,cache_write_tokens) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, e.AttemptID, e.State, e.PromptTokens, e.CompletionTokens, actual, e.CacheDetailsVersion, e.CacheReadTokens, e.CacheWriteTokens)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE newapi_execution_bindings SET recovery_complete=true WHERE attempt_id=$1`, e.AttemptID)
		return err
	})
}

// RecoveryCandidate contains only persisted request identifiers and a digest.
type RecoveryCandidate struct{ AttemptID, RequestID, Fingerprint string }

// Stale rotates a bounded reconciliation batch fairly across replicas.
// The three-minute grace exceeds the executor's total 120-second request deadline.
func (j *Journal) Stale(ctx context.Context, backend string) ([]RecoveryCandidate, error) {
	rows, err := j.pool.Query(ctx, `WITH candidates AS (
 SELECT b.attempt_id FROM newapi_execution_bindings b JOIN budget_reservations r ON r.request_id=b.request_id
 WHERE b.backend=$1 AND NOT b.recovery_complete AND b.created_at<clock_timestamp()-interval '3 minutes' AND r.state IN ('dispatched','uncertain')
 AND NOT EXISTS(SELECT 1 FROM newapi_execution_receipts e WHERE e.attempt_id=b.attempt_id)
 ORDER BY b.recovery_checked_at,b.created_at,b.attempt_id LIMIT 100 FOR UPDATE OF b SKIP LOCKED)
 UPDATE newapi_execution_bindings b SET recovery_checked_at=clock_timestamp() FROM candidates c WHERE b.attempt_id=c.attempt_id
 RETURNING b.attempt_id,b.request_id,b.fingerprint`, backend)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := []RecoveryCandidate{}
	for rows.Next() {
		var item RecoveryCandidate
		if err := rows.Scan(&item.AttemptID, &item.RequestID, &item.Fingerprint); err != nil {
			return nil, err
		}
		results = append(results, item)
	}
	return results, rows.Err()
}
