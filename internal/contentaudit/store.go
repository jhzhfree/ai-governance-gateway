package contentaudit

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var consentID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

type Policy struct {
	Enabled       bool   `json:"enabled"`
	RetentionDays int    `json:"retention_days"`
	MaxBytes      int    `json:"max_bytes"`
	ConsentRef    string `json:"consent_ref"`
	Revision      int64  `json:"revision"`
}

func DefaultPolicy() Policy { return Policy{RetentionDays: 30, MaxBytes: 65536} }
func (p Policy) Valid() bool {
	return p.RetentionDays >= 1 && p.RetentionDays <= 90 && p.MaxBytes >= 1024 && p.MaxBytes <= 262144 && p.Revision >= 0 && p.Revision < 9007199254740991 && len(p.ConsentRef) <= 128 && (!p.Enabled || consentID.MatchString(p.ConsentRef))
}

type Service struct {
	pool  *pgxpool.Pool
	ring  *Keyring
	slots chan struct{}
}

// MaxActiveCaptures bounds live encrypted-audit captures per service instance.
// At the maximum policy size, retained response prefixes total at most 8 MiB
// (32 * 256 KiB), excluding transient encryption and transport buffers.
const MaxActiveCaptures = 32

func New(pool *pgxpool.Pool, ring *Keyring) *Service {
	return &Service{pool: pool, ring: ring, slots: make(chan struct{}, MaxActiveCaptures)}
}
func (s *Service) Configured() bool { return s != nil && s.ring != nil }
func ReadPolicy(ctx context.Context, tx pgx.Tx, org, project string) (p Policy, err error) {
	p = DefaultPolicy()
	err = tx.QueryRow(ctx, `SELECT enabled,retention_days,max_bytes,consent_ref,revision FROM content_audit_policies WHERE organization_id=$1 AND project_id=$2`, org, project).Scan(&p.Enabled, &p.RetentionDays, &p.MaxBytes, &p.ConsentRef, &p.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return
}
func (s *Service) SetPolicy(ctx context.Context, tx pgx.Tx, org, project string, p Policy) error {
	if !p.Valid() {
		return ErrConflict
	}
	if p.Enabled && !s.Configured() {
		return ErrUnavailable
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "aigw:content-policy:"+org+":"+project); err != nil {
		return err
	}
	current, err := ReadPolicy(ctx, tx, org, project)
	if err != nil {
		return err
	}
	if current.Revision != p.Revision {
		return ErrConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO content_audit_policies(organization_id,project_id,enabled,retention_days,max_bytes,consent_ref,revision) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(organization_id,project_id) DO UPDATE SET enabled=excluded.enabled,retention_days=excluded.retention_days,max_bytes=excluded.max_bytes,consent_ref=excluded.consent_ref,revision=excluded.revision`, org, project, p.Enabled, p.RetentionDays, p.MaxBytes, p.ConsentRef, p.Revision+1)
	return err
}

type Meta struct {
	PolicyRevision int64      `json:"policy_revision"`
	ConsentRef     string     `json:"consent_ref"`
	MaxBytes       int        `json:"max_bytes"`
	RetentionDays  int        `json:"retention_days"`
	RequestID      string     `json:"request_id"`
	Organization   string     `json:"organization_id"`
	Project        string     `json:"project_id"`
	Model          string     `json:"model"`
	KeyPrefix      string     `json:"key_prefix"`
	State          string     `json:"state"`
	Status         int        `json:"status"`
	CreatedAt      time.Time  `json:"created_at"`
	ExpiresAt      time.Time  `json:"expires_at"`
	FinishedAt     *time.Time `json:"finished_at"`
}

const Columns = `policy_revision,consent_ref,max_bytes,retention_days,request_id,organization_id,project_id,model,key_prefix,state,status,created_at,expires_at,finished_at`

func ScanMeta(row interface{ Scan(...any) error }) (v Meta, err error) {
	err = row.Scan(&v.PolicyRevision, &v.ConsentRef, &v.MaxBytes, &v.RetentionDays, &v.RequestID, &v.Organization, &v.Project, &v.Model, &v.KeyPrefix, &v.State, &v.Status, &v.CreatedAt, &v.ExpiresAt, &v.FinishedAt)
	return
}

type Plain struct {
	Content       string `json:"content"`
	SHA256        string `json:"sha256"`
	ObservedBytes int64  `json:"observed_bytes"`
	CapturedBytes int    `json:"captured_bytes"`
	Truncated     bool   `json:"truncated"`
}
type Detail struct {
	Meta     Meta   `json:"meta"`
	Request  Plain  `json:"request"`
	Response *Plain `json:"response"`
}
type encrypted struct {
	direction          string
	cipher, nonce, sha []byte
	bytes              int64
	truncated          bool
}
type Capture struct {
	service         *Service
	owner           string
	checkpointParts int
	checkpointBytes int
	checkpointHead  []byte
	meta            Meta
	dek             []byte
	limit           int
	prefix          []byte
	observed        int64
	hash            hash.Hash
	finished        bool
}

func (*Capture) String() string   { return "[content audit capture redacted]" }
func (*Capture) GoString() string { return "[content audit capture redacted]" }

// Observe is an in-memory primitive. Production forwarding must use Checkpoint.
func (c *Capture) Observe(data []byte) {
	if c == nil || c.finished {
		return
	}
	c.observed += int64(len(data))
	_, _ = c.hash.Write(data)
	remaining := c.limit - len(c.prefix)
	if remaining > 0 {
		c.prefix = append(c.prefix, data[:min(remaining, len(data))]...)
	}
}
func aad(meta Meta, direction string, sha []byte, size int64, truncated bool) []byte {
	data, _ := json.Marshal(struct {
		Org, Project, ID, Model, Direction, SHA string
		PolicyRevision                          int64
		Consent, Prefix                         string
		MaxBytes, RetentionDays                 int
		Created, Expires                        int64
		Size                                    int64
		Truncated                               bool
	}{meta.Organization, meta.Project, meta.RequestID, meta.Model, direction, hex.EncodeToString(sha), meta.PolicyRevision, meta.ConsentRef, meta.KeyPrefix, meta.MaxBytes, meta.RetentionDays, meta.CreatedAt.UnixMicro(), meta.ExpiresAt.UnixMicro(), size, truncated})
	return data
}
func encrypt(meta Meta, direction string, key, prefix, sha []byte, size int64) (v encrypted, err error) {
	for cut := 0; cut < utf8.UTFMax && len(prefix) > 0 && !utf8.Valid(prefix); cut++ {
		prefix = prefix[:len(prefix)-1]
	}
	if !utf8.Valid(prefix) {
		return v, ErrUnavailable
	}
	v = encrypted{direction: direction, sha: sha, bytes: size, truncated: int64(len(prefix)) < size}
	v.cipher, v.nonce, err = seal(key, prefix, aad(meta, direction, sha, size, v.truncated))
	return
}
func insertPayload(ctx context.Context, tx pgx.Tx, meta Meta, p encrypted) error {
	_, err := tx.Exec(ctx, `INSERT INTO content_audit_payloads(request_id,direction,created_at,ciphertext,nonce,sha256,observed_bytes,truncated) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, meta.RequestID, p.direction, meta.CreatedAt, p.cipher, p.nonce, p.sha, p.bytes, p.truncated)
	return err
}
func appendChain(ctx context.Context, tx pgx.Tx, org, id, event string, parts ...[]byte) error {
	zero := make([]byte, 32)
	if _, err := tx.Exec(ctx, `INSERT INTO content_audit_heads(organization_id,sequence,hash) VALUES($1,0,$2) ON CONFLICT DO NOTHING`, org, zero); err != nil {
		return err
	}
	var seq int64
	var previous []byte
	if err := tx.QueryRow(ctx, `SELECT sequence,hash FROM content_audit_heads WHERE organization_id=$1 FOR UPDATE`, org).Scan(&seq, &previous); err != nil {
		return err
	}
	sum := digestParts(parts...)
	encoded, _ := json.Marshal(struct {
		Org, ID, Event   string
		Sequence         int64
		Previous, Digest string
	}{org, id, event, seq + 1, hex.EncodeToString(previous), hex.EncodeToString(sum)})
	next := sha256.Sum256(encoded)
	if _, err := tx.Exec(ctx, `INSERT INTO content_audit_chain(organization_id,sequence,request_id,event,digest,previous,hash) VALUES($1,$2,$3,$4,$5,$6,$7)`, org, seq+1, id, event, sum, previous, next[:]); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE content_audit_heads SET sequence=$2,hash=$3 WHERE organization_id=$1`, org, seq+1, next[:])
	return err
}
func (s *Service) dek(ctx context.Context, tx pgx.Tx, org, id string, create bool) ([]byte, error) {
	if !s.Configured() || s.ring.keys[id] == nil {
		return nil, ErrUnavailable
	}
	var wrapped, nonce []byte
	if create {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, ErrUnavailable
		}
		cipher, n, e := seal(s.ring.keys[id], key, []byte("aigw:dek:"+org+":"+id))
		if e != nil {
			return nil, e
		}
		if _, err := tx.Exec(ctx, `INSERT INTO content_audit_deks(organization_id,key_id,wrapped_dek,nonce) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, org, id, cipher, n); err != nil {
			return nil, err
		}
	}
	if err := tx.QueryRow(ctx, `SELECT wrapped_dek,nonce FROM content_audit_deks WHERE organization_id=$1 AND key_id=$2`, org, id).Scan(&wrapped, &nonce); err != nil {
		return nil, err
	}
	return open(s.ring.keys[id], wrapped, nonce, []byte("aigw:dek:"+org+":"+id))
}
func (s *Service) Begin(ctx context.Context, meta Meta, request []byte) (capture *Capture, err error) {
	var acquired bool
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL synchronous_commit = on"); err != nil {
			return err
		}
		policy, err := ReadPolicy(ctx, tx, meta.Organization, meta.Project)
		if err != nil {
			return err
		}
		if !policy.Enabled {
			return nil
		}
		if !s.Configured() {
			return ErrUnavailable
		}
		select {
		case s.slots <- struct{}{}:
			acquired = true
		default:
			return ErrUnavailable
		}
		key, err := s.dek(ctx, tx, meta.Organization, s.ring.active, true)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO content_audit_records(request_id,organization_id,project_id,model,key_prefix,policy_revision,key_id,max_bytes,consent_ref,retention_days,state,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'capturing',clock_timestamp()+($10::integer)::double precision*interval '1 day') RETURNING created_at,expires_at`, meta.RequestID, meta.Organization, meta.Project, meta.Model, meta.KeyPrefix, policy.Revision, s.ring.active, policy.MaxBytes, policy.ConsentRef, policy.RetentionDays).Scan(&meta.CreatedAt, &meta.ExpiresAt); err != nil {
			return err
		}
		meta.State = "capturing"
		meta.PolicyRevision = policy.Revision
		meta.ConsentRef = policy.ConsentRef
		meta.MaxBytes = policy.MaxBytes
		meta.RetentionDays = policy.RetentionDays
		sha := sha256.Sum256(request)
		p, err := encrypt(meta, "request", key, request[:min(len(request), policy.MaxBytes)], sha[:], int64(len(request)))
		if err != nil {
			return err
		}
		if err := insertPayload(ctx, tx, meta, p); err != nil {
			return err
		}
		if err := appendChain(ctx, tx, meta.Organization, meta.RequestID, "start", p.sha, p.cipher, p.nonce, aad(meta, p.direction, p.sha, p.bytes, p.truncated)); err != nil {
			return err
		}
		capture = &Capture{service: s, meta: meta, dek: key, limit: policy.MaxBytes, hash: sha256.New(), owner: rand.Text()}
		capture.checkpointHead = make([]byte, 32)
		proof := checkpointProof(key, meta, capture.checkpointHead, capture.hash.Sum(nil), 0, 0, 0)
		if _, err := tx.Exec(ctx, `INSERT INTO content_audit_checkpoints(request_id,owner_id,lease_until,sha256,observed_bytes,captured_bytes,parts,head,proof) VALUES($1,$2,clock_timestamp()+interval '5 minutes',$3,0,0,0,$4,$5)`, meta.RequestID, capture.owner, capture.hash.Sum(nil), capture.checkpointHead, proof); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		capture = nil
	}
	if err != nil && acquired {
		<-s.slots
	}
	return
}
func (c *Capture) Finish(ctx context.Context, status int, state string) error {
	if c == nil {
		return nil
	}
	if c.finished {
		return ErrConflict
	}
	c.finished = true
	defer func() {
		for i := range c.dek {
			c.dek[i] = 0
		}
		<-c.service.slots
	}()
	if state != "complete" && state != "incomplete" && state != "canceled" {
		return ErrConflict
	}
	p, err := encrypt(c.meta, "response", c.dek, c.prefix, c.hash.Sum(nil), c.observed)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, c.service.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL synchronous_commit = on"); err != nil {
			return err
		}
		var current string
		var expired bool
		if err := tx.QueryRow(ctx, `SELECT state,expires_at<=clock_timestamp() FROM content_audit_records WHERE request_id=$1 FOR UPDATE`, c.meta.RequestID).Scan(&current, &expired); err != nil {
			return err
		}
		if current != "capturing" || expired {
			return ErrConflict
		}
		var owner string
		if err := tx.QueryRow(ctx, `SELECT owner_id FROM content_audit_checkpoints WHERE request_id=$1`, c.meta.RequestID).Scan(&owner); err != nil {
			return err
		}
		if owner != c.owner {
			return ErrConflict
		}
		if err := insertPayload(ctx, tx, c.meta, p); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE content_audit_records SET state=$2,status=$3,finished_at=clock_timestamp() WHERE request_id=$1`, c.meta.RequestID, state, status); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM content_audit_checkpoints WHERE request_id=$1`, c.meta.RequestID); err != nil {
			return err
		}
		return appendChain(ctx, tx, c.meta.Organization, c.meta.RequestID, "seal", p.sha, p.cipher, p.nonce, aad(c.meta, p.direction, p.sha, p.bytes, p.truncated), []byte(state), []byte{byte(status >> 8), byte(status)})
	})
}
func (s *Service) Decrypt(ctx context.Context, tx pgx.Tx, meta Meta) (detail Detail, err error) {
	detail.Meta = meta
	var keyID string
	if err := tx.QueryRow(ctx, `SELECT key_id FROM content_audit_records WHERE request_id=$1`, meta.RequestID).Scan(&keyID); err != nil {
		return detail, err
	}
	key, err := s.dek(ctx, tx, meta.Organization, keyID, false)
	if err != nil {
		return detail, err
	}
	defer func() {
		for i := range key {
			key[i] = 0
		}
	}()
	rows, err := tx.Query(ctx, `SELECT direction,ciphertext,nonce,sha256,observed_bytes,truncated FROM content_audit_payloads WHERE request_id=$1 AND created_at=$2 ORDER BY direction`, meta.RequestID, meta.CreatedAt)
	if err != nil {
		return detail, err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var p encrypted
		if err := rows.Scan(&p.direction, &p.cipher, &p.nonce, &p.sha, &p.bytes, &p.truncated); err != nil {
			return detail, err
		}
		plain, err := open(key, p.cipher, p.nonce, aad(meta, p.direction, p.sha, p.bytes, p.truncated))
		if err != nil {
			return detail, err
		}
		if !p.truncated {
			sha := sha256.Sum256(plain)
			if !equal(sha[:], p.sha) {
				return detail, ErrUnavailable
			}
		}
		v := Plain{Content: string(plain), SHA256: hex.EncodeToString(p.sha), ObservedBytes: p.bytes, CapturedBytes: len(plain), Truncated: p.truncated}
		if p.direction == "request" {
			detail.Request = v
			found = true
		} else {
			detail.Response = &v
		}
	}
	if err := rows.Err(); err != nil {
		return detail, err
	}
	if !found {
		return detail, ErrUnavailable
	}
	return detail, nil
}
func equal(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

func digestParts(parts ...[]byte) []byte {
	h := sha256.New()
	for _, p := range parts {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(p)))
		h.Write(size[:])
		h.Write(p)
	}
	return h.Sum(nil)
}
