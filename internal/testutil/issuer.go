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
	"net/url"
	"sync"
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
	type authorization struct{ client, redirect, nonce, challenge string }
	var mu sync.Mutex
	codes := map[string]authorization{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("nonce") == "" {
			http.Error(w, "PKCE and nonce required", 400)
			return
		}
		code := rand.Text()
		mu.Lock()
		codes[code] = authorization{q.Get("client_id"), q.Get("redirect_uri"), q.Get("nonce"), q.Get("code_challenge")}
		mu.Unlock()
		u, err := url.Parse(q.Get("redirect_uri"))
		if err != nil {
			http.Error(w, "invalid redirect", 400)
			return
		}
		v := u.Query()
		v.Set("code", code)
		v.Set("state", q.Get("state"))
		u.RawQuery = v.Encode()
		http.Redirect(w, r, u.String(), 303)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", 400)
			return
		}
		mu.Lock()
		flow, ok := codes[r.Form.Get("code")]
		delete(codes, r.Form.Get("code"))
		mu.Unlock()
		hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		client := r.Form.Get("client_id")
		if basic, _, ok := r.BasicAuth(); ok {
			client = basic
		}
		if !ok || r.Form.Get("grant_type") != "authorization_code" || client != flow.client || r.Form.Get("redirect_uri") != flow.redirect || base64.RawURLEncoding.EncodeToString(hash[:]) != flow.challenge {
			http.Error(w, "invalid grant", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": i.Token(t, "alice", nil), "token_type": "Bearer", "expires_in": 3600, "id_token": i.Token(t, "alice", map[string]any{"aud": flow.client, "nonce": flow.nonce})})
	})
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
