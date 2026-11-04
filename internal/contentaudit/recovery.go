package contentaudit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

func number(v int64) []byte { var b [8]byte; binary.BigEndian.PutUint64(b[:], uint64(v)); return b[:] }
func partAAD(meta Meta, seq int) []byte {
	return digestParts(aad(meta, "checkpoint", nil, 0, false), number(int64(seq)))
}

// Domain-separated per-request journal keys avoid consuming one organization's
// AES-GCM nonce budget for every streaming fragment across all of its requests.
func checkpointKey(dek []byte, meta Meta, purpose string) []byte {
	kdf := hmac.New(sha256.New, dek)
	kdf.Write(digestParts([]byte("aigw:checkpoint-key:v1:"+purpose), []byte(meta.Organization), []byte(meta.RequestID)))
	return kdf.Sum(nil)
}
func checkpointProof(key []byte, meta Meta, head, sha []byte, observed int64, captured, parts int) []byte {
	mac := hmac.New(sha256.New, checkpointKey(key, meta, "proof"))
	mac.Write(digestParts(aad(meta, "checkpoint-proof", nil, 0, false), head, sha, number(observed), number(int64(captured)), number(int64(parts))))
	return mac.Sum(nil)
}

// Checkpoint commits an encrypted append-only bounded prefix and authenticated
// observed digest before forwarding. Capture is serial-use, like ResponseWriter.
// After any failure the caller must stop forwarding and must not replay the data.
func (c *Capture) Checkpoint(ctx context.Context, data []byte) error {
	if c == nil {
		return nil
	}
	if c.finished {
		return ErrConflict
	}
	c.Observe(data)
	seq := c.checkpointParts
	head := c.checkpointHead
	var cipher, nonce []byte
	if len(c.prefix) > c.checkpointBytes {
		seq++
		var err error
		cipher, nonce, err = seal(checkpointKey(c.dek, c.meta, "parts"), c.prefix[c.checkpointBytes:], partAAD(c.meta, seq))
		if err != nil {
			return err
		}
		head = digestParts(head, cipher, nonce, partAAD(c.meta, seq))
	}
	sha := c.hash.Sum(nil)
	proof := checkpointProof(c.dek, c.meta, head, sha, c.observed, len(c.prefix), seq)
	err := pgx.BeginFunc(ctx, c.service.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL synchronous_commit = on"); err != nil {
			return err
		}
		var state string
		var expired bool
		if err := tx.QueryRow(ctx, `SELECT state,expires_at<=clock_timestamp() FROM content_audit_records WHERE request_id=$1 FOR UPDATE`, c.meta.RequestID).Scan(&state, &expired); err != nil {
			return err
		}
		if state != "capturing" || expired {
			return ErrConflict
		}
		tag, err := tx.Exec(ctx, `UPDATE content_audit_checkpoints SET sha256=$3,observed_bytes=$4,captured_bytes=$5,parts=$6,head=$7,proof=$8,updated_at=clock_timestamp(),lease_until=clock_timestamp()+interval '5 minutes' WHERE request_id=$1 AND owner_id=$2 AND lease_until>clock_timestamp() AND parts=$9`, c.meta.RequestID, c.owner, sha, c.observed, len(c.prefix), seq, head, proof, c.checkpointParts)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		if cipher != nil {
			_, err = tx.Exec(ctx, `INSERT INTO content_audit_checkpoint_parts(request_id,sequence,ciphertext,nonce) VALUES($1,$2,$3,$4)`, c.meta.RequestID, seq, cipher, nonce)
		}
		return err
	})
	if err == nil {
		c.checkpointParts = seq
		c.checkpointBytes = len(c.prefix)
		c.checkpointHead = head
	}
	return err
}

type recoveryTarget struct {
	meta             Meta
	keyID            string
	sha, head, proof []byte
	observed         int64
	captured, parts  int
}

