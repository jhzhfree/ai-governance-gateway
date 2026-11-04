package control

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
)

type OwnershipTransfer struct {
	ID         string             `json:"id"`
	Owner      identity.Principal `json:"owner"`
	Target     identity.Principal `json:"target"`
	TargetRole string             `json:"target_role"`
	Reason     string             `json:"reason"`
	CreatedAt  time.Time          `json:"created_at"`
	ExpiresAt  time.Time          `json:"expires_at"`
}

const ownershipColumns = `id,owner_issuer,owner_subject,target_issuer,target_subject,target_role,reason,created_at,expires_at`

func scanOwnership(row interface{ Scan(...any) error }) (v OwnershipTransfer, err error) {
	err = row.Scan(&v.ID, &v.Owner.Issuer, &v.Owner.Subject, &v.Target.Issuer, &v.Target.Subject, &v.TargetRole, &v.Reason, &v.CreatedAt, &v.ExpiresAt)
	return
}
func (s *Store) ownershipActor(ctx context.Context, tx pgx.Tx, p identity.Principal) error {
	if s.localIdentity != nil {
		return s.localIdentity.AuthorizeActor(ctx, tx, p)
	}
	return nil
}

// PendingOwnership exposes a handoff only to its owner and intended recipient.
// Other organization members get null, without seeing another member's identity.
func (s *Store) PendingOwnership(ctx context.Context, p identity.Principal, org string) (v *OwnershipTransfer, err error) {
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := orgRole(ctx, tx, p, org); err != nil {
			return err
		}
		item, err := scanOwnership(tx.QueryRow(ctx, `SELECT `+ownershipColumns+` FROM organization_ownership_transfers WHERE organization_id=$1 AND state='pending' AND expires_at>clock_timestamp() AND ((owner_issuer=$2 AND owner_subject=$3) OR (target_issuer=$2 AND target_subject=$3))`, org, p.Issuer, p.Subject))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		v = &item
		return nil
	})
	return
}
func (s *Store) RequestOwnership(ctx context.Context, p identity.Principal, org string, target identity.Principal, expected, reason string) (v OwnershipTransfer, err error) {
	if !validPrincipal(target) || target == p || (expected != "admin" && expected != "developer" && expected != "viewer") || utf8.RuneCountInString(reason) < 1 || utf8.RuneCountInString(reason) > 256 || strings.TrimSpace(reason) != reason {
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
		var current string
		if err := tx.QueryRow(ctx, `SELECT role FROM organization_members WHERE organization_id=$1 AND issuer=$2 AND subject=$3 AND enabled`, org, target.Issuer, target.Subject).Scan(&current); errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if current != expected {
			return ErrMemberConflict
		}
		// Membership writers share memberLock; read membership before checking
		// local account status so another organization cannot probe identities.
		if err := s.ownershipActor(ctx, tx, target); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE organization_ownership_transfers SET state='expired',finished_at=clock_timestamp() WHERE organization_id=$1 AND state='pending' AND expires_at<=clock_timestamp()`, org); err != nil {
			return err
		}
		var pending bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organization_ownership_transfers WHERE organization_id=$1 AND state='pending')`, org).Scan(&pending); err != nil {
			return err
		}
		if pending {
			return ErrMemberConflict
		}
		v, err = scanOwnership(tx.QueryRow(ctx, `INSERT INTO organization_ownership_transfers(id,organization_id,owner_issuer,owner_subject,target_issuer,target_subject,target_role,reason,state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending') RETURNING `+ownershipColumns, rand.Text(), org, p.Issuer, p.Subject, target.Issuer, target.Subject, expected, reason))
		if err != nil {
			return err
		}
		return audit(ctx, tx, org, p, "organization.ownership_requested", v.ID)
	})
	return
}
func (s *Store) FinishOwnership(ctx context.Context, p identity.Principal, org, id string, accept bool) error {
	if len(id) < 1 || len(id) > 128 {
		return ErrInvalid
	}
	return transaction(ctx, s.pool, func(tx pgx.Tx) error {
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
		v, err := scanOwnership(tx.QueryRow(ctx, `SELECT `+ownershipColumns+` FROM organization_ownership_transfers WHERE organization_id=$1 AND id=$2 AND state='pending' FOR UPDATE`, org, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrMemberConflict
		}
		if err != nil {
			return err
		}
		if !accept {
			if role != "owner" || p != v.Owner {
				return identity.ErrForbidden
			}
			if _, err := tx.Exec(ctx, `UPDATE organization_ownership_transfers SET state='canceled',finished_at=clock_timestamp() WHERE id=$1`, id); err != nil {
				return err
			}
			return audit(ctx, tx, org, p, "organization.ownership_canceled", id)
		}
		if p != v.Target {
			return identity.ErrForbidden
		}
		if err := s.ownershipActor(ctx, tx, v.Owner); err != nil {
			return err
		}
		var expired bool
		if err := tx.QueryRow(ctx, `SELECT expires_at<=clock_timestamp() FROM organization_ownership_transfers WHERE id=$1`, id).Scan(&expired); err != nil {
			return err
		}
		if expired || role != v.TargetRole {
			return ErrMemberConflict
		}
		var ownerRole string
		if err := tx.QueryRow(ctx, `SELECT role FROM organization_members WHERE organization_id=$1 AND issuer=$2 AND subject=$3 FOR UPDATE`, org, v.Owner.Issuer, v.Owner.Subject).Scan(&ownerRole); err != nil {
			return err
		}
		if ownerRole != "owner" {
			return ErrMemberConflict
		}
		if _, err := tx.Exec(ctx, `UPDATE organization_members SET role='admin' WHERE organization_id=$1 AND issuer=$2 AND subject=$3`, org, v.Owner.Issuer, v.Owner.Subject); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE organization_members SET role='owner' WHERE organization_id=$1 AND issuer=$2 AND subject=$3`, org, p.Issuer, p.Subject); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE organization_ownership_transfers SET state='accepted',finished_at=clock_timestamp() WHERE id=$1`, id); err != nil {
			return err
		}
		return audit(ctx, tx, org, p, "organization.ownership_accepted", id)
	})
}
