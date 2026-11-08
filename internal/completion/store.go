package completion

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
)

var ErrInvalid = errors.New("invalid embedded completion evidence")
var ErrConflict = errors.New("embedded completion conflict")
var idPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{7,127}$`)

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// RouteDigest identifies the non-secret tariff/route snapshot. Credential-derived
// ExecutionIdentity is strictly in-memory and must never enter durable evidence.
func RouteDigest(route *provider.Route) [32]byte { return sha256.Sum256([]byte(route.PriceVersion())) }

// Bind is a once-only pre-dispatch fence. A duplicate must never authorize replay.
func (s *Store) Bind(ctx context.Context, route *provider.Route, req provider.Request, requestID, attemptID string) error {
	if s == nil || s.pool == nil || route == nil || !route.Ready() || !idPattern.MatchString(requestID) || !idPattern.MatchString(attemptID) || req.HasTools() || req.HasParts() || (!req.IsEmbedding() && req.MaxTokens < 16) || !provider.ValidateRequest(req) {
		return ErrInvalid
	}
	p := route.PriceSnapshot()
	alias, upstream := route.ExecutionModel()
	revision := RouteDigest(route)
	if req.IsEmbedding() != (route.API() == "embeddings") || alias != req.Model || (p.CachePricing != nil && !route.UsesEmbeddedCacheEvidence()) || route.GovernanceChannelID() != 0 {
		return ErrInvalid
	}
	if _, err := route.Bound(req.MaxTokens); err != nil {
		return ErrInvalid
	}
	if route.UsesNativeMessages() && provider.ValidateEmbeddedMessages(req) != nil {
		return ErrInvalid
	}
	protocol := Protocol(route)
	var cachePrice *int64
	if route.UsesEmbeddedCacheEvidence() {
		cachePrice = &p.CachePricing.ReadPerMillion
	}
	r, err := s.pool.Exec(ctx, `INSERT INTO engine_execution_bindings(attempt_id,request_id,engine,protocol,model,upstream_model,supplier,route_digest,price_version,input_price,output_price,max_input,max_output,stream,cache_read_price)
 SELECT a.attempt_id,a.request_id,'bifrost',$14,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$15 FROM inference_attempts a JOIN budget_reservations r ON r.request_id=a.request_id
 WHERE a.attempt_id=$1 AND a.request_id=$2 AND a.model=$3 AND a.provider=$5 AND a.stream=$12 AND a.outcome='unfinished' AND r.state='dispatched' AND r.price_version=$7 AND r.currency=$13
 ON CONFLICT DO NOTHING`, attemptID, requestID, alias, upstream, route.Provider(), revision[:], p.PriceVersion, p.InputPerMillion, p.OutputPerMillion, p.MaxInputTokens, req.MaxTokens, req.Stream, provider.Currency, protocol, cachePrice)
	if err != nil {
		return err
	}
	if r.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

// Record persists only a completed adapter protocol with original numeric usage.
// Callers must establish completion before invoking it, independently of delivery.
func (s *Store) Record(ctx context.Context, requestID, attemptID string, revision [32]byte, raw []byte) error {
	if s == nil || s.pool == nil || !idPattern.MatchString(requestID) || !idPattern.MatchString(attemptID) {
		return ErrInvalid
	}
	sum := sha256.Sum256(raw)
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id, protocol string
		var digest []byte
		var maxIn, maxOut int64
		if err := tx.QueryRow(ctx, `SELECT request_id,route_digest,max_input,max_output,protocol FROM engine_execution_bindings WHERE attempt_id=$1 FOR UPDATE`, attemptID).Scan(&id, &digest, &maxIn, &maxOut, &protocol); err != nil {
			return err
		}
		c, err := ParseUsage(protocol, raw)
		if err != nil {
			return err
		}
		if id != requestID || !bytes.Equal(digest, revision[:]) || c.Input > maxIn || c.Output > maxOut {
			return ErrInvalid
		}
		var old []byte
		err = tx.QueryRow(ctx, `SELECT raw_usage FROM engine_completion_events WHERE attempt_id=$1`, attemptID).Scan(&old)
		if err == nil {
			if bytes.Equal(old, raw) {
				return nil
			}
			return ErrConflict
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var read, uncached, write *int64
		if c.Cache != nil {
			read, uncached, write = &c.Cache.Read, &c.Cache.Uncached, c.Cache.Creation
		}
		_, err = tx.Exec(ctx, `INSERT INTO engine_completion_events(attempt_id,raw_usage,usage_sha256,input_tokens,output_tokens,cache_read_tokens,uncached_input_tokens,cache_write_tokens) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, attemptID, raw, sum[:], c.Input, c.Output, read, uncached, write)
		return err
	})
}

