package contentaudit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5"
)

type Checkpoint struct {
	Sequence int64  `json:"sequence"`
	Hash     string `json:"hash"`
	Valid    bool   `json:"valid"`
}

// Verify detects chain gaps/edits against the stored head. External anchoring is separate.
func (s *Service) Verify(ctx context.Context, org string) (v Checkpoint, err error) {
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		var expected int64
		var head []byte
		if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT sequence FROM content_audit_heads WHERE organization_id=$1),0),COALESCE((SELECT hash FROM content_audit_heads WHERE organization_id=$1),decode(repeat('00',32),'hex'))`, org).Scan(&expected, &head); err != nil {
			return err
		}
		previous := make([]byte, 32)
		metaColumns := "r." + strings.ReplaceAll(Columns, ",", ",r.")
		rows, err := tx.Query(ctx, `SELECT c.sequence,c.request_id,c.event,c.digest,c.previous,c.hash,p.sha256,p.ciphertext,p.nonce,p.observed_bytes,p.truncated,`+metaColumns+` FROM content_audit_chain c JOIN content_audit_records r ON r.request_id=c.request_id AND r.organization_id=c.organization_id AND (r.state<>'expired' OR (EXISTS(SELECT 1 FROM content_audit_chain q WHERE q.organization_id=r.organization_id AND q.request_id=r.request_id AND q.event='purge') AND NOT EXISTS(SELECT 1 FROM content_audit_payloads keep WHERE keep.request_id=r.request_id))) LEFT JOIN content_audit_payloads p ON p.request_id=c.request_id AND p.created_at=r.created_at AND p.direction=CASE c.event WHEN 'start' THEN 'request' WHEN 'seal' THEN 'response' ELSE 'none' END WHERE c.organization_id=$1 ORDER BY c.sequence`, org)
		if err != nil {
			return err
		}
		defer rows.Close()
		valid := true
		for rows.Next() {
			var seq int64
			var id, event string
			var digest, prev, next []byte
			var payload encrypted
			var size *int64
			var truncated *bool
			var meta Meta
			if err := rows.Scan(&seq, &id, &event, &digest, &prev, &next, &payload.sha, &payload.cipher, &payload.nonce, &size, &truncated, &meta.PolicyRevision, &meta.ConsentRef, &meta.MaxBytes, &meta.RetentionDays, &meta.RequestID, &meta.Organization, &meta.Project, &meta.Model, &meta.KeyPrefix, &meta.State, &meta.Status, &meta.CreatedAt, &meta.ExpiresAt, &meta.FinishedAt); err != nil {
				return err
			}
			encoded, _ := json.Marshal(struct {
				Org, ID, Event   string
				Sequence         int64
				Previous, Digest string
			}{org, id, event, seq, hex.EncodeToString(prev), hex.EncodeToString(digest)})
			hash := sha256.Sum256(encoded)
			valid = valid && seq == v.Sequence+1 && equal(prev, previous) && equal(hash[:], next)
			if event == "purge" {
				valid = valid && meta.State == "expired" && equal(digestParts([]byte("expired")), digest)
			} else if meta.State != "expired" {
				if size == nil || truncated == nil || payload.cipher == nil {
					valid = false
				} else {
					direction := "request"
					if event == "seal" {
						direction = "response"
					}
					parts := [][]byte{payload.sha, payload.cipher, payload.nonce, aad(meta, direction, payload.sha, *size, *truncated)}
					if event == "seal" {
						parts = append(parts, []byte(meta.State), []byte{byte(meta.Status >> 8), byte(meta.Status)})
					}
					valid = valid && equal(digestParts(parts...), digest)
				}
			}
			v.Sequence = seq
			previous = next
		}
		if err := rows.Err(); err != nil {
			return err
		}
		v.Hash = hex.EncodeToString(previous)
		v.Valid = valid && v.Sequence == expected && equal(previous, head)
		return nil
	})
	return
}
func (s *Service) Purge(ctx context.Context, limit int) (n int, err error) {
	if limit < 1 || limit > 1000 {
		return 0, ErrConflict
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT request_id,organization_id FROM content_audit_records WHERE expires_at<=clock_timestamp() AND state<>'expired' ORDER BY organization_id,request_id LIMIT $1 FOR UPDATE SKIP LOCKED`, limit)
		if err != nil {
			return err
		}
		type target struct{ id, org string }
		var targets []target
		for rows.Next() {
			var t target
			if err := rows.Scan(&t.id, &t.org); err != nil {
				rows.Close()
				return err
			}
			targets = append(targets, t)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, t := range targets {
			if _, err := tx.Exec(ctx, `DELETE FROM content_audit_payloads WHERE request_id=$1`, t.id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE content_audit_records SET state='expired' WHERE request_id=$1`, t.id); err != nil {
				return err
			}
			if err := appendChain(ctx, tx, t.org, t.id, "purge", []byte("expired")); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	if err != nil {
		n = 0
	}
	return
}

// Ready refuses a misleading healthy state if active policies or retained data lack keys.
func (s *Service) Ready(ctx context.Context) bool {
	var enabled bool
	var ids []string
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM content_audit_policies WHERE enabled),ARRAY(SELECT DISTINCT key_id FROM content_audit_records WHERE state<>'expired' AND expires_at>clock_timestamp())`).Scan(&enabled, &ids)
	if err != nil {
		return false
	}
	if (enabled || len(ids) > 0) && !s.Configured() {
		return false
	}
	for _, id := range ids {
		if s.ring.keys[id] == nil {
			return false
		}
	}
	return true
}
