// Package contentaudit implements opt-in encrypted, bounded content capture.
package contentaudit

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
)

var ErrUnavailable = errors.New("content audit unavailable")
var ErrConflict = errors.New("content audit conflict")
var keyID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type Keyring struct {
	keys   map[string][]byte
	active string
}

func (Keyring) String() string   { return "[content audit keys redacted]" }
func (Keyring) GoString() string { return "[content audit keys redacted]" }

// ParseKeys accepts an environment-injected map of IDs to base64 32-byte KEKs.
func ParseKeys(text, active string) (*Keyring, error) {
	if text == "" && active == "" {
		return nil, nil
	}
	var values map[string]string
	if len(text) > 8192 || json.Unmarshal([]byte(text), &values) != nil || len(values) < 1 || len(values) > 16 || !keyID.MatchString(active) {
		return nil, ErrUnavailable
	}
	r := &Keyring{keys: map[string][]byte{}, active: active}
	for id, value := range values {
		key, err := base64.StdEncoding.DecodeString(value)
		if !keyID.MatchString(id) || err != nil || len(key) != 32 {
			return nil, ErrUnavailable
		}
		r.keys[id] = key
	}
	if r.keys[active] == nil {
		return nil, ErrUnavailable
	}
	return r, nil
}
func seal(key, plain, aad []byte) (encrypted, nonce []byte, err error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, ErrUnavailable
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, ErrUnavailable
	}
	nonce = make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, nil, ErrUnavailable
	}
	return aead.Seal(nil, nonce, plain, aad), nonce, nil
}
func open(key, encrypted, nonce, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrUnavailable
	}
	aead, err := cipher.NewGCM(block)
	if err != nil || len(nonce) != aead.NonceSize() {
		return nil, ErrUnavailable
	}
	plain, err := aead.Open(nil, nonce, encrypted, aad)
	if err != nil {
		return nil, ErrUnavailable
	}
	return plain, nil
}
