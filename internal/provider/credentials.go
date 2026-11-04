package provider

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
)

var ErrCredentialStorageUnavailable = errors.New("provider credential encryption unavailable")

func validCredential(value string) bool {
	return strings.TrimSpace(value) != "" && len(value) <= 8192 && strings.IndexFunc(value, func(r rune) bool { return r < 33 || r > 126 }) < 0
}
func (m *Manager) encryptionKeys() (map[string][]byte, string, error) {
	raw, active := m.getenv("AIGW_PROVIDER_ENCRYPTION_KEYS"), m.getenv("AIGW_PROVIDER_ENCRYPTION_ACTIVE_KEY")
	var encoded map[string]string
	if json.Unmarshal([]byte(raw), &encoded) != nil || len(encoded) < 1 || len(encoded) > 16 || active == "" {
		return nil, "", ErrCredentialStorageUnavailable
	}
	keys := map[string][]byte{}
	for id, value := range encoded {
		if !pricePattern.MatchString(id) {
			return nil, "", ErrCredentialStorageUnavailable
		}
		key, err := base64.StdEncoding.DecodeString(value)
		if err != nil || len(key) != 32 {
			return nil, "", ErrCredentialStorageUnavailable
		}
		keys[id] = key
	}
	if keys[active] == nil {
		return nil, "", ErrCredentialStorageUnavailable
	}
	return keys, active, nil
}
func credentialAAD(id string, revision int64, keyID string) []byte {
	data, _ := json.Marshal([]any{"aigw-provider-credential-v1", id, revision, keyID})
	return data
}
func credentialAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrCredentialStorageUnavailable
	}
	return cipher.NewGCM(block)
}

type credentialQuery interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}
type credentialMetadata struct {
	revision   int64
	source     string
	configured bool
}

// A stored credential always takes precedence. Authentication/decryption failure
// never falls back to an environment secret, which might belong to another account.
func (m *Manager) credentials(ctx context.Context, q credentialQuery) (func(string) string, map[string]credentialMetadata, error) {
	rows, err := q.Query(ctx, `SELECT provider,revision,key_id,nonce,ciphertext FROM provider_credentials ORDER BY provider`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	overrides := map[string]string{}
	metadata := map[string]credentialMetadata{}
	var keys map[string][]byte
	for rows.Next() {
		var id, keyID string
		var revision int64
		var nonce, data []byte
		if err := rows.Scan(&id, &revision, &keyID, &nonce, &data); err != nil {
			return nil, nil, err
		}
		if keys == nil {
			var err error
			keys, _, err = m.encryptionKeys()
			if err != nil {
				return nil, nil, err
			}
		}
		aead, err := credentialAEAD(keys[keyID])
		if err != nil || len(nonce) != 12 {
			return nil, nil, ErrCredentialStorageUnavailable
		}
		plain, err := aead.Open(nil, nonce, data, credentialAAD(id, revision, keyID))
		if err != nil || !validCredential(string(plain)) {
			return nil, nil, ErrCredentialStorageUnavailable
		}
		profile, ok := profiles[id]
		if !ok {
			return nil, nil, ErrCredentialStorageUnavailable
		}
		overrides[profile.KeyEnvironment] = string(plain)
		for i := range plain {
			plain[i] = 0
		}
		metadata[id] = credentialMetadata{revision: revision, source: "database", configured: true}
	}
	if rows.Err() != nil {
		return nil, nil, rows.Err()
	}
	for _, p := range Profiles() {
		if _, ok := metadata[p.ID]; !ok {
			metadata[p.ID] = credentialMetadata{source: "environment", configured: validCredential(m.getenv(p.KeyEnvironment))}
		}
	}
	return func(name string) string {
		if v, ok := overrides[name]; ok {
			return v
		}
		return m.getenv(name)
	}, metadata, nil
}
func (m *Manager) SetCredential(ctx context.Context, p identity.Principal, id string, expected int64, secret string) error {
	if !m.CanManage(p) {
		return identity.ErrForbidden
	}
	if _, ok := profiles[id]; !ok || expected < 0 || expected >= MaxAmount || !validCredential(secret) {
		return ErrConfigurationInvalid
	}
	keys, active, err := m.encryptionKeys()
	if err != nil {
		return err
	}
	aead, err := credentialAEAD(keys[active])
	if err != nil {
		return ErrCredentialStorageUnavailable
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return ErrCredentialStorageUnavailable
	}
	encrypted := aead.Seal(nil, nonce, []byte(secret), credentialAAD(id, expected+1, active))
	return pgx.BeginFunc(ctx, m.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(817349160126)`); err != nil {
			return err
		}
		var revision int64
		err := tx.QueryRow(ctx, `SELECT revision FROM provider_credentials WHERE provider=$1 FOR UPDATE`, id).Scan(&revision)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if revision != expected {
			return ErrConfigurationConflict
		}
		if _, err = tx.Exec(ctx, `INSERT INTO provider_credentials(provider,revision,key_id,nonce,ciphertext) VALUES($1,$2,$3,$4,$5) ON CONFLICT(provider) DO UPDATE SET revision=EXCLUDED.revision,key_id=EXCLUDED.key_id,nonce=EXCLUDED.nonce,ciphertext=EXCLUDED.ciphertext,updated_at=now()`, id, expected+1, active, nonce, encrypted); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO provider_credential_events(provider,revision,actor_issuer,actor_subject) VALUES($1,$2,$3,$4)`, id, expected+1, p.Issuer, p.Subject)
		return err
	})
}
func (m *Manager) String() string   { return "[provider manager; credentials redacted]" }
func (m *Manager) GoString() string { return m.String() }

var _ fmt.Stringer = (*Manager)(nil)
