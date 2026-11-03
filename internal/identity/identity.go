// Package identity verifies management credentials without assigning business roles.
package identity

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

var (
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrForbidden       = errors.New("forbidden")
)

type Principal struct {
	Issuer  string `json:"issuer"`
	Subject string `json:"subject"`
}
type Authenticator interface {
	Authenticate(context.Context, string) (Principal, error)
}
type OIDC struct{ verifier *oidc.IDTokenVerifier }

// NewOIDC uses discovery/JWKS with signature, issuer, audience and expiry checks.
// The service entrypoint requires HTTPS; local HTTP issuers are only for tests.
// IdPs must issue JWT API tokens for the configured audience with aigw:manage.
func NewOIDC(ctx context.Context, issuer, audience string) (*OIDC, error) {
	if issuer == "" || audience == "" {
		return nil, errors.New("issuer and audience required")
	}
	p, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, errors.New("OIDC discovery failed")
	}
	return &OIDC{verifier: p.Verifier(&oidc.Config{ClientID: audience, SupportedSigningAlgs: []string{"RS256"}})}, nil
}

func (o *OIDC) Authenticate(ctx context.Context, token string) (Principal, error) {
	p, _, err := o.Verify(ctx, token)
	return p, err
}

// Verify also exposes the verified expiry so browser sessions cannot outlive
// the API token that authorized their creation.
func (o *OIDC) Verify(ctx context.Context, token string) (Principal, time.Time, error) {
	if len(token) > 16<<10 || strings.HasPrefix(token, "aigw_pk_") {
		return Principal{}, time.Time{}, ErrUnauthenticated
	}
	id, err := o.verifier.Verify(ctx, token)
	if err != nil {
		return Principal{}, time.Time{}, ErrUnauthenticated
	}
	var claims struct {
		Scope string `json:"scope"`
	}
	if id.Claims(&claims) != nil || id.Subject == "" {
		return Principal{}, time.Time{}, ErrUnauthenticated
	}
	for _, s := range strings.Fields(claims.Scope) {
		if s == "aigw:manage" {
			return Principal{Issuer: id.Issuer, Subject: id.Subject}, id.Expiry, nil
		}
	}
	return Principal{}, time.Time{}, ErrForbidden
}

func Bearer(header string) (string, error) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || len(parts[1]) > 16<<10 {
		return "", ErrUnauthenticated
	}
	return parts[1], nil
}
