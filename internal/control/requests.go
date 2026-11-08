package control

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jhzhfree/ai-governance-gateway/internal/execution"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
)

// RequestRecord exposes accounting metadata only. State does not acknowledge
// delivery to the downstream client; rejected requests without a hold are absent.
type CacheBilling struct {
	FiveMinutePerMillion *int64 `json:"five_minute_per_million"`
	OneHourPerMillion    *int64 `json:"one_hour_per_million"`
	FiveMinuteTokens     *int64 `json:"five_minute_tokens"`
	OneHourTokens        *int64 `json:"one_hour_tokens"`
	InputPerMillion      int64  `json:"input_per_million"`
	OutputPerMillion     int64  `json:"output_per_million"`
	ReadPerMillion       int64  `json:"read_per_million"`
	WritePerMillion      int64  `json:"write_per_million"`
	DetailsVersion       int    `json:"details_version"`
	InputTokens          *int64 `json:"input_tokens"`
	OutputTokens         *int64 `json:"output_tokens"`
	ReadTokens           *int64 `json:"read_tokens"`
	WriteTokens          *int64 `json:"write_tokens"`
}

type RequestRecord struct {
	EmbeddedCompletion *EmbeddedCompletion `json:"embedded_completion,omitempty"`
	CacheBilling       *CacheBilling       `json:"cache_billing,omitempty"`
	Attempt            *execution.Attempt  `json:"attempt"`
	RequestID          string              `json:"request_id"`
	Period             string              `json:"period"`
	Currency           string              `json:"currency"`
	PriceVersion       string              `json:"price_version"`
	State              string              `json:"state"`
	Reserved           int64               `json:"reserved"`
	Actual             *int64              `json:"actual"`
	CreatedAt          time.Time           `json:"created_at"`
	UpdatedAt          time.Time           `json:"updated_at"`
}

func ValidRequestState(state string) bool {
	switch state {
	case "", "reserved", "dispatched", "uncertain", "settled", "released", "estimated_final":
		return true
	}
	return false
}
func (r RequestRecord) Cursor() string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(r.CreatedAt.UnixMicro(), 10) + ":" + r.RequestID))
}
func requestCursor(cursor string) (time.Time, string, error) {
	if cursor == "" {
		return time.Time{}, "", nil
	}
	if len(cursor) > 512 {
		return time.Time{}, "", ErrInvalid
	}
	data, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", ErrInvalid
	}
	stamp, id, ok := strings.Cut(string(data), ":")
	micro, err := strconv.ParseInt(stamp, 10, 64)
	if !ok || err != nil || (micro <= 0 || micro > 253402300799999999) || len(id) == 0 || len(id) > 256 || base64.RawURLEncoding.EncodeToString(data) != cursor {
		return time.Time{}, "", ErrInvalid
	}
	return time.UnixMicro(micro), id, nil
}

const recordColumns = `r.request_id,r.period_id,r.currency,r.price_version,r.state,r.reserved,r.actual,r.created_at,r.updated_at,
 CASE WHEN a.attempt_id IS NULL THEN NULL ELSE jsonb_build_object(
 'attempt_id',a.attempt_id,'request_id',a.request_id,'model',a.model,'provider',a.provider,'region',a.region,'stream',a.stream,'outcome',a.outcome,'error_code',a.error_code,'upstream_status',a.upstream_status,'input_tokens',a.input_tokens,'output_tokens',a.output_tokens,'started_at',a.started_at,'finished_at',a.finished_at,'duration_ms',a.duration_ms) END,
 CASE WHEN b.cache_read_price IS NULL THEN NULL ELSE jsonb_build_object(
 'input_per_million',b.input_price,'output_per_million',b.output_price,'read_per_million',b.cache_read_price,'write_per_million',b.cache_write_price,
 'five_minute_per_million',b.cache_write_5m_price,'one_hour_per_million',b.cache_write_1h_price,
 'five_minute_tokens',CASE WHEN e.cache_details_version=2 THEN e.cache_write_5m_tokens ELSE NULL END,
 'one_hour_tokens',CASE WHEN e.cache_details_version=2 THEN e.cache_write_1h_tokens ELSE NULL END,
 'details_version',COALESCE(e.cache_details_version,0),'input_tokens',e.input_tokens,'output_tokens',e.output_tokens,
 'read_tokens',CASE WHEN e.cache_details_version>=1 THEN e.cache_read_tokens ELSE NULL END,
 'write_tokens',CASE WHEN e.cache_details_version>=1 THEN e.cache_write_tokens ELSE NULL END) END,
 CASE WHEN eb.attempt_id IS NULL THEN NULL ELSE jsonb_build_object(
 'engine',eb.engine,'protocol',eb.protocol,
 'state',CASE WHEN er.attempt_id IS NOT NULL THEN 'receipted' WHEN ee.attempt_id IS NOT NULL THEN 'recorded' ELSE 'awaiting_usage' END,
 'input_per_million',eb.input_price,'output_per_million',eb.output_price,
 'input_tokens',ee.input_tokens,'output_tokens',ee.output_tokens,'actual',er.actual,
 'recorded_at',ee.created_at,'received_at',er.received_at) END`
