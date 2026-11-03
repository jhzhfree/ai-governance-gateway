package testutil

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Issuer is a local test IdP with real RSA signatures and a JWKS endpoint.
// No token or private key is written to logs or persistent files.
type Issuer struct {
	URL string
	key *rsa.PrivateKey
}

func NewIssuer(t testing.TB) *Issuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	i := &Issuer{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": i.URL, "jwks_uri": i.URL + "/jwks", "authorization_endpoint": i.URL + "/authorize", "token_endpoint": i.URL + "/token", "response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"}})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{"kty": "RSA", "kid": "test-key", "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	i.URL = s.URL
	return i
}
func (i *Issuer) Token(t testing.TB, subject string, overrides map[string]any) string {
	t.Helper()
	claims := map[string]any{"iss": i.URL, "sub": subject, "aud": "aigw-management", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "scope": "aigw:manage"}
	for k, v := range overrides {
		claims[k] = v
	}
	b, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"test-key","typ":"at+jwt"}`))
	unsigned := header + "." + base64.RawURLEncoding.EncodeToString(b)
	hash := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, i.key, crypto.SHA256, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
}
