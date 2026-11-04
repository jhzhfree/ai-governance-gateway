package control

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jhzhfree/ai-governance-gateway/internal/contentaudit"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
)

func (s *Store) WithContentAudit(service *contentaudit.Service) *Store { s.content = service; return s }
func (s *Store) contentService() *contentaudit.Service {
	if s.content != nil {
		return s.content
	}
	return contentaudit.New(s.pool, nil)
}

type ContentPolicy struct {
	contentaudit.Policy
	Configured bool `json:"configured"`
}

func (s *Store) ContentPolicy(ctx context.Context, p identity.Principal, org, project string) (v ContentPolicy, err error) {
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := orgAdmin(ctx, tx, p, org); err != nil {
			return err
		}
		if _, err := projectRole(ctx, tx, p, org, project); err != nil {
			return err
		}
		var err error
		v.Policy, err = contentaudit.ReadPolicy(ctx, tx, org, project)
		v.Configured = s.contentService().Configured()
		return err
	})
	return
}
func (s *Store) SetContentPolicy(ctx context.Context, p identity.Principal, org, project string, policy contentaudit.Policy) error {
	if !policy.Valid() {
		return ErrInvalid
	}
	return transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := orgAdmin(ctx, tx, p, org); err != nil {
			return err
		}
		if _, err := projectRole(ctx, tx, p, org, project); err != nil {
			return err
		}
		if policy.Enabled && !s.billingReady(ctx, tx) {
			return errBillingUnavailable
		}
		if err := s.contentService().SetPolicy(ctx, tx, org, project, policy); err != nil {
			return err
		}
		return audit(ctx, tx, org, p, "content_audit.policy_changed", project)
	})
}

type ContentAuditor struct {
	identity.Principal
	Active bool `json:"active"`
}

func (s *Store) GrantContentAuditor(ctx context.Context, p identity.Principal, org, project string, target ContentAuditor) error {
	if !validPrincipal(target.Principal) {
		return ErrInvalid
	}
	return transaction(ctx, s.pool, func(tx pgx.Tx) error {
		role, err := orgRole(ctx, tx, p, org)
		if err != nil {
			return err
		}
		if role != "owner" {
			return identity.ErrForbidden
		}
		if _, err := projectRole(ctx, tx, p, org, project); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO content_audit_auditors(organization_id,project_id,issuer,subject,active) VALUES($1,$2,$3,$4,$5) ON CONFLICT(organization_id,project_id,issuer,subject) DO UPDATE SET active=excluded.active`, org, project, target.Issuer, target.Subject, target.Active); err != nil {
			return err
		}
		return audit(ctx, tx, org, p, "content_audit.auditor_changed", project)
	})
}
func contentReader(ctx context.Context, tx pgx.Tx, p identity.Principal, org, project string) error {
	var active bool
	err := tx.QueryRow(ctx, `SELECT active FROM content_audit_auditors WHERE organization_id=$1 AND project_id=$2 AND issuer=$3 AND subject=$4 FOR SHARE`, org, project, p.Issuer, p.Subject).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || (!active && err == nil) {
		return identity.ErrForbidden
	}
	return err
}
func contentMetadata(ctx context.Context, tx pgx.Tx, p identity.Principal, org, project string) error {
	if err := contentReader(ctx, tx, p, org, project); err == nil {
		return nil
	} else if !errors.Is(err, identity.ErrForbidden) {
		return err
	}
	if err := orgAdmin(ctx, tx, p, org); err != nil {
		return err
	}
	_, err := projectRole(ctx, tx, p, org, project)
	return err
}
func (s *Store) ContentRecords(ctx context.Context, p identity.Principal, org, project, after string, limit int) (items []contentaudit.Meta, next string, err error) {
	items = []contentaudit.Meta{}
	stamp, id, e := requestCursor(after)
	if e != nil || limit < 1 || limit > 100 {
		return items, "", ErrInvalid
	}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := contentMetadata(ctx, tx, p, org, project); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+contentaudit.Columns+` FROM content_audit_records WHERE organization_id=$1 AND project_id=$2 AND expires_at>clock_timestamp() AND state<>'expired' AND ($3='' OR (created_at,request_id)<($4,$3)) ORDER BY created_at DESC,request_id DESC LIMIT $5`, org, project, id, stamp, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, e := contentaudit.ScanMeta(rows)
			if e != nil {
				return e
			}
			items = append(items, v)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(items) > limit {
			items = items[:limit]
			last := items[len(items)-1]
			next = (RequestRecord{RequestID: last.RequestID, CreatedAt: last.CreatedAt}).Cursor()
		}
		return nil
	})
	return
}
func (s *Store) ReadContent(ctx context.Context, p identity.Principal, org, project, id, reason string) (v contentaudit.Detail, err error) {
	if !s.billingReady(ctx, nil) {
		return v, errBillingUnavailable
	}
	if len(id) < 1 || len(id) > 256 || len(reason) < 1 || len(reason) > 256 || strings.TrimSpace(reason) != reason {
		return v, ErrInvalid
	}
	// Access log commits before decryption. A later decryption error still leaves the attempt.
	var meta contentaudit.Meta
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := contentReader(ctx, tx, p, org, project); err != nil {
			return err
		}
		var err error
		meta, err = contentaudit.ScanMeta(tx.QueryRow(ctx, `SELECT `+contentaudit.Columns+` FROM content_audit_records WHERE organization_id=$1 AND project_id=$2 AND request_id=$3 AND expires_at>clock_timestamp() AND state<>'expired'`, org, project, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO content_audit_access_log(organization_id,project_id,request_id,actor_issuer,actor_subject,reason) VALUES($1,$2,$3,$4,$5,$6)`, org, project, id, p.Issuer, p.Subject, reason)
		return err
	})
	if err != nil {
		return v, err
	}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := contentReader(ctx, tx, p, org, project); err != nil {
			return err
		}
		var err error
		meta, err = contentaudit.ScanMeta(tx.QueryRow(ctx, `SELECT `+contentaudit.Columns+` FROM content_audit_records WHERE organization_id=$1 AND project_id=$2 AND request_id=$3 AND expires_at>clock_timestamp() AND state<>'expired' FOR SHARE`, org, project, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		v, err = s.contentService().Decrypt(ctx, tx, meta)
		return err
	})
	return
}