const recordFrom = ` FROM budget_reservations r LEFT JOIN inference_attempts a ON a.request_id=r.request_id LEFT JOIN newapi_execution_bindings b ON b.attempt_id=a.attempt_id LEFT JOIN newapi_execution_receipts e ON e.attempt_id=b.attempt_id
 LEFT JOIN engine_execution_bindings eb ON eb.attempt_id=a.attempt_id LEFT JOIN engine_completion_events ee ON ee.attempt_id=eb.attempt_id
 LEFT JOIN engine_completion_receipts er ON er.attempt_id=ee.attempt_id AND er.usage_sha256=ee.usage_sha256 AND ee.reconciled `

func scanRecord(row interface{ Scan(...any) error }, item *RequestRecord, extras ...any) error {
	var metadata, cache, embedded []byte
	args := []any{&item.RequestID, &item.Period, &item.Currency, &item.PriceVersion, &item.State, &item.Reserved, &item.Actual, &item.CreatedAt, &item.UpdatedAt, &metadata, &cache, &embedded}
	args = append(args, extras...)
	if err := row.Scan(args...); err != nil {
		return err
	}
	if cache != nil {
		if err := json.Unmarshal(cache, &item.CacheBilling); err != nil {
			return err
		}
	}
	if embedded != nil {
		if err := json.Unmarshal(embedded, &item.EmbeddedCompletion); err != nil {
			return err
		}
	}
	if metadata != nil {
		var attempt execution.Attempt
		if err := json.Unmarshal(metadata, &attempt); err != nil {
			return err
		}
		item.Attempt = &attempt
	}
	return nil
}
func (s *Store) ListRequests(ctx context.Context, p identity.Principal, org, project, after, state, outcome string, limit int) (items []RequestRecord, err error) {
	items = []RequestRecord{}
	stamp, id, err := requestCursor(after)
	if err != nil || !ValidRequestState(state) || !execution.ValidOutcome(outcome) || limit < 1 || limit > 101 {
		return items, ErrInvalid
	}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := projectRole(ctx, tx, p, org, project); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+recordColumns+recordFrom+` WHERE r.organization_id=$1 AND r.project_id=$2 AND ($3='' OR r.state=$3) AND ($4='' OR (r.created_at,r.request_id)<($5,$4)) AND ($7='' OR a.outcome=$7) ORDER BY r.created_at DESC,r.request_id DESC LIMIT $6`, org, project, state, id, stamp, limit, outcome)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v RequestRecord
			if err := scanRecord(rows, &v); err != nil {
				return err
			}
			items = append(items, v)
		}
		return rows.Err()
	})
	return
}
func (s *Store) GetRequest(ctx context.Context, p identity.Principal, org, project, id string) (record RequestRecord, err error) {
	if id == "" || len(id) > 256 {
		return record, ErrInvalid
	}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := projectRole(ctx, tx, p, org, project); err != nil {
			return err
		}
		err := scanRecord(tx.QueryRow(ctx, `SELECT `+recordColumns+recordFrom+` WHERE r.organization_id=$1 AND r.project_id=$2 AND r.request_id=$3`, org, project, id), &record)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return
}
