package control

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
)

var ErrMemberConflict = errors.New("member role changed")

type MemberRecord struct {
	identity.Principal
	Role          string  `json:"role"`
	ProjectRole   *string `json:"project_role"`
	EffectiveRole string  `json:"effective_role"`
}

func (m MemberRecord) Cursor() string {
	return base64.RawURLEncoding.EncodeToString([]byte(m.Issuer + "\x00" + m.Subject))
}
func memberCursor(cursor string) (identity.Principal, error) {
	var p identity.Principal
	if cursor == "" {
		return p, nil
	}
	if len(cursor) > 4096 {
		return p, ErrInvalid
	}
	data, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || strings.Count(string(data), "\x00") != 1 {
		return p, ErrInvalid
	}
	p.Issuer, p.Subject, _ = strings.Cut(string(data), "\x00")
	if !validPrincipal(p) || (MemberRecord{Principal: p}).Cursor() != cursor {
		return p, ErrInvalid
	}
	return p, nil
}

func (s *Store) ListMembers(ctx context.Context, p identity.Principal, org, project, after string, limit int) (items []MemberRecord, next string, err error) {
	items = []MemberRecord{}
	cursor, e := memberCursor(after)
	if e != nil || limit < 1 || limit > 100 {
		return items, "", ErrInvalid
	}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := orgAdmin(ctx, tx, p, org); err != nil {
			return err
		}
		if project != "" {
			if _, err := projectRole(ctx, tx, p, org, project); err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, `SELECT m.issuer,m.subject,m.role,g.role FROM organization_members m LEFT JOIN project_members g ON g.organization_id=m.organization_id AND g.project_id=$2 AND g.issuer=m.issuer AND g.subject=m.subject WHERE m.organization_id=$1 AND ($3='' OR (m.issuer,m.subject)>($3,$4)) ORDER BY m.issuer,m.subject LIMIT $5`, org, project, cursor.Issuer, cursor.Subject, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m MemberRecord
			if err := rows.Scan(&m.Issuer, &m.Subject, &m.Role, &m.ProjectRole); err != nil {
				return err
			}
			m.EffectiveRole = m.Role
			if project != "" {
				switch m.Role {
				case "owner", "admin":
					m.EffectiveRole = "admin"
				case "viewer":
					m.EffectiveRole = "viewer"
				default:
					m.EffectiveRole = "none"
					if m.ProjectRole != nil {
						m.EffectiveRole = *m.ProjectRole
					}
				}
			}
			items = append(items, m)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(items) > limit {
			items = items[:limit]
			next = items[len(items)-1].Cursor()
		}
		return nil
	})
	return
}

// Serialize membership writers before taking role SHARE locks, including legacy PUTs.
func memberLock(ctx context.Context, tx pgx.Tx, org string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "aigw:members:"+org)
	return err
}
func validMember(m Member) bool {
	return validPrincipal(m.Principal) && (m.Role == "admin" || m.Role == "developer" || m.Role == "viewer")
}
func (s *Store) ChangeMember(ctx context.Context, p identity.Principal, org, project string, m Member, expected *string) error {
	if !validMember(m) || (expected != nil && *expected != "" && *expected != "admin" && *expected != "developer" && *expected != "viewer" && *expected != "owner") {
		return ErrInvalid
	}
	return transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := memberLock(ctx, tx, org); err != nil {
			return err
		}
		role, err := orgRole(ctx, tx, p, org)
		if err != nil {
			return err
		}
		if project == "" {
			if role != "owner" {
				return identity.ErrForbidden
			}
		} else {
			if role != "owner" && role != "admin" {
				return identity.ErrForbidden
			}
			if _, err := projectRole(ctx, tx, p, org, project); err != nil {
				return err
			}
		}
		var current string
		if project == "" {
			err = tx.QueryRow(ctx, `SELECT role FROM organization_members WHERE organization_id=$1 AND issuer=$2 AND subject=$3 FOR UPDATE`, org, m.Issuer, m.Subject).Scan(&current)
		} else {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organization_members WHERE organization_id=$1 AND issuer=$2 AND subject=$3)`, org, m.Issuer, m.Subject).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return ErrNotFound
			}
			err = tx.QueryRow(ctx, `SELECT role FROM project_members WHERE organization_id=$1 AND project_id=$2 AND issuer=$3 AND subject=$4 FOR UPDATE`, org, project, m.Issuer, m.Subject).Scan(&current)
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if project == "" && current == "owner" {
			return identity.ErrForbidden
		}
		if expected != nil && *expected != current {
			return ErrMemberConflict
		}
		if current == m.Role {
			return nil
		}
		if project == "" {
			_, err = tx.Exec(ctx, `INSERT INTO organization_members(organization_id,issuer,subject,role) VALUES($1,$2,$3,$4) ON CONFLICT(organization_id,issuer,subject) DO UPDATE SET role=excluded.role`, org, m.Issuer, m.Subject, m.Role)
		} else {
			_, err = tx.Exec(ctx, `INSERT INTO project_members(organization_id,project_id,issuer,subject,role) VALUES($1,$2,$3,$4,$5) ON CONFLICT(organization_id,project_id,issuer,subject) DO UPDATE SET role=excluded.role`, org, project, m.Issuer, m.Subject, m.Role)
		}
		if err != nil {
			return err
		}
		action, resource := "organization.member_changed", m.Issuer+"#"+m.Subject
		if project != "" {
			action, resource = "project.member_changed", project
		}
		return audit(ctx, tx, org, p, action, resource)
	})
}
func (s *Store) RemoveProjectMember(ctx context.Context, p identity.Principal, org, project string, target identity.Principal, expected string) error {
	if !validPrincipal(target) || (expected != "admin" && expected != "developer" && expected != "viewer") {
		return ErrInvalid
	}
	return transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := memberLock(ctx, tx, org); err != nil {
			return err
		}
		if err := orgAdmin(ctx, tx, p, org); err != nil {
			return err
		}
		if _, err := projectRole(ctx, tx, p, org, project); err != nil {
			return err
		}
		var role string
		err := tx.QueryRow(ctx, `SELECT role FROM project_members WHERE organization_id=$1 AND project_id=$2 AND issuer=$3 AND subject=$4 FOR UPDATE`, org, project, target.Issuer, target.Subject).Scan(&role)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if role != expected {
			return ErrMemberConflict
		}
		if _, err := tx.Exec(ctx, `DELETE FROM project_members WHERE organization_id=$1 AND project_id=$2 AND issuer=$3 AND subject=$4`, org, project, target.Issuer, target.Subject); err != nil {
			return err
		}
		return audit(ctx, tx, org, p, "project.member_removed", project)
	})
}
