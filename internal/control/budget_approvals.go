package control

import (
	"context"
	"crypto/rand"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrBudgetApprovalRequired = errors.New("budget adjustment requires independent approval")

type BudgetApprovalPolicy struct {
	Enabled  bool  `json:"enabled"`
	Revision int64 `json:"revision"`
}
type BudgetApproval struct {
	ID               string              `json:"id"`
	ProjectID        string              `json:"project_id"`
	Period           string              `json:"period"`
	Currency         string              `json:"currency"`
	ExpectedRevision int64               `json:"expected_revision"`
	PreviousLimit    int64               `json:"previous_limit"`
	RequestedLimit   int64               `json:"requested_limit"`
	Requester        identity.Principal  `json:"requester"`
	Reason           string              `json:"reason"`
	State            string              `json:"state"`
	CreatedAt        time.Time           `json:"created_at"`
	ExpiresAt        time.Time           `json:"expires_at"`
	FinishedAt       *time.Time          `json:"finished_at"`
	Decider          *identity.Principal `json:"decider"`
	DecisionReason   *string             `json:"decision_reason"`
	AppliedRevision  *int64              `json:"applied_revision"`
}

const approvalColumns = `id,project_id,period_id,currency,expected_revision,previous_limit,requested_limit,requester_issuer,requester_subject,reason,state,created_at,expires_at,finished_at,decider_issuer,decider_subject,decision_reason,applied_revision`

func scanApproval(row interface{ Scan(...any) error }) (v BudgetApproval, err error) {
	var issuer, subject *string
	err = row.Scan(&v.ID, &v.ProjectID, &v.Period, &v.Currency, &v.ExpectedRevision, &v.PreviousLimit, &v.RequestedLimit, &v.Requester.Issuer, &v.Requester.Subject, &v.Reason, &v.State, &v.CreatedAt, &v.ExpiresAt, &v.FinishedAt, &issuer, &subject, &v.DecisionReason, &v.AppliedRevision)
	if issuer != nil && subject != nil {
		v.Decider = &identity.Principal{Issuer: *issuer, Subject: *subject}
	}
	return
}
func approvalLock(ctx context.Context, tx pgx.Tx, org string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "aigw:budget-approvals:"+org)
	return err
}
func readApprovalPolicy(ctx context.Context, tx pgx.Tx, org string) (v BudgetApprovalPolicy, err error) {
	err = tx.QueryRow(ctx, `SELECT enabled,revision FROM budget_approval_policies WHERE organization_id=$1`, org).Scan(&v.Enabled, &v.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return
}
func (s *Store) BudgetApprovalPolicy(ctx context.Context, p identity.Principal, org string) (v BudgetApprovalPolicy, err error) {
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := orgAdmin(ctx, tx, p, org); err != nil {
			return err
		}
		v, err = readApprovalPolicy(ctx, tx, org)
		return err
	})
	return
}
func validApprovalReason(s string) bool {
	return strings.TrimSpace(s) == s && utf8.RuneCountInString(s) >= 1 && utf8.RuneCountInString(s) <= 256
}
func (s *Store) SetBudgetApprovalPolicy(ctx context.Context, p identity.Principal, org string, expected int64, enabled bool, reason string) (v BudgetApprovalPolicy, err error) {
	if expected < 0 || expected >= MaxAPIAmount || !validApprovalReason(reason) {
		return v, ErrInvalid
	}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := memberLock(ctx, tx, org); err != nil {
			return err
		}
		if err := approvalLock(ctx, tx, org); err != nil {
			return err
		}
		if err := s.ownershipActor(ctx, tx, p); err != nil {
			return err
		}
		role, err := orgRole(ctx, tx, p, org)
		if err != nil {
			return err
		}
		if role != "owner" {
			return identity.ErrForbidden
		}
		current, err := readApprovalPolicy(ctx, tx, org)
		if err != nil {
			return err
		}
		if current.Revision != expected {
			return ErrBudgetConflict
		}
		v = BudgetApprovalPolicy{Enabled: enabled, Revision: expected + 1}
		if _, err := tx.Exec(ctx, `INSERT INTO budget_approval_policies(organization_id,enabled,revision) VALUES($1,$2,$3) ON CONFLICT(organization_id) DO UPDATE SET enabled=excluded.enabled,revision=excluded.revision`, org, enabled, v.Revision); err != nil {
			return err
		}
		// Policy edits close existing proposals rather than allowing stale policy decisions.
		if _, err := tx.Exec(ctx, `WITH changed AS(UPDATE budget_approval_requests SET state='canceled',finished_at=clock_timestamp(),decider_issuer=$2,decider_subject=$3,decision_reason=$4 WHERE organization_id=$1 AND state='pending' RETURNING id)
 INSERT INTO audit_events(organization_id,actor_issuer,actor_subject,action,resource_id) SELECT $1,$2,$3,'budget.approval_canceled',id FROM changed`, org, p.Issuer, p.Subject, reason); err != nil {
			return err
		}
		// Retain policy reasons in append-only version history alongside the audit.
		if _, err := tx.Exec(ctx, `INSERT INTO budget_approval_policy_versions(organization_id,revision,enabled,reason,actor_issuer,actor_subject) VALUES($1,$2,$3,$4,$5,$6)`, org, v.Revision, enabled, reason, p.Issuer, p.Subject); err != nil {
			return err
		}
		return audit(ctx, tx, org, p, "budget.approval_policy_changed", org)
	})
	return
}
func (s *Store) RequestBudgetApproval(ctx context.Context, p identity.Principal, org, project, period string, revision, limit int64, reason string) (v BudgetApproval, err error) {
	if revision < 0 || revision >= MaxAPIAmount || limit < 0 || limit > MaxAPIAmount || !validApprovalReason(reason) {
		return v, ErrInvalid
	}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := memberLock(ctx, tx, org); err != nil {
			return err
		}
		if err := approvalLock(ctx, tx, org); err != nil {
			return err
		}
		if err := s.ownershipActor(ctx, tx, p); err != nil {
			return err
		}
		if err := orgAdmin(ctx, tx, p, org); err != nil {
			return err
		}
		policy, err := readApprovalPolicy(ctx, tx, org)
		if err != nil {
			return err
		}
		if !policy.Enabled {
			return ErrBudgetConflict
		}
		current, err := readBudget(ctx, tx, org, project)
		if err != nil {
			return err
		}
		if current.Period != period || current.Revision != revision || limit < current.Spent+current.Held || limit == current.Limit {
			return ErrBudgetConflict
		}
		if _, err := tx.Exec(ctx, `WITH changed AS(UPDATE budget_approval_requests SET state='canceled',finished_at=clock_timestamp(),decider_issuer=$3,decider_subject=$4,decision_reason='周期已变更或申请已过期' WHERE organization_id=$1 AND project_id=$2 AND state='pending' AND(expires_at<=clock_timestamp() OR period_id<>$5) RETURNING id)
 INSERT INTO audit_events(organization_id,actor_issuer,actor_subject,action,resource_id) SELECT $1,$3,$4,'budget.approval_canceled',id FROM changed`, org, project, p.Issuer, p.Subject, period); err != nil {
			return err
		}
		var pending bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM budget_approval_requests WHERE organization_id=$1 AND project_id=$2 AND state='pending')`, org, project).Scan(&pending); err != nil {
			return err
		}
		if pending {
			return ErrBudgetConflict
		}
		v, err = scanApproval(tx.QueryRow(ctx, `INSERT INTO budget_approval_requests(id,organization_id,project_id,period_id,currency,expected_revision,previous_limit,requested_limit,requester_issuer,requester_subject,reason) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING `+approvalColumns, rand.Text(), org, project, period, current.Currency, revision, current.Limit, limit, p.Issuer, p.Subject, reason))
		if err != nil {
			return err
		}
		return audit(ctx, tx, org, p, "budget.approval_requested", v.ID)
	})
	return
}
func (s *Store) DecideBudgetApproval(ctx context.Context, p identity.Principal, org, project, id, action, reason string) (v BudgetApproval, err error) {
	if (action != "approve" && action != "reject" && action != "cancel") || !validApprovalReason(reason) {
		return v, ErrInvalid
	}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := memberLock(ctx, tx, org); err != nil {
			return err
		}
		if err := approvalLock(ctx, tx, org); err != nil {
			return err
		}
		if err := s.ownershipActor(ctx, tx, p); err != nil {
			return err
		}
		if err := orgAdmin(ctx, tx, p, org); err != nil {
			return err
		}
		var err error
		v, err = scanApproval(tx.QueryRow(ctx, `SELECT `+approvalColumns+` FROM budget_approval_requests WHERE organization_id=$1 AND project_id=$2 AND id=$3 FOR UPDATE`, org, project, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if v.State != "pending" {
			return ErrBudgetConflict
		}
		state := "rejected"
		if action == "cancel" {
			if v.Requester != p {
				return identity.ErrForbidden
			}
			state = "canceled"
		}
		if action == "approve" {
			if v.Requester == p {
				return identity.ErrForbidden
			}
			if err := s.ownershipActor(ctx, tx, v.Requester); err != nil {
				return err
			}
			if err := orgAdmin(ctx, tx, v.Requester, org); err != nil {
				return err
			}
			policy, err := readApprovalPolicy(ctx, tx, org)
			if err != nil {
				return err
			}
			var expired bool
			if err := tx.QueryRow(ctx, `SELECT $1::timestamptz<=clock_timestamp()`, v.ExpiresAt).Scan(&expired); err != nil {
				return err
			}
			if !policy.Enabled || expired {
				return ErrBudgetConflict
			}
			if err := applyBudget(ctx, tx, p, org, project, v.Period, v.ExpectedRevision, v.RequestedLimit, v.Reason); err != nil {
				return err
			}
			state = "approved"
		}
		v, err = scanApproval(tx.QueryRow(ctx, `UPDATE budget_approval_requests SET state=$4,finished_at=clock_timestamp(),decider_issuer=$5,decider_subject=$6,decision_reason=$7,applied_revision=CASE WHEN $4='approved' THEN expected_revision+1 ELSE NULL END WHERE organization_id=$1 AND project_id=$2 AND id=$3 RETURNING `+approvalColumns, org, project, id, state, p.Issuer, p.Subject, reason))
		if err != nil {
			return err
		}
		return audit(ctx, tx, org, p, "budget.approval_"+state, id)
	})
	return
}
func (s *Store) BudgetApprovals(ctx context.Context, p identity.Principal, org, project, after string) (items []BudgetApproval, err error) {
	items = []BudgetApproval{}
	if len(after) > 128 {
		return items, ErrInvalid
	}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := orgAdmin(ctx, tx, p, org); err != nil {
			return err
		}
		if _, err := readBudget(ctx, tx, org, project); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+approvalColumns+` FROM budget_approval_requests WHERE organization_id=$1 AND project_id=$2 AND id>$3 ORDER BY id LIMIT 51`, org, project, after)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanApproval(rows)
			if err != nil {
				return err
			}
			items = append(items, v)
		}
		return rows.Err()
	})
	return
}

type BudgetApprovalPolicyChange struct {
	BudgetApprovalPolicy
	Actor     identity.Principal `json:"actor"`
	Reason    string             `json:"reason"`
	CreatedAt time.Time          `json:"created_at"`
}

func (s *Store) BudgetApprovalPolicyChanges(ctx context.Context, p identity.Principal, org string, after int64) (items []BudgetApprovalPolicyChange, err error) {
	items = []BudgetApprovalPolicyChange{}
	if after < 0 {
		return items, ErrInvalid
	}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := orgAdmin(ctx, tx, p, org); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT enabled,revision,actor_issuer,actor_subject,reason,created_at FROM budget_approval_policy_versions WHERE organization_id=$1 AND revision>$2 ORDER BY revision LIMIT 51`, org, after)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v BudgetApprovalPolicyChange
			if err := rows.Scan(&v.Enabled, &v.Revision, &v.Actor.Issuer, &v.Actor.Subject, &v.Reason, &v.CreatedAt); err != nil {
				return err
			}
			items = append(items, v)
		}
		return rows.Err()
	})
	return
}
