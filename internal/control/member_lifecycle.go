package control

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
)

type MemberLifecycleEvent struct {
	Revision                int64              `json:"revision"`
	Enabled                 bool               `json:"enabled"`
	PreviousRole            string             `json:"previous_role"`
	NewRole                 string             `json:"new_role"`
	Reason                  string             `json:"reason"`
	Actor                   identity.Principal `json:"actor"`
	RevokedKeys             int64              `json:"revoked_keys"`
	RemovedProjectGrants    int64              `json:"removed_project_grants"`
	RemovedDepartmentGrants int64              `json:"removed_department_grants"`
	RevokedAuditors         int64              `json:"revoked_auditors"`
	CanceledTransfers       int64              `json:"canceled_transfers"`
	CreatedAt               time.Time          `json:"created_at"`
}

func (s *Store) SetMemberEnabled(ctx context.Context, p identity.Principal, org string, target identity.Principal, expected int64, enabled bool, restoreRole, reason string) (v MemberLifecycleEvent, err error) {
	if !validPrincipal(target) || p == target || expected < 0 || expected >= MaxAPIAmount || strings.TrimSpace(reason) != reason || utf8.RuneCountInString(reason) < 1 || utf8.RuneCountInString(reason) > 256 {
		return v, ErrInvalid
	}
	if (!enabled && restoreRole != "") || (enabled && restoreRole != "admin" && restoreRole != "developer" && restoreRole != "viewer") {
		return v, ErrInvalid
	}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := memberLock(ctx, tx, org); err != nil {
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
		var currentEnabled bool
		var currentRole string
		var revision int64
		if err := tx.QueryRow(ctx, `SELECT role,enabled,lifecycle_revision FROM organization_members WHERE organization_id=$1 AND issuer=$2 AND subject=$3 FOR UPDATE`, org, target.Issuer, target.Subject).Scan(&currentRole, &currentEnabled, &revision); errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if currentRole == "owner" {
			return identity.ErrForbidden
		}
		if revision != expected || enabled == currentEnabled {
			return ErrMemberConflict
		}
		v = MemberLifecycleEvent{Revision: revision + 1, Enabled: enabled, PreviousRole: currentRole, NewRole: currentRole, Actor: p, Reason: reason}
		if enabled {
			v.NewRole = restoreRole
		}
		if _, err := tx.Exec(ctx, `UPDATE organization_members SET enabled=$4,role=$5,lifecycle_revision=$6 WHERE organization_id=$1 AND issuer=$2 AND subject=$3`, org, target.Issuer, target.Subject, enabled, v.NewRole, v.Revision); err != nil {
			return err
		}
		if !enabled {
			if err := departmentLock(ctx, tx, org, true); err != nil {
				return err
			}
			tag, err := tx.Exec(ctx, `DELETE FROM department_members WHERE organization_id=$1 AND issuer=$2 AND subject=$3`, org, target.Issuer, target.Subject)
			if err != nil {
				return err
			}
			v.RemovedDepartmentGrants = tag.RowsAffected()
			if v.RemovedDepartmentGrants > 0 {
				if _, err := recordDepartments(ctx, tx, p, org, reason); err != nil {
					return err
				}
			}
			// Credential issuance holds this same member row FOR SHARE until commit.
			// Therefore a late issuance either precedes this update and is revoked here,
			// or sees enabled=false after waiting and cannot commit another credential.
			if err := tx.QueryRow(ctx, `WITH changed AS (
 UPDATE project_keys SET revoked_at=clock_timestamp() WHERE organization_id=$1 AND creator_issuer=$2 AND creator_subject=$3 AND revoked_at IS NULL RETURNING id),
 logged AS (INSERT INTO audit_events(organization_id,actor_issuer,actor_subject,action,resource_id) SELECT $1,$4,$5,'project.key_revoked',id FROM changed RETURNING id)
 SELECT count(*) FROM logged`, org, target.Issuer, target.Subject, p.Issuer, p.Subject).Scan(&v.RevokedKeys); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `WITH changed AS (DELETE FROM project_members WHERE organization_id=$1 AND issuer=$2 AND subject=$3 RETURNING project_id),
 logged AS (INSERT INTO audit_events(organization_id,actor_issuer,actor_subject,action,resource_id) SELECT $1,$4,$5,'project.member_removed',project_id FROM changed RETURNING id)
 SELECT count(*) FROM logged`, org, target.Issuer, target.Subject, p.Issuer, p.Subject).Scan(&v.RemovedProjectGrants); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `WITH changed AS (UPDATE content_audit_auditors SET active=false WHERE organization_id=$1 AND issuer=$2 AND subject=$3 AND active RETURNING project_id),
 logged AS (INSERT INTO audit_events(organization_id,actor_issuer,actor_subject,action,resource_id) SELECT $1,$4,$5,'content_audit.auditor_changed',project_id FROM changed RETURNING id)
 SELECT count(*) FROM logged`, org, target.Issuer, target.Subject, p.Issuer, p.Subject).Scan(&v.RevokedAuditors); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `WITH changed AS (UPDATE organization_ownership_transfers SET state='canceled',finished_at=clock_timestamp() WHERE organization_id=$1 AND target_issuer=$2 AND target_subject=$3 AND state='pending' RETURNING id),
 logged AS (INSERT INTO audit_events(organization_id,actor_issuer,actor_subject,action,resource_id) SELECT $1,$4,$5,'organization.ownership_canceled',id FROM changed RETURNING id)
 SELECT count(*) FROM logged`, org, target.Issuer, target.Subject, p.Issuer, p.Subject).Scan(&v.CanceledTransfers); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO organization_member_lifecycle(organization_id,issuer,subject,revision,enabled,previous_role,new_role,reason,actor_issuer,actor_subject,revoked_keys,removed_project_grants,revoked_auditors,canceled_transfers,removed_department_grants) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) RETURNING created_at`, org, target.Issuer, target.Subject, v.Revision, enabled, currentRole, v.NewRole, reason, p.Issuer, p.Subject, v.RevokedKeys, v.RemovedProjectGrants, v.RevokedAuditors, v.CanceledTransfers, v.RemovedDepartmentGrants).Scan(&v.CreatedAt); err != nil {
			return err
		}
		action := "organization.member_disabled"
		if enabled {
			action = "organization.member_enabled"
		}
		return audit(ctx, tx, org, p, action, target.Issuer+"#"+target.Subject)
	})
	return
}
func (s *Store) MemberLifecycle(ctx context.Context, p identity.Principal, org string, target identity.Principal, after int64) (items []MemberLifecycleEvent, err error) {
	items = []MemberLifecycleEvent{}
	if !validPrincipal(target) || after < 0 {
		return items, ErrInvalid
	}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := orgAdmin(ctx, tx, p, org); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organization_members WHERE organization_id=$1 AND issuer=$2 AND subject=$3)`, org, target.Issuer, target.Subject).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		rows, err := tx.Query(ctx, `SELECT revision,enabled,previous_role,new_role,reason,actor_issuer,actor_subject,revoked_keys,removed_project_grants,revoked_auditors,canceled_transfers,created_at,removed_department_grants FROM organization_member_lifecycle WHERE organization_id=$1 AND issuer=$2 AND subject=$3 AND revision>$4 ORDER BY revision LIMIT 51`, org, target.Issuer, target.Subject, after)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v MemberLifecycleEvent
			if err := rows.Scan(&v.Revision, &v.Enabled, &v.PreviousRole, &v.NewRole, &v.Reason, &v.Actor.Issuer, &v.Actor.Subject, &v.RevokedKeys, &v.RemovedProjectGrants, &v.RevokedAuditors, &v.CanceledTransfers, &v.CreatedAt, &v.RemovedDepartmentGrants); err != nil {
				return err
			}
			items = append(items, v)
		}
		return rows.Err()
	})
	return
}