// Settle verifies the live handler's computed amount against the immutable price.
func (s *Store) Settle(ctx context.Context, requestID, attemptID string, actual int64) error {
	return s.apply(ctx, requestID, attemptID, &actual)
}
func (s *Store) apply(ctx context.Context, requestID, attemptID string, expected *int64) error {
	if s == nil || s.pool == nil || !idPattern.MatchString(requestID) || !idPattern.MatchString(attemptID) {
		return ErrInvalid
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id, protocol string
		var inPrice, outPrice, maxIn, maxOut, input, output int64
		var raw, hash []byte
		var cachePrice, read, uncached, write *int64
		if err := tx.QueryRow(ctx, `SELECT b.request_id,b.input_price,b.output_price,b.max_input,b.max_output,e.raw_usage,e.usage_sha256,e.input_tokens,e.output_tokens,b.protocol,b.cache_read_price,e.cache_read_tokens,e.uncached_input_tokens,e.cache_write_tokens FROM engine_execution_bindings b JOIN engine_completion_events e USING(attempt_id) WHERE b.attempt_id=$1 FOR UPDATE OF b`, attemptID).Scan(&id, &inPrice, &outPrice, &maxIn, &maxOut, &raw, &hash, &input, &output, &protocol, &cachePrice, &read, &uncached, &write); err != nil {
			return err
		}
		c, err := ParseUsage(protocol, raw)
		sum := sha256.Sum256(raw)
		if err != nil || id != requestID || !bytes.Equal(sum[:], hash) || c.Input != input || c.Output != output || input > maxIn || output > maxOut {
			return ErrInvalid
		}
		var cache *provider.CachePricing
		var readCount int64
		if c.Cache != nil {
			if cachePrice == nil || read == nil || uncached == nil || *read != c.Cache.Read || *uncached != c.Cache.Uncached {
				return ErrInvalid
			}
			if (write == nil) != (c.Cache.Creation == nil) || (write != nil && *write != *c.Cache.Creation) {
				return ErrInvalid
			}
			cache = &provider.CachePricing{ReadPerMillion: *cachePrice}
			readCount = *read
		} else if cachePrice != nil || read != nil || uncached != nil || write != nil {
			return ErrInvalid
		}
		actual, err := provider.TokenCost(input, output, readCount, 0, inPrice, outPrice, cache)
		if err != nil || (expected != nil && *expected != actual) {
			return ErrInvalid
		}
		var old int64
		var oldHash []byte
		err = tx.QueryRow(ctx, `SELECT actual,usage_sha256 FROM engine_completion_receipts WHERE attempt_id=$1`, attemptID).Scan(&old, &oldHash)
		if err == nil {
			if old == actual && bytes.Equal(oldHash, hash) {
				return nil
			}
			return ErrConflict
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := budget.ReconcileTx(ctx, tx, requestID, actual); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO engine_completion_receipts(attempt_id,actual,usage_sha256) VALUES($1,$2,$3)`, attemptID, actual, hash)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE engine_completion_events SET reconciled=true WHERE attempt_id=$1`, attemptID)
		return err
	})
}

// Recover selects at most 100 old events fairly across workers. Bad evidence is
// retained and does not block the rest. Missing events never settle or redispatch.
func (s *Store) Recover(ctx context.Context, before time.Time) (int, error) {
	// Include the crash gap between durable Dispatch and Bind. Marking stale
	// dispatches uncertain preserves their holds, including legacy-engine rows.
	// No legacy evidence is consumed by this embedded worker.
	// A missing terminal event after the maximum request lifetime is unknown,
	// never a zero-cost receipt. Limit work per pass and retain the budget hold.
	unknown, err := s.pool.Query(ctx, `SELECT request_id FROM budget_reservations WHERE updated_at<$1 AND state='dispatched' ORDER BY updated_at,request_id LIMIT 100`, before)
	if err != nil {
		return 0, err
	}
	var ids []string
	for unknown.Next() {
		var id string
		if err := unknown.Scan(&id); err != nil {
			unknown.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	err = unknown.Err()
	unknown.Close()
	if err != nil {
		return 0, err
	}
	ledger := budget.New(s.pool)
	for _, id := range ids {
		if err := ledger.MarkUncertain(ctx, id); err != nil && !errors.Is(err, budget.ErrState) {
			return 0, err
		}
	}
	rows, err := s.pool.Query(ctx, `WITH candidates AS (
 SELECT e.attempt_id FROM engine_completion_events e WHERE NOT e.reconciled AND e.created_at<$1 AND NOT EXISTS(SELECT 1 FROM engine_completion_receipts r WHERE r.attempt_id=e.attempt_id)
 ORDER BY e.recovery_checked_at,e.created_at,e.attempt_id LIMIT 100 FOR UPDATE OF e SKIP LOCKED)
 UPDATE engine_completion_events e SET recovery_checked_at=clock_timestamp() FROM candidates c WHERE e.attempt_id=c.attempt_id RETURNING e.attempt_id,(SELECT request_id FROM engine_execution_bindings b WHERE b.attempt_id=e.attempt_id)`, before)
	if err != nil {
		return 0, err
	}
	type item struct{ attempt, request string }
	var items []item
	for rows.Next() {
		var i item
		if err := rows.Scan(&i.attempt, &i.request); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	n := 0
	var failure error
	for _, i := range items {
		if err := s.apply(ctx, i.request, i.attempt, nil); err != nil {
			failure = ErrConflict
		} else {
			n++
		}
	}
	return n, failure
}

// Protocol is chosen from the immutable server route, never client parameters.
func Protocol(route *provider.Route) string {
	if route.API() == "embeddings" {
		return "embeddings_v1"
	}
	if route.UsesNativeMessages() {
		return "deepseek_messages_v1"
	}
	if route.UsesReadOnlyCache() {
		return "deepseek_cache_v1"
	}
	return "chat_v1"
}
