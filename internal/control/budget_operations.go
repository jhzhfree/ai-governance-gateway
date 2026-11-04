package control

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jhzhfree/ai-governance-gateway/internal/budgetalert"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
)

var ErrBudgetConflict = errors.New("budget policy changed or limit below committed balance")

type BudgetAccount struct {
	Period   string `json:"period"`
	Currency string `json:"currency"`
	Limit    int64  `json:"limit"`
	Spent    int64  `json:"spent"`
	Held     int64  `json:"held"`
	Revision int64  `json:"revision"`
}
type BudgetChange struct {
	Revision      int64              `json:"revision"`
	PreviousLimit int64              `json:"previous_limit"`
	NewLimit      int64              `json:"new_limit"`
	Reason        string             `json:"reason"`
	Actor         identity.Principal `json:"actor"`
	CreatedAt     time.Time          `json:"created_at"`
}

func budgetPermission(ctx context.Context, tx pgx.Tx, p identity.Principal, org, project string, write bool) error {
	if write || project == "" {
		return orgAdmin(ctx, tx, p, org)
	}
	_, err := projectRole(ctx, tx, p, org, project)
	return err
}
func readBudget(ctx context.Context, tx pgx.Tx, org, project string) (v BudgetAccount, err error) {
	err = tx.QueryRow(ctx, `SELECT a.period_id,a.currency,a.limit_amount,a.spent,a.held,COALESCE((SELECT max(c.revision) FROM budget_limit_changes c WHERE (c.organization_id,c.project_id,c.period_id,c.currency)=(a.organization_id,a.project_id,a.period_id,a.currency)),0) FROM budget_accounts a JOIN organizations o ON o.id=a.organization_id AND o.period_id=a.period_id AND o.currency=a.currency WHERE a.organization_id=$1 AND a.project_id=$2`, org, project).Scan(&v.Period, &v.Currency, &v.Limit, &v.Spent, &v.Held, &v.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return
}
func (s *Store) Budget(ctx context.Context, p identity.Principal, org, project string) (v BudgetAccount, err error) {
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := budgetPermission(ctx, tx, p, org, project, false); err != nil {
			return err
		}
		var e error
		v, e = readBudget(ctx, tx, org, project)
		return e
	})
	return
}
func (s *Store) SetBudget(ctx context.Context, p identity.Principal, org, project, period string, revision, limit int64, reason string) error {
	if revision < 0 || revision >= MaxAPIAmount || limit < 0 || limit > MaxAPIAmount || len(reason) < 1 || len(reason) > 256 || strings.TrimSpace(reason) != reason {
		return ErrInvalid
	}
	return transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := budgetPermission(ctx, tx, p, org, project, true); err != nil {
			return err
		}
		var current, currency string
		if err := tx.QueryRow(ctx, `SELECT period_id,currency FROM organizations WHERE id=$1 FOR SHARE`, org).Scan(&current, &currency); err != nil {
			return err
		}
		if current != period {
			return ErrBudgetConflict
		}
		rows, err := tx.Query(ctx, `SELECT project_id FROM budget_accounts WHERE organization_id=$1 AND period_id=$2 AND project_id IN ('',$3) AND currency=$4 ORDER BY project_id FOR UPDATE`, org, period, project, currency)
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
		account, err := readBudget(ctx, tx, org, project)
		if err != nil {
			return err
		}
		if account.Revision != revision || limit < account.Spent+account.Held {
			return ErrBudgetConflict
		}
		if _, err = tx.Exec(ctx, `UPDATE budget_accounts SET limit_amount=$4 WHERE organization_id=$1 AND project_id=$2 AND period_id=$3 AND currency=$5`, org, project, period, limit, account.Currency); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO budget_limit_changes(organization_id,project_id,period_id,currency,revision,previous_limit,new_limit,reason,actor_issuer,actor_subject) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, org, project, period, account.Currency, revision+1, account.Limit, limit, reason, p.Issuer, p.Subject); err != nil {
			return err
		}
		if err := budgetalert.Evaluate(ctx, tx, org, project, period, account.Currency); err != nil {
			return err
		}
		return audit(ctx, tx, org, p, "budget.limit_changed", project)
	})
}
func (s *Store) BudgetChanges(ctx context.Context, p identity.Principal, org, project, period string, after int64) (items []BudgetChange, err error) {
	items = []BudgetChange{}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := budgetPermission(ctx, tx, p, org, project, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT revision,previous_limit,new_limit,reason,actor_issuer,actor_subject,created_at FROM budget_limit_changes WHERE organization_id=$1 AND project_id=$2 AND period_id=$3 AND revision>$4 ORDER BY revision LIMIT 101`, org, project, period, after)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v BudgetChange
			if err := rows.Scan(&v.Revision, &v.PreviousLimit, &v.NewLimit, &v.Reason, &v.Actor.Issuer, &v.Actor.Subject, &v.CreatedAt); err != nil {
				return err
			}
			items = append(items, v)
		}
		return rows.Err()
	})
	return
}

type BudgetCycle struct {
	Enabled  bool  `json:"enabled"`
	Revision int64 `json:"revision"`
}

func (s *Store) BudgetCycle(ctx context.Context, p identity.Principal, org string) (v BudgetCycle, err error) {
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := orgAdmin(ctx, tx, p, org); err != nil {
			return err
		}
		e := tx.QueryRow(ctx, `SELECT enabled,revision FROM budget_cycles WHERE organization_id=$1`, org).Scan(&v.Enabled, &v.Revision)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		return e
	})
	return
}
func (s *Store) SetBudgetCycle(ctx context.Context, p identity.Principal, org string, expected int64, enabled bool) error {
	if expected < 0 || expected >= MaxAPIAmount {
		return ErrInvalid
	}
	return transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := orgAdmin(ctx, tx, p, org); err != nil {
			return err
		}
		var id string
		if err := tx.QueryRow(ctx, `SELECT id FROM organizations WHERE id=$1 FOR UPDATE`, org).Scan(&id); err != nil {
			return err
		}
		var revision int64
		err := tx.QueryRow(ctx, `SELECT revision FROM budget_cycles WHERE organization_id=$1`, org).Scan(&revision)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if revision != expected {
			return ErrBudgetConflict
		}
		if _, err = tx.Exec(ctx, `INSERT INTO budget_cycles(organization_id,enabled,revision) VALUES($1,$2,$3) ON CONFLICT(organization_id) DO UPDATE SET enabled=EXCLUDED.enabled,revision=EXCLUDED.revision`, org, enabled, revision+1); err != nil {
			return err
		}
		return audit(ctx, tx, org, p, "budget.cycle_changed", org)
	})
}
func (s *Store) RotateBudgetPeriod(ctx context.Context, p identity.Principal, org, expected, next string) error {
	return s.rotateBudgetPeriod(ctx, p, org, expected, next, false)
}
func (s *Store) rotateBudgetPeriod(ctx context.Context, p identity.Principal, org, expected, next string, automatic bool) error {
	if _, err := time.Parse("2006-01", next); err != nil {
		return ErrInvalid
	}
	return transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if !automatic {
			if err := orgAdmin(ctx, tx, p, org); err != nil {
				return err
			}
		}
		var current, currency string
		if err := tx.QueryRow(ctx, `SELECT period_id,currency FROM organizations WHERE id=$1 FOR UPDATE`, org).Scan(&current, &currency); err != nil {
			return err
		}
		if automatic {
			var enabled bool
			err := tx.QueryRow(ctx, `SELECT enabled FROM budget_cycles WHERE organization_id=$1 FOR SHARE`, org).Scan(&enabled)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			if !enabled {
				return nil
			}
			if current >= next {
				return nil
			}
		} else if current != expected || next <= current {
			return ErrBudgetConflict
		}
		// Fail if any target account exists: never reset an existing month's spending.
		if _, err := tx.Exec(ctx, `INSERT INTO budget_accounts(organization_id,project_id,period_id,currency,limit_amount) SELECT organization_id,project_id,$3,currency,limit_amount FROM budget_accounts WHERE organization_id=$1 AND period_id=$2 AND currency=$4`, org, current, next, currency); err != nil {
			return ErrBudgetConflict
		}
		if _, err := tx.Exec(ctx, `UPDATE organizations SET period_id=$2 WHERE id=$1`, org, next); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO budget_period_changes(organization_id,previous_period,new_period,actor_issuer,actor_subject) VALUES($1,$2,$3,$4,$5)`, org, current, next, p.Issuer, p.Subject); err != nil {
			return err
		}
		return audit(ctx, tx, org, p, "budget.period_rotated", org)
	})
}
func (s *Store) EnsureBudgetPeriod(ctx context.Context, org string) error {
	current := time.Now().UTC().Format("2006-01")
	var due bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organizations o JOIN budget_cycles c ON c.organization_id=o.id WHERE o.id=$1 AND c.enabled AND o.period_id<$2)`, org, current).Scan(&due); err != nil {
		return err
	}
	if !due {
		return nil
	}
	return s.rotateBudgetPeriod(ctx, identity.Principal{Issuer: "system:budget", Subject: "monthly-rotation"}, org, "", current, true)
}
func (s *Store) RotateDueBudgetPeriods(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT o.id FROM organizations o JOIN budget_cycles c ON c.organization_id=o.id WHERE c.enabled AND o.period_id<$1 ORDER BY o.id LIMIT 100`, time.Now().UTC().Format("2006-01"))
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.EnsureBudgetPeriod(ctx, id); err != nil {
			return err
		}
	}
	return nil
}