type ContentAccess struct {
	ID        int64              `json:"id"`
	Actor     identity.Principal `json:"actor"`
	RequestID string             `json:"request_id"`
	Reason    string             `json:"reason"`
	CreatedAt time.Time          `json:"created_at"`
}

func (s *Store) ContentAccessLog(ctx context.Context, p identity.Principal, org, project string, after int64, limit int) (items []ContentAccess, err error) {
	items = []ContentAccess{}
	if after < 0 || limit < 1 || limit > 101 {
		return items, ErrInvalid
	}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := orgAdmin(ctx, tx, p, org); err != nil {
			return err
		}
		if _, err := projectRole(ctx, tx, p, org, project); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id,actor_issuer,actor_subject,request_id,reason,created_at FROM content_audit_access_log WHERE organization_id=$1 AND project_id=$2 AND id>$3 ORDER BY id LIMIT $4`, org, project, after, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a ContentAccess
			if err := rows.Scan(&a.ID, &a.Actor.Issuer, &a.Actor.Subject, &a.RequestID, &a.Reason, &a.CreatedAt); err != nil {
				return err
			}
			items = append(items, a)
		}
		return rows.Err()
	})
	return
}

func (s *Store) ContentAuditors(ctx context.Context, p identity.Principal, org, project, after string, limit int) (items []ContentAuditor, next string, err error) {
	items = []ContentAuditor{}
	cursor, e := memberCursor(after)
	if e != nil || limit < 1 || limit > 100 {
		return items, "", ErrInvalid
	}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := orgAdmin(ctx, tx, p, org); err != nil {
			return err
		}
		if _, err := projectRole(ctx, tx, p, org, project); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT issuer,subject,active FROM content_audit_auditors WHERE organization_id=$1 AND project_id=$2 AND ($3='' OR (issuer,subject)>($3,$4)) ORDER BY issuer,subject LIMIT $5`, org, project, cursor.Issuer, cursor.Subject, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v ContentAuditor
			if err := rows.Scan(&v.Issuer, &v.Subject, &v.Active); err != nil {
				return err
			}
			items = append(items, v)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(items) > limit {
			items = items[:limit]
			last := items[len(items)-1]
			next = (MemberRecord{Principal: last.Principal}).Cursor()
		}
		return nil
	})
	return
}
