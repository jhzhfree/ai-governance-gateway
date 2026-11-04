package control

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jhzhfree/ai-governance-gateway/internal/circuit"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/observe"
)

type RouteStatus struct {
	Model      string `json:"model"`
	Provider   string `json:"provider"`
	State      string `json:"state"`
	RetryAfter int    `json:"retry_after"`
}
type Runtime struct {
	ObservedAt           time.Time         `json:"observed_at"`
	Models               []RouteStatus     `json:"models"`
	OpenHolds            int64             `json:"open_holds"`
	HeldAmount           string            `json:"held_amount"`
	UnfinishedExecutions int64             `json:"unfinished_executions"`
	Instance             *observe.Snapshot `json:"instance"`
}

func (s *Store) Runtime(ctx context.Context, p identity.Principal, org, project string) (v Runtime, err error) {
	v.Models = []RouteStatus{}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		if _, err := projectRole(ctx, tx, p, org, project); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT transaction_timestamp()`).Scan(&v.ObservedAt); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT model FROM project_models WHERE organization_id=$1 AND project_id=$2 ORDER BY model`, org, project)
		if err != nil {
			return err
		}
		allowed := map[string]bool{}
		for rows.Next() {
			var model string
			if err := rows.Scan(&model); err != nil {
				rows.Close()
				return err
			}
			allowed[model] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, model := range s.modelsIn(ctx, tx) {
			if !allowed[model.ID] {
				continue
			}
			status := circuit.Snapshot{State: "not_applicable"}
			if model.Provider != "mock" {
				var err error
				status, err = circuit.Read(ctx, tx, model.PriceVersion, v.ObservedAt)
				if err != nil {
					return err
				}
			}
			v.Models = append(v.Models, RouteStatus{model.ID, model.Provider, status.State, status.RetryAfter})
		}
		return tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE r.state IN ('reserved','dispatched','uncertain')),COALESCE(sum(r.reserved) FILTER(WHERE r.state IN ('reserved','dispatched','uncertain')),0)::text,count(*) FILTER(WHERE a.outcome='unfinished') FROM budget_reservations r LEFT JOIN inference_attempts a USING(request_id) WHERE r.organization_id=$1 AND r.project_id=$2`, org, project).Scan(&v.OpenHolds, &v.HeldAmount, &v.UnfinishedExecutions)
	})
	return
}
