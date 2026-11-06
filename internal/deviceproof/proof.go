// Package deviceproof implements the Ed25519 DPoP proof profile used by device
// sessions. TLS is still mandatory; DPoP does not sign the HTTP body or query.
package deviceproof

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	jose "github.com/go-jose/go-jose/v4"
	"regexp"
	"strings"
	"time"
)

var ErrInvalid = errors.New("invalid device proof")
var noncePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

type Claims struct {
	ID        string `json:"jti"`
	Method    string `json:"htm"`
	URL       string `json:"htu"`
	IssuedAt  int64  `json:"iat"`
	TokenHash string `json:"ath,omitempty"`
}

func Public(key ed25519.PublicKey) string { return base64.RawURLEncoding.EncodeToString(key) }
func ParsePublic(s string) (ed25519.PublicKey, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) != ed25519.PublicKeySize || Public(b) != s {
		return nil, ErrInvalid
	}
	return ed25519.PublicKey(b), nil
}
func tokenHash(s string) string {
	if s == "" {
		return ""
	}
	h := sha256.Sum256([]byte(s))
	return base64.RawURLEncoding.EncodeToString(h[:])
}
func Sign(key ed25519.PrivateKey, method, htu, token string) (string, error) {
	return SignAt(key, method, htu, token, time.Now(), rand.Text())
}
func SignAt(key ed25519.PrivateKey, method, htu, token string, now time.Time, id string) (string, error) {
	if len(key) != ed25519.PrivateKeySize || !noncePattern.MatchString(id) {
		return "", ErrInvalid
	}
	opts := (&jose.SignerOptions{}).WithType("dpop+jwt").WithHeader("jwk", jose.JSONWebKey{Key: key.Public()})
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.EdDSA, Key: key}, opts)
	if err != nil {
		return "", ErrInvalid
	}
	payload, err := json.Marshal(Claims{ID: id, Method: method, URL: htu, IssuedAt: now.Unix(), TokenHash: tokenHash(token)})
	if err != nil {
		return "", ErrInvalid
	}
	sig, err := signer.Sign(payload)
	if err != nil {
		return "", ErrInvalid
	}
	return sig.CompactSerialize()
}
func Verify(raw string, key ed25519.PublicKey, method, htu, token string, now time.Time) (Claims, error) {
	var c Claims
	if len(raw) == 0 || len(raw) > 8<<10 || strings.Count(raw, ".") != 2 || len(key) != ed25519.PublicKeySize {
		return c, ErrInvalid
	}
	signed, err := jose.ParseSigned(raw, []jose.SignatureAlgorithm{jose.EdDSA})
	if err != nil || len(signed.Signatures) != 1 {
		return c, ErrInvalid
	}
	header := signed.Signatures[0].Protected
	if header.ExtraHeaders[jose.HeaderType] != "dpop+jwt" || header.JSONWebKey == nil || !header.JSONWebKey.IsPublic() {
		return c, ErrInvalid
	}
	supplied, ok := header.JSONWebKey.Key.(ed25519.PublicKey)
	if !ok || !supplied.Equal(key) {
		return c, ErrInvalid
	}
	payload, err := signed.Verify(key)
	if err != nil || json.Unmarshal(payload, &c) != nil {
		return c, ErrInvalid
	}
	expected := tokenHash(token)
	if c.Method != method || c.URL != htu || !noncePattern.MatchString(c.ID) || c.IssuedAt < now.Unix()-60 || c.IssuedAt > now.Unix()+30 || subtle.ConstantTimeCompare([]byte(c.TokenHash), []byte(expected)) != 1 {
		return Claims{}, ErrInvalid
	}
	return c, nil
}

type Binding struct{ DeviceID, SessionID, Organization, Project string }
type bindingKey struct{}

func WithBinding(ctx context.Context, v Binding) context.Context {
	return context.WithValue(ctx, bindingKey{}, v)
}
func FromContext(ctx context.Context) (Binding, bool) {
	v, ok := ctx.Value(bindingKey{}).(Binding)
	return v, ok
}
