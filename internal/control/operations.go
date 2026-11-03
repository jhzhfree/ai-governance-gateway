package control

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jhzhfree/ai-governance-gateway/internal/admission"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
)

func (s *Store) Limits(ctx context.Context, p identity.Principal, org, project string) (v admission.Snapshot, err error) {
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		if _, err := projectRole(ctx, tx, p, org, project); err != nil {
			return err
		}
		var now time.Time
		if err := tx.QueryRow(ctx, `SELECT transaction_timestamp()`).Scan(&now); err != nil {
			return err
		}
		var err error
		v, err = admission.Read(ctx, tx, org, project, now)
		return err
	})
	return
}
func (s *Store) SetLimits(ctx context.Context, p identity.Principal, org, project string, policy admission.Policy) (v admission.Snapshot, err error) {
	if !policy.Valid() {
		return v, ErrInvalid
	}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := orgAdmin(ctx, tx, p, org); err != nil {
			return err
		}
		if _, err := projectRole(ctx, tx, p, org, project); err != nil {
			return err
		}
		if err := admission.Update(ctx, tx, org, project, policy); err != nil {
			return err
		}
		if err := audit(ctx, tx, org, p, "project.limits_changed", project); err != nil {
			return err
		}
		var now time.Time
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return err
		}
		var err error
		v, err = admission.Read(ctx, tx, org, project, now)
		return err
	})
	return
}

type AccountCheck struct {
	Scope         string `json:"scope"`
	Period        string `json:"period"`
	Currency      string `json:"currency"`
	Limit         string `json:"limit_amount"`
	StoredHeld    string `json:"stored_held"`
	ComputedHeld  string `json:"computed_held"`
	StoredSpent   string `json:"stored_spent"`
	ComputedSpent string `json:"computed_spent"`
	Consistent    bool   `json:"consistent"`
}
type InspectionItem struct {
	Request     RequestRecord `json:"request"`
	LeaseActive bool          `json:"lease_active"`
	Reasons     []string      `json:"reasons"`
}
type Inspection struct {
	ObservedAt        time.Time          `json:"observed_at"`
	Cutoff            time.Time          `json:"cutoff"`
	Limits            admission.Snapshot `json:"limits"`
	Accounts          []AccountCheck     `json:"accounts"`
	AccountsTruncated bool               `json:"accounts_truncated"`
	Data              []InspectionItem   `json:"data"`
	NextCursor        string             `json:"next_cursor"`
}

func (s *Store) Inspect(ctx context.Context, p identity.Principal, org, project, after string, olderSeconds, limit int) (v Inspection, err error) {
	v.Accounts = []AccountCheck{}
	v.Data = []InspectionItem{}
	stamp, id, err := requestCursor(after)
	if err != nil || olderSeconds < 1 || olderSeconds > 2592000 || limit < 1 || limit > 100 {
		return v, ErrInvalid
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		if _, err := projectRole(ctx, tx, p, org, project); err != nil {
			return err
		}
		role, err := orgRole(ctx, tx, p, org)
		if err != nil {
			return err
		}
		orgAdmin := role == "owner" || role == "admin"
		if err := tx.QueryRow(ctx, `SELECT transaction_timestamp()`).Scan(&v.ObservedAt); err != nil {
			return err
		}
		v.Cutoff = v.ObservedAt.Add(-time.Duration(olderSeconds) * time.Second)
		v.Limits, err = admission.Read(ctx, tx, org, project, v.ObservedAt)
		if err != nil {
			return err
		}
		accounts, err := tx.Query(ctx, `SELECT CASE WHEN a.project_id='' THEN 'organization' ELSE 'project' END,a.period_id,a.currency,a.limit_amount::text,a.held::text,c.held::text,a.spent::text,c.spent::text,(a.held=c.held AND a.spent=c.spent) FROM budget_accounts a CROSS JOIN LATERAL (
 SELECT COALESCE(sum(reserved) FILTER(WHERE state IN ('reserved','dispatched','uncertain')),0) AS held,COALESCE(sum(actual) FILTER(WHERE state IN ('settled','released','estimated_final')),0) AS spent FROM budget_reservations r WHERE r.organization_id=a.organization_id AND r.period_id=a.period_id AND r.currency=a.currency AND (a.project_id='' OR r.project_id=a.project_id)
 ) c WHERE a.organization_id=$1 AND (a.project_id=$2 OR ($3 AND a.project_id='')) ORDER BY a.period_id DESC,a.currency,a.project_id LIMIT 101`, org, project, orgAdmin)
		if err != nil {
			return err
		}
		for accounts.Next() {
			var a AccountCheck
			if err := accounts.Scan(&a.Scope, &a.Period, &a.Currency, &a.Limit, &a.StoredHeld, &a.ComputedHeld, &a.StoredSpent, &a.ComputedSpent, &a.Consistent); err != nil {
				accounts.Close()
				return err
			}
			v.Accounts = append(v.Accounts, a)
		}
		err = accounts.Err()
		accounts.Close()
		if err != nil {
			return err
		}
		if len(v.Accounts) > 100 {
			v.AccountsTruncated = true
			v.Accounts = v.Accounts[:100]
		}
		rows, err := tx.Query(ctx, `SELECT `+recordColumns+`,EXISTS(SELECT 1 FROM admission_leases l WHERE l.request_id=r.request_id AND l.organization_id=r.organization_id AND l.project_id=r.project_id AND l.expires_at>$3)`+recordFrom+` WHERE r.organization_id=$1 AND r.project_id=$2 AND ((r.state IN ('reserved','dispatched','uncertain') AND r.updated_at<=$4) OR (a.outcome='unfinished' AND a.started_at<=$4)) AND ($5='' OR (r.created_at,r.request_id)<($6,$5)) ORDER BY r.created_at DESC,r.request_id DESC LIMIT $7`, org, project, v.ObservedAt, v.Cutoff, id, stamp, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item InspectionItem
			if err := scanRecord(rows, &item.Request, &item.LeaseActive); err != nil {
				return err
			}
			item.Reasons = []string{}
			r := item.Request
			if (r.State == "reserved" || r.State == "dispatched" || r.State == "uncertain") && !r.UpdatedAt.After(v.Cutoff) {
				item.Reasons = append(item.Reasons, "open_hold")
			}
			if r.Attempt == nil {
				item.Reasons = append(item.Reasons, "metadata_unrecorded")
			} else if r.Attempt.Outcome == "unfinished" && !r.Attempt.StartedAt.After(v.Cutoff) {
				item.Reasons = append(item.Reasons, "execution_unfinished")
			}
			v.Data = append(v.Data, item)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(v.Data) > limit {
			v.Data = v.Data[:limit]
			v.NextCursor = v.Data[len(v.Data)-1].Request.Cursor()
		}
		return nil
	})
	return
}
