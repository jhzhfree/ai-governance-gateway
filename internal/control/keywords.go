package control

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/keyword"
)

var ErrKeywordConflict = errors.New("keyword policy changed")

func (s *Store) KeywordPolicy(ctx context.Context, p identity.Principal, org, project string) (v keyword.Policy, err error) {
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := orgAdmin(ctx, tx, p, org); err != nil {
			return err
		}
		if _, err := projectRole(ctx, tx, p, org, project); err != nil {
			return err
		}
		var e error
		v, e = keyword.Read(ctx, tx, org, project)
		return e
	})
	return
}
func (s *Store) SetKeywordPolicy(ctx context.Context, p identity.Principal, org, project string, expected int64, rules []keyword.Rule) error {
	if _, err := keyword.Compile(rules); err != nil || expected < 0 || expected >= MaxAPIAmount || rules == nil {
		return ErrInvalid
	}
	return transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := orgAdmin(ctx, tx, p, org); err != nil {
			return err
		}
		if _, err := projectRole(ctx, tx, p, org, project); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,14014))`, org+"/"+project); err != nil {
			return err
		}
		current, err := keyword.Read(ctx, tx, org, project)
		if err != nil {
			return err
		}
		if current.Revision != expected {
			return ErrKeywordConflict
		}
		raw, _ := json.Marshal(rules)
		if _, err := tx.Exec(ctx, `INSERT INTO keyword_policies(organization_id,project_id,revision,rules) VALUES($1,$2,$3,$4) ON CONFLICT(organization_id,project_id) DO UPDATE SET revision=EXCLUDED.revision,rules=EXCLUDED.rules`, org, project, expected+1, raw); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO keyword_policy_versions(organization_id,project_id,revision,rules,actor_issuer,actor_subject) VALUES($1,$2,$3,$4,$5,$6)`, org, project, expected+1, raw, p.Issuer, p.Subject); err != nil {
			return err
		}
		return audit(ctx, tx, org, p, "keyword.policy_changed", project)
	})
}

type KeywordMatch struct {
	ID             int64     `json:"id"`
	RequestID      string    `json:"request_id"`
	PolicyRevision int64     `json:"policy_revision"`
	RuleID         string    `json:"rule_id"`
	Direction      string    `json:"direction"`
	Action         string    `json:"action"`
	CreatedAt      time.Time `json:"created_at"`
}

func (s *Store) KeywordMatches(ctx context.Context, p identity.Principal, org, project string, before int64) (items []KeywordMatch, err error) {
	items = []KeywordMatch{}
	if before < 0 {
		return nil, ErrInvalid
	}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := orgAdmin(ctx, tx, p, org); err != nil {
			return err
		}
		if _, err := projectRole(ctx, tx, p, org, project); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id,request_id,policy_revision,rule_id,direction,action,created_at FROM keyword_matches WHERE organization_id=$1 AND project_id=$2 AND ($3::bigint=0 OR id<$3) ORDER BY id DESC LIMIT 101`, org, project, before)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item KeywordMatch
			if err := rows.Scan(&item.ID, &item.RequestID, &item.PolicyRevision, &item.RuleID, &item.Direction, &item.Action, &item.CreatedAt); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return
}
