package control

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/systemhost"
)

func (s *Store) WithSystem(agent systemhost.Agent, admin identity.Principal, node string) *Store {
	s.host = agent
	s.hostAdmin = admin
	s.hostNode = node
	return s
}
func (s *Store) systemActor(ctx context.Context, tx pgx.Tx, p identity.Principal) error {
	if s.host == nil || s.hostAdmin.Issuer == "" || s.hostAdmin.Subject == "" || p != s.hostAdmin {
		return identity.ErrForbidden
	}
	return s.ownershipActor(ctx, tx, p)
}
func (s *Store) SystemSnapshot(ctx context.Context, p identity.Principal) (v systemhost.Snapshot, err error) {
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error { return s.systemActor(ctx, tx, p) })
	if err != nil {
		return v, err
	}
	v, err = s.host.Snapshot(ctx)
	if err == nil && v.Node != s.hostNode {
		return systemhost.Snapshot{}, systemhost.ErrConflict
	}
	return
}

type SystemEvent struct {
	ID          int64              `json:"id"`
	OperationID string             `json:"operation_id"`
	Node        string             `json:"node"`
	Actor       identity.Principal `json:"actor"`
	Action      string             `json:"action"`
	Phase       string             `json:"phase"`
	Reason      string             `json:"reason"`
	Payload     json.RawMessage    `json:"payload"`
	CreatedAt   time.Time          `json:"created_at"`
}

func (s *Store) SystemEvents(ctx context.Context, p identity.Principal) (events []SystemEvent, err error) {
	events = []SystemEvent{}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.systemActor(ctx, tx, p); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id,operation_id,node,actor_issuer,actor_subject,action,phase,reason,payload,created_at FROM system_management_events ORDER BY id DESC LIMIT 100`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v SystemEvent
			if err := rows.Scan(&v.ID, &v.OperationID, &v.Node, &v.Actor.Issuer, &v.Actor.Subject, &v.Action, &v.Phase, &v.Reason, &v.Payload, &v.CreatedAt); err != nil {
				return err
			}
			events = append(events, v)
		}
		return rows.Err()
	})
	return
}
func (s *Store) ChangeSystem(ctx context.Context, p identity.Principal, node, action, target, reason string, c systemhost.Change) (v systemhost.Result, err error) {
	// Node binding prevents a load-balancer from silently changing another host.
	if node != s.hostNode || node == "" {
		return v, systemhost.ErrConflict
	}
	if action != "apply" && action != "confirm" && action != "rollback" {
		return v, ErrInvalid
	}
	reason = strings.TrimSpace(reason)
	if len(reason) < 1 || len(reason) > 256 {
		return v, ErrInvalid
	}
	id := rand.Text()
	if action == "apply" {
		if c.ID != "" {
			return v, ErrInvalid
		}
		c.ID = id
		if err := c.Validate(); err != nil {
			return v, err
		}
	} else {
		if len(target) < 16 || len(target) > 64 {
			return v, ErrInvalid
		}
	}
	payload := map[string]any{"target": target}
	if action == "apply" {
		payload["change"] = c
	}
	raw, _ := json.Marshal(payload)
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.systemActor(ctx, tx, p); err != nil {
			return err
		}
		_, e := tx.Exec(ctx, `INSERT INTO system_management_events(operation_id,node,actor_issuer,actor_subject,action,phase,reason,payload) VALUES($1,$2,$3,$4,$5,'requested',$6,$7)`, id, node, p.Issuer, p.Subject, action, reason, raw)
		return e
	})
	if err != nil {
		return v, err
	}
	// Never hold a database transaction across a host operation. Authorization
	// linearizes at the durable intent, as does other dispatched work.
	state, checkErr := s.host.Snapshot(ctx)
	if checkErr != nil {
		err = checkErr
	} else if state.Node != node {
		err = systemhost.ErrConflict
	}
	if err == nil {
		if action == "apply" {
			v, err = s.host.Apply(ctx, c)
		} else {
			v, err = s.host.Finish(ctx, target, action == "confirm")
		}
	}
	phase := "succeeded"
	if err != nil {
		phase = "unknown"
		if errors.Is(err, systemhost.ErrInvalid) || errors.Is(err, systemhost.ErrConflict) {
			phase = "rejected"
		}
	}
	outcome, _ := json.Marshal(map[string]any{"result": v})
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_, auditErr := s.pool.Exec(cleanup, `INSERT INTO system_management_events(operation_id,node,actor_issuer,actor_subject,action,phase,reason,payload) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, node, p.Issuer, p.Subject, action, phase, reason, outcome)
	if auditErr != nil {
		return v, systemhost.ErrUnavailable
	}
	return
}
