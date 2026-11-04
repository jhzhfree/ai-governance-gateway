package control

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
)

const MaxCostReportRows = 1000

type CostRow struct {
	Day             string  `json:"day"`
	ProjectID       string  `json:"project_id"`
	Model           *string `json:"model"`
	Provider        *string `json:"provider"`
	Currency        string  `json:"currency"`
	Admitted        int64   `json:"admitted"`
	SettledAmount   string  `json:"settled_amount"`
	EstimatedAmount string  `json:"estimated_amount"`
	OpenReserved    string  `json:"open_reserved"`
	Released        int64   `json:"released"`
	UsageKnown      int64   `json:"usage_known"`
	InputTokens     string  `json:"input_tokens"`
	OutputTokens    string  `json:"output_tokens"`
}
type CostReport struct {
	From       string    `json:"from"`
	To         string    `json:"to"`
	ObservedAt time.Time `json:"observed_at"`
	Rows       []CostRow `json:"rows"`
}

// CostReport reads one consistent ledger snapshot. Date limits refer to admission
// time (UTC), not settlement time; late settlements update historical reports.
// Organization totals are restricted to owner/admin. Project roles may inspect
// only their authorized project. Unknown attempt attribution stays null.
func (s *Store) CostReport(ctx context.Context, p identity.Principal, org, project, from, to string, export bool) (v CostReport, err error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	start, e1 := time.Parse("2006-01-02", from)
	end, e2 := time.Parse("2006-01-02", to)
	if e1 != nil || e2 != nil || start.Year() < 1 || end.Year() < 1 || !end.After(start) || end.Sub(start) > 90*24*time.Hour {
		return v, ErrInvalid
	}
	v = CostReport{From: from, To: to, Rows: []CostRow{}}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		if project == "" {
			if err := orgAdmin(ctx, tx, p, org); err != nil {
				return err
			}
		} else {
			if _, err := projectRole(ctx, tx, p, org, project); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(ctx, `SELECT transaction_timestamp()`).Scan(&v.ObservedAt); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT to_char(r.created_at AT TIME ZONE 'UTC','YYYY-MM-DD'),r.project_id,a.model,a.provider,r.currency,
 count(*),COALESCE(sum(r.actual) FILTER(WHERE r.state='settled'),0)::text,
 COALESCE(sum(r.actual) FILTER(WHERE r.state='estimated_final'),0)::text,
 COALESCE(sum(r.reserved) FILTER(WHERE r.state IN ('reserved','dispatched','uncertain')),0)::text,
 count(*) FILTER(WHERE r.state='released'),count(*) FILTER(WHERE a.input_tokens IS NOT NULL AND a.output_tokens IS NOT NULL),
 COALESCE(sum(a.input_tokens) FILTER(WHERE a.input_tokens IS NOT NULL AND a.output_tokens IS NOT NULL),0)::text,
 COALESCE(sum(a.output_tokens) FILTER(WHERE a.input_tokens IS NOT NULL AND a.output_tokens IS NOT NULL),0)::text
 FROM budget_reservations r LEFT JOIN inference_attempts a USING(request_id)
 WHERE r.organization_id=$1 AND ($2='' OR r.project_id=$2) AND r.created_at >= $3 AND r.created_at < $4
 GROUP BY 1,2,3,4,5 ORDER BY 1,2,3 NULLS FIRST,4 NULLS FIRST,5 LIMIT 1001`, org, project, start, end)
		if err != nil {
			return err
		}
		for rows.Next() {
			var item CostRow
			if err := rows.Scan(&item.Day, &item.ProjectID, &item.Model, &item.Provider, &item.Currency, &item.Admitted, &item.SettledAmount, &item.EstimatedAmount, &item.OpenReserved, &item.Released, &item.UsageKnown, &item.InputTokens, &item.OutputTokens); err != nil {
				rows.Close()
				return err
			}
			v.Rows = append(v.Rows, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(v.Rows) > MaxCostReportRows {
			return ErrInvalid
		}
		if export {
			return audit(ctx, tx, org, p, "cost_report.exported", project+":"+from+":"+to)
		}
		return nil
	})
	if err != nil {
		v.Rows = nil
	}
	return
}
