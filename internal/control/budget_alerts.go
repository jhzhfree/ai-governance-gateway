package control

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jhzhfree/ai-governance-gateway/internal/budgetalert"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
)

type AlertPolicy struct {
	Enabled    bool  `json:"enabled"`
	Thresholds []int `json:"thresholds"`
	Revision   int64 `json:"revision"`
}
type BudgetAlert struct {
	ID             int64               `json:"id"`
	Period         string              `json:"period"`
	Currency       string              `json:"currency"`
	Threshold      int                 `json:"threshold"`
	PolicyRevision int64               `json:"policy_revision"`
	Limit          string              `json:"limit"`
	Spent          string              `json:"spent"`
	Held           string              `json:"held"`
	CreatedAt      time.Time           `json:"created_at"`
	AcknowledgedAt *time.Time          `json:"acknowledged_at"`
	Actor          *identity.Principal `json:"actor"`
	Reason         *string             `json:"reason"`
}

func alertPolicy(ctx context.Context, tx pgx.Tx, org, project string) (v AlertPolicy, err error) {
	v = AlertPolicy{Thresholds: []int{80, 100}}
	err = tx.QueryRow(ctx, `SELECT enabled,thresholds,revision FROM budget_alert_policies WHERE organization_id=$1 AND project_id=$2`, org, project).Scan(&v.Enabled, &v.Thresholds, &v.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return
}
func (s *Store) AlertPolicy(ctx context.Context, p identity.Principal, org, project string) (v AlertPolicy, err error) {
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := budgetPermission(ctx, tx, p, org, project, false); err != nil {
			return err
		}
		var err error
		v, err = alertPolicy(ctx, tx, org, project)
		return err
	})
	return
}
func (s *Store) SetAlertPolicy(ctx context.Context, p identity.Principal, org, project string, v AlertPolicy, reason string) error {
	if v.Revision < 0 || v.Revision >= MaxAPIAmount || len(v.Thresholds) < 1 || len(v.Thresholds) > 7 || !validReason(reason) {
		return ErrInvalid
	}
	previous := 0
	for _, t := range v.Thresholds {
		if t <= previous || (t != 50 && t != 60 && t != 70 && t != 80 && t != 90 && t != 95 && t != 100) {
			return ErrInvalid
		}
		previous = t
	}
	return transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := budgetPermission(ctx, tx, p, org, project, true); err != nil {
			return err
		}
		var period, currency string
		if err := tx.QueryRow(ctx, `SELECT period_id,currency FROM organizations WHERE id=$1 FOR SHARE`, org).Scan(&period, &currency); err != nil {
			return err
		}
		// Match ledger lock order. Publishing under account locks serializes policy
		// changes with reservations, settlement and other policy writers.
		rows, err := tx.Query(ctx, `SELECT project_id FROM budget_accounts WHERE organization_id=$1 AND project_id IN ('',$2) AND period_id=$3 AND currency=$4 ORDER BY project_id FOR UPDATE`, org, project, period, currency)
		if err != nil {
			return err
		}
		for rows.Next() {
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if _, err := readBudget(ctx, tx, org, project); err != nil {
			return err
		}
		current, err := alertPolicy(ctx, tx, org, project)
		if err != nil {
			return err
		}
		if current.Revision != v.Revision {
			return ErrBudgetConflict
		}
		_, err = tx.Exec(ctx, `INSERT INTO budget_alert_policies(organization_id,project_id,enabled,thresholds,revision) VALUES($1,$2,$3,$4,$5) ON CONFLICT(organization_id,project_id) DO UPDATE SET enabled=EXCLUDED.enabled,thresholds=EXCLUDED.thresholds,revision=EXCLUDED.revision`, org, project, v.Enabled, v.Thresholds, v.Revision+1)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO budget_alert_policy_versions(organization_id,project_id,revision,enabled,thresholds,actor_issuer,actor_subject,reason) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, org, project, v.Revision+1, v.Enabled, v.Thresholds, p.Issuer, p.Subject, reason)
		if err != nil {
			return err
		}
		if err := budgetalert.Evaluate(ctx, tx, org, project, period, currency); err != nil {
			return err
		}
		return audit(ctx, tx, org, p, "budget_alert.policy_changed", project)
	})
}
func validReason(reason string) bool {
	return len(reason) > 0 && len(reason) <= 256 && strings.TrimSpace(reason) == reason
}
func (s *Store) BudgetAlerts(ctx context.Context, p identity.Principal, org, project string, after int64) (items []BudgetAlert, err error) {
	items = []BudgetAlert{}
	if after < 0 {
		return items, ErrInvalid
	}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := budgetPermission(ctx, tx, p, org, project, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id,period_id,currency,threshold,policy_revision,limit_amount::text,spent::text,held::text,created_at,acknowledged_at,actor_issuer,actor_subject,reason FROM budget_alerts WHERE organization_id=$1 AND project_id=$2 AND ($3::bigint=0 OR id<$3) ORDER BY id DESC LIMIT 51`, org, project, after)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a BudgetAlert
			var issuer, subject *string
			if err := rows.Scan(&a.ID, &a.Period, &a.Currency, &a.Threshold, &a.PolicyRevision, &a.Limit, &a.Spent, &a.Held, &a.CreatedAt, &a.AcknowledgedAt, &issuer, &subject, &a.Reason); err != nil {
				return err
			}
			if issuer != nil && subject != nil {
				a.Actor = &identity.Principal{Issuer: *issuer, Subject: *subject}
			}
			items = append(items, a)
		}
		return rows.Err()
	})
	return
}
func (s *Store) AcknowledgeBudgetAlert(ctx context.Context, p identity.Principal, org, project string, id int64, reason string) error {
	if id < 1 || !validReason(reason) {
		return ErrInvalid
	}
	return transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := budgetPermission(ctx, tx, p, org, project, true); err != nil {
			return err
		}
		var ack *time.Time
		if err := tx.QueryRow(ctx, `SELECT acknowledged_at FROM budget_alerts WHERE organization_id=$1 AND project_id=$2 AND id=$3 FOR UPDATE`, org, project, id).Scan(&ack); errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if ack != nil {
			return ErrBudgetConflict
		}
		if _, err := tx.Exec(ctx, `UPDATE budget_alerts SET acknowledged_at=clock_timestamp(),actor_issuer=$4,actor_subject=$5,reason=$6 WHERE organization_id=$1 AND project_id=$2 AND id=$3`, org, project, id, p.Issuer, p.Subject, reason); err != nil {
			return err
		}
		return audit(ctx, tx, org, p, "budget_alert.acknowledged", strconv.FormatInt(id, 10))
	})
}
