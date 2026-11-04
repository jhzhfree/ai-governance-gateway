// Package budgetalert writes metadata-only notifications in the caller's ledger
// transaction. It never sends messages or changes budget balances.
package budgetalert

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Evaluate accounts must already be locked by the caller. Policy reads use MVCC
// without row locks; policy writers lock accounts before publishing and backfill
// current balances, so concurrent configuration cannot miss a committed crossing.
func Evaluate(ctx context.Context, tx pgx.Tx, org, project, period, currency string) error {
	_, err := tx.Exec(ctx, `INSERT INTO budget_alerts(organization_id,project_id,period_id,currency,threshold,policy_revision,limit_amount,spent,held)
 SELECT a.organization_id,a.project_id,a.period_id,a.currency,t.value,p.revision,a.limit_amount,a.spent,a.held
 FROM budget_accounts a JOIN budget_alert_policies p USING(organization_id,project_id)
 CROSS JOIN LATERAL unnest(p.thresholds) AS t(value)
 WHERE a.organization_id=$1 AND a.project_id IN ('',$2) AND a.period_id=$3 AND a.currency=$4
 AND p.enabled AND a.limit_amount>0 AND (a.spent::numeric+a.held::numeric)*100>=a.limit_amount::numeric*t.value
 ON CONFLICT(organization_id,project_id,period_id,currency,threshold) DO NOTHING`, org, project, period, currency)
	return err
}
