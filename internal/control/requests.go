package control

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
)

// RequestRecord exposes accounting metadata only. State does not acknowledge
// delivery to the downstream client; rejected requests without a hold are absent.
type RequestRecord struct {
	RequestID    string    `json:"request_id"`
	Period       string    `json:"period"`
	Currency     string    `json:"currency"`
	PriceVersion string    `json:"price_version"`
	State        string    `json:"state"`
	Reserved     int64     `json:"reserved"`
	Actual       *int64    `json:"actual"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
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

const recordColumns = `request_id,period_id,currency,price_version,state,reserved,actual,created_at,updated_at`

func scanRecord(row interface{ Scan(...any) error }, item *RequestRecord) error {
	return row.Scan(&item.RequestID, &item.Period, &item.Currency, &item.PriceVersion, &item.State, &item.Reserved, &item.Actual, &item.CreatedAt, &item.UpdatedAt)
}
func (s *Store) ListRequests(ctx context.Context, p identity.Principal, org, project, after, state string, limit int) (items []RequestRecord, err error) {
	items = []RequestRecord{}
	stamp, id, err := requestCursor(after)
	if err != nil || !ValidRequestState(state) || limit < 1 || limit > 101 {
		return items, ErrInvalid
	}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := projectRole(ctx, tx, p, org, project); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+recordColumns+` FROM budget_reservations WHERE organization_id=$1 AND project_id=$2 AND ($3='' OR state=$3) AND ($4='' OR (created_at,request_id)<($5,$4)) ORDER BY created_at DESC,request_id DESC LIMIT $6`, org, project, state, id, stamp, limit)
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
		err := scanRecord(tx.QueryRow(ctx, `SELECT `+recordColumns+` FROM budget_reservations WHERE organization_id=$1 AND project_id=$2 AND request_id=$3`, org, project, id), &record)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return
}
