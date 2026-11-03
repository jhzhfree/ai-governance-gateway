package identity_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func TestOIDCValidatesSignatureAndClaims(t *testing.T) {
	i := testutil.NewIssuer(t)
	auth, err := identity.NewOIDC(context.Background(), i.URL, "aigw-management")
	if err != nil {
		t.Fatal(err)
	}
	p, err := auth.Authenticate(context.Background(), i.Token(t, "alice", nil))
	if err != nil || p.Subject != "alice" || p.Issuer != i.URL {
		t.Fatalf("valid identity failed: %v", err)
	}
	for _, tc := range []struct {
		name   string
		claims map[string]any
		want   error
	}{
		{"expired", map[string]any{"exp": time.Now().Add(-time.Hour).Unix()}, identity.ErrUnauthenticated},
		{"not_yet_valid", map[string]any{"nbf": time.Now().Add(time.Hour).Unix()}, identity.ErrUnauthenticated},
		{"audience", map[string]any{"aud": "other-api"}, identity.ErrUnauthenticated},
		{"issuer", map[string]any{"iss": "https://wrong.example"}, identity.ErrUnauthenticated},
		{"subject", map[string]any{"sub": ""}, identity.ErrUnauthenticated},
		{"scope", map[string]any{"scope": "openid profile"}, identity.ErrForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := auth.Authenticate(context.Background(), i.Token(t, "alice", tc.claims))
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want %v", err, tc.want)
			}
		})
	}
	token := i.Token(t, "alice", nil)
	parts := strings.Split(token, ".")
	// Keep issuer/audience/expiry/scope valid: only signature verification should
	// prevent an attacker changing the subject to another existing user.
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	claims["sub"] = "victim"
	payload, err = json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	parts[1] = base64.RawURLEncoding.EncodeToString(payload)
	for _, invalid := range []string{strings.Join(parts, "."), "aigw_pk_not-a-management-token", "not-a-jwt"} {
		if _, err := auth.Authenticate(context.Background(), invalid); !errors.Is(err, identity.ErrUnauthenticated) {
			t.Fatal("invalid credential accepted")
		}
	}
}
func TestBearerParsing(t *testing.T) {
	for _, h := range []string{"", "Basic abc", "Bearer a b", "Bearer"} {
		if _, err := identity.Bearer(h); err == nil {
			t.Fatal("invalid auth header accepted")
		}
	}
	if token, err := identity.Bearer("bearer token"); err != nil || token != "token" {
		t.Fatal("valid bearer rejected")
	}
}