// recoverPayload authenticates the bounded journal, then re-encrypts one final
// response payload in the existing audit format. No hash internal state is stored.
func (s *Service) recoverPayload(ctx context.Context, tx pgx.Tx, t recoveryTarget) (p encrypted, err error) {
	if t.captured > t.meta.MaxBytes || t.parts > t.captured {
		return p, ErrUnavailable
	}
	key, err := s.dek(ctx, tx, t.meta.Organization, t.keyID, false)
	if err != nil {
		return p, err
	}
	defer func() {
		for i := range key {
			key[i] = 0
		}
	}()
	if !hmac.Equal(checkpointProof(key, t.meta, t.head, t.sha, t.observed, t.captured, t.parts), t.proof) {
		return p, ErrUnavailable
	}
	partKey := checkpointKey(key, t.meta, "parts")
	defer func() {
		for i := range partKey {
			partKey[i] = 0
		}
	}()
	rows, err := tx.Query(ctx, `SELECT sequence,ciphertext,nonce FROM content_audit_checkpoint_parts WHERE request_id=$1 ORDER BY sequence`, t.meta.RequestID)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	prefix := make([]byte, 0, t.captured)
	defer func() {
		for i := range prefix {
			prefix[i] = 0
		}
	}()
	head := make([]byte, 32)
	seq := 0
	for rows.Next() {
		var n int
		var cipher, nonce []byte
		if err := rows.Scan(&n, &cipher, &nonce); err != nil {
			return p, err
		}
		seq++
		if n != seq || n > t.parts {
			return p, ErrUnavailable
		}
		plain, err := open(partKey, cipher, nonce, partAAD(t.meta, n))
		if err != nil {
			return p, err
		}
		if len(prefix)+len(plain) > t.captured {
			for i := range plain {
				plain[i] = 0
			}
			return p, ErrUnavailable
		}
		prefix = append(prefix, plain...)
		for i := range plain {
			plain[i] = 0
		}
		head = digestParts(head, cipher, nonce, partAAD(t.meta, n))
	}
	if err := rows.Err(); err != nil {
		return p, err
	}
	if seq != t.parts || len(prefix) != t.captured || !equal(head, t.head) {
		return p, ErrUnavailable
	}
	if int64(len(prefix)) == t.observed {
		sum := sha256.Sum256(prefix)
		if !equal(sum[:], t.sha) {
			return p, ErrUnavailable
		}
	}
	return encrypt(t.meta, "response", key, prefix, t.sha, t.observed)
}

// Recover seals only expired leases as incomplete, status=0 (unknown), and does
// not replay inference or mutate budgets. Live work is fenced by the record lock.
func (s *Service) Recover(ctx context.Context, limit int) (n int, err error) {
	if limit < 1 || limit > 100 || !s.Configured() {
		return 0, ErrConflict
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL synchronous_commit = on"); err != nil {
			return err
		}
		cols := "r." + strings.ReplaceAll(Columns, ",", ",r.")
		rows, err := tx.Query(ctx, `SELECT `+cols+`,r.key_id,c.sha256,c.observed_bytes,c.captured_bytes,c.parts,c.head,c.proof
 FROM content_audit_records r JOIN content_audit_checkpoints c USING(request_id)
 WHERE r.state='capturing' AND r.expires_at>clock_timestamp() AND c.lease_until<=clock_timestamp()
 ORDER BY r.organization_id,r.request_id LIMIT $1 FOR UPDATE OF r SKIP LOCKED`, limit)
		if err != nil {
			return err
		}
		var targets []recoveryTarget
		for rows.Next() {
			var t recoveryTarget
			if err := rows.Scan(&t.meta.PolicyRevision, &t.meta.ConsentRef, &t.meta.MaxBytes, &t.meta.RetentionDays, &t.meta.RequestID, &t.meta.Organization, &t.meta.Project, &t.meta.Model, &t.meta.KeyPrefix, &t.meta.State, &t.meta.Status, &t.meta.CreatedAt, &t.meta.ExpiresAt, &t.meta.FinishedAt, &t.keyID, &t.sha, &t.observed, &t.captured, &t.parts, &t.head, &t.proof); err != nil {
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
			// Re-read in a fresh READ COMMITTED statement after the record lock. The
			// candidate join may have observed an expired checkpoint immediately before
			// its owner committed a lease renewal without changing the parent row.
			err := tx.QueryRow(ctx, `SELECT sha256,observed_bytes,captured_bytes,parts,head,proof FROM content_audit_checkpoints WHERE request_id=$1 AND lease_until<=clock_timestamp() FOR UPDATE`, t.meta.RequestID).Scan(&t.sha, &t.observed, &t.captured, &t.parts, &t.head, &t.proof)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			p, err := s.recoverPayload(ctx, tx, t)
			if err != nil {
				return err
			}
			if err := insertPayload(ctx, tx, t.meta, p); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE content_audit_records SET state='incomplete',status=0,finished_at=clock_timestamp() WHERE request_id=$1`, t.meta.RequestID); err != nil {
				return err
			}
			if err := appendChain(ctx, tx, t.meta.Organization, t.meta.RequestID, "seal", p.sha, p.cipher, p.nonce, aad(t.meta, "response", p.sha, p.bytes, p.truncated), []byte("incomplete"), []byte{0, 0}); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM content_audit_checkpoints WHERE request_id=$1`, t.meta.RequestID); err != nil {
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
