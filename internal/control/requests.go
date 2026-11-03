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
type RequestRecord struct {
	Attempt      *execution.Attempt `json:"attempt"`
	RequestID    string             `json:"request_id"`
	Period       string             `json:"period"`
	Currency     string             `json:"currency"`
	PriceVersion string             `json:"price_version"`
	State        string             `json:"state"`
	Reserved     int64              `json:"reserved"`
	Actual       *int64             `json:"actual"`
	CreatedAt    time.Time          `json:"created_at"`
	UpdatedAt    time.Time          `json:"updated_at"`
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
 'attempt_id',a.attempt_id,'request_id',a.request_id,'model',a.model,'provider',a.provider,'region',a.region,'stream',a.stream,'outcome',a.outcome,'error_code',a.error_code,'upstream_status',a.upstream_status,'input_tokens',a.input_tokens,'output_tokens',a.output_tokens,'started_at',a.started_at,'finished_at',a.finished_at,'duration_ms',a.duration_ms) END`
const recordFrom = ` FROM budget_reservations r LEFT JOIN inference_attempts a ON a.request_id=r.request_id `

func scanRecord(row interface{ Scan(...any) error }, item *RequestRecord) error {
	var metadata []byte
	if err := row.Scan(&item.RequestID, &item.Period, &item.Currency, &item.PriceVersion, &item.State, &item.Reserved, &item.Actual, &item.CreatedAt, &item.UpdatedAt, &metadata); err != nil {
		return err
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
