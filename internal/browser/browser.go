// Package browser owns the OIDC authorization-code flow and opaque sessions.
// OAuth tokens remain on the server and are discarded after session creation.
package browser

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"golang.org/x/oauth2"
)

//go:embed schema.sql
var schema string

type Config struct {
	Origin, ClientID, ClientSecret string
	AllowLoopbackHTTP              bool
	HTTPClient                     *http.Client
}
type Service struct {
	pool                     *pgxpool.Pool
	api                      *identity.OIDC
	id                       *oidc.IDTokenVerifier
	oauth                    oauth2.Config
	origin, host, cookieName string
	secure                   bool
	client                   *http.Client
}

func New(ctx context.Context, pool *pgxpool.Pool, api *identity.OIDC, issuer string, c Config) (*Service, error) {
	u, err := url.Parse(c.Origin)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || c.ClientID == "" || pool == nil || api == nil {
		return nil, errors.New("invalid browser configuration")
	}
	secure := u.Scheme == "https"
	if !secure && !(u.Scheme == "http" && c.AllowLoopbackHTTP && net.ParseIP(u.Hostname()).IsLoopback()) {
		return nil, errors.New("browser requires HTTPS or explicit loopback HTTP")
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	provider, err := oidc.NewProvider(oidc.ClientContext(ctx, client), issuer)
	if err != nil {
		return nil, errors.New("browser OIDC discovery failed")
	}
	name := "__Host-aigw_session"
	if !secure {
		name = "aigw_session_dev"
	}
	return &Service{pool: pool, api: api, id: provider.Verifier(&oidc.Config{ClientID: c.ClientID, SupportedSigningAlgs: []string{"RS256"}}), oauth: oauth2.Config{ClientID: c.ClientID, ClientSecret: c.ClientSecret, Endpoint: provider.Endpoint(), RedirectURL: c.Origin + "/auth/callback", Scopes: []string{oidc.ScopeOpenID, "profile", "aigw:manage"}}, origin: c.Origin, host: u.Host, cookieName: name, secure: secure, client: client}, nil
}
func (s *Service) Init(ctx context.Context) error { _, err := s.pool.Exec(ctx, schema); return err }
func digest(v string) []byte                      { h := sha256.Sum256([]byte(v)); return h[:] }
func csrf(v string) string {
	h := sha256.Sum256([]byte("aigw:csrf:" + v))
	return hex.EncodeToString(h[:])
}
func (s *Service) cookie(w http.ResponseWriter, name, value, path string, age int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: path, MaxAge: age, HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode})
}
func (s *Service) flowName() string { return s.cookieName + "_flow" }
func reply(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, code string) {
	reply(w, status, map[string]any{"error": map[string]string{"code": code, "request_id": w.Header().Get("X-Request-ID")}})
}
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/login", s.login)
	mux.HandleFunc("GET /auth/callback", s.callback)
	mux.HandleFunc("GET /auth/session", s.session)
	mux.HandleFunc("POST /auth/logout", s.logout)
	mux.HandleFunc("/auth/", func(w http.ResponseWriter, r *http.Request) { fail(w, 404, "not_found") })
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Request-ID", rand.Text())
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Host != s.host {
			fail(w, 403, "origin_mismatch")
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func (s *Service) login(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" && origin != s.origin {
		fail(w, 403, "origin_mismatch")
		return
	}
	state, binding, nonce := rand.Text()+rand.Text(), rand.Text()+rand.Text(), rand.Text()
	verifier := oauth2.GenerateVerifier()
	// Expired flows are unusable; subsequent logins remove expired flow secrets.
	if _, err := s.pool.Exec(r.Context(), `DELETE FROM browser_login_flows WHERE expires_at<=clock_timestamp()`); err != nil {
		fail(w, 503, "login_unavailable")
		return
	}
	_, err := s.pool.Exec(r.Context(), `INSERT INTO browser_login_flows VALUES($1,$2,$3,$4,clock_timestamp()+interval '5 minutes')`, digest(state), digest(binding), verifier, nonce)
	if err != nil {
		fail(w, 503, "login_unavailable")
		return
	}
	s.cookie(w, s.flowName(), binding, "/", 300)
	http.Redirect(w, r, s.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusSeeOther)
}
func (s *Service) callback(w http.ResponseWriter, r *http.Request) {
	state, code := r.URL.Query().Get("state"), r.URL.Query().Get("code")
	binding, err := r.Cookie(s.flowName())
	if err != nil || state == "" || len(state) > 256 || code == "" || len(code) > 4096 {
		fail(w, 400, "invalid_login")
		return
	}
	var verifier, nonce string
	err = s.pool.QueryRow(r.Context(), `DELETE FROM browser_login_flows WHERE state_digest=$1 AND binding_digest=$2 AND expires_at>clock_timestamp() RETURNING verifier,nonce`, digest(state), digest(binding.Value)).Scan(&verifier, &nonce)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			fail(w, 400, "invalid_login")
		} else {
			fail(w, 503, "login_unavailable")
		}
		return
	}
	s.cookie(w, s.flowName(), "", "/", -1)
	ctx, cancel := context.WithTimeout(oidc.ClientContext(r.Context(), s.client), 10*time.Second)
	defer cancel()
	token, err := s.oauth.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		fail(w, 401, "login_failed")
		return
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok {
		fail(w, 401, "login_failed")
		return
	}
	id, err := s.id.Verify(ctx, raw)
	if err != nil || subtle.ConstantTimeCompare([]byte(idNonce(id)), []byte(nonce)) != 1 {
		fail(w, 401, "login_failed")
		return
	}
	p, expiry, err := s.api.Verify(ctx, token.AccessToken)
	if err != nil || p.Issuer != id.Issuer || p.Subject != id.Subject {
		fail(w, 401, "login_failed")
		return
	}
	expiry = earliest(expiry, id.Expiry, time.Now().Add(8*time.Hour))
	if !token.Expiry.IsZero() {
		expiry = earliest(expiry, token.Expiry)
	}
	if !expiry.After(time.Now()) {
		fail(w, 401, "login_failed")
		return
	}
	secret := rand.Text() + rand.Text()
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if old, e := r.Cookie(s.cookieName); e == nil {
			if _, e := tx.Exec(ctx, `DELETE FROM browser_sessions WHERE digest=$1`, digest(old.Value)); e != nil {
				return e
			}
		}
		if _, e := tx.Exec(ctx, `DELETE FROM browser_sessions WHERE expires_at<=clock_timestamp()`); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `INSERT INTO browser_sessions(digest,issuer,subject,expires_at) VALUES($1,$2,$3,$4)`, digest(secret), p.Issuer, p.Subject, expiry)
		return e
	})
	if err != nil {
		fail(w, 503, "login_unavailable")
		return
	}
	s.cookie(w, s.cookieName, secret, "/", int(time.Until(expiry).Seconds()))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
func idNonce(id *oidc.IDToken) string {
	if id == nil {
		return ""
	}
	return id.Nonce
}
func earliest(times ...time.Time) time.Time {
	v := times[0]
	for _, t := range times[1:] {
		if t.Before(v) {
			v = t
		}
	}
	return v
}
func (s *Service) lookup(r *http.Request) (identity.Principal, string, time.Time, error) {
	c, err := r.Cookie(s.cookieName)
	if err != nil || len(c.Value) < 40 || len(c.Value) > 256 {
		return identity.Principal{}, "", time.Time{}, identity.ErrUnauthenticated
	}
	var p identity.Principal
	var expires time.Time
	err = s.pool.QueryRow(r.Context(), `SELECT issuer,subject,expires_at FROM browser_sessions WHERE digest=$1 AND expires_at>clock_timestamp()`, digest(c.Value)).Scan(&p.Issuer, &p.Subject, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		err = identity.ErrUnauthenticated
	}
	return p, c.Value, expires, err
}

// AuthenticateRequest is only used when no explicit Authorization header exists.
func (s *Service) AuthenticateRequest(r *http.Request) (identity.Principal, error) {
	if r.Host != s.host {
		return identity.Principal{}, identity.ErrForbidden
	}
	p, secret, _, err := s.lookup(r)
	if err != nil {
		return identity.Principal{}, err
	}
	if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
		if r.Header.Get("Origin") != s.origin || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(csrf(secret))) != 1 {
			return identity.Principal{}, identity.ErrForbidden
		}
	}
	return p, nil
}
func (s *Service) session(w http.ResponseWriter, r *http.Request) {
	p, secret, expires, err := s.lookup(r)
	if err != nil {
		if errors.Is(err, identity.ErrUnauthenticated) {
			fail(w, 401, "unauthenticated")
		} else {
			fail(w, 503, "session_unavailable")
		}
		return
	}
	reply(w, 200, map[string]any{"user": p, "csrf_token": csrf(secret), "expires_at": expires})
}
func (s *Service) logout(w http.ResponseWriter, r *http.Request) {
	if _, err := s.AuthenticateRequest(r); err != nil {
		if errors.Is(err, identity.ErrUnauthenticated) {
			fail(w, 401, "unauthenticated")
		} else if errors.Is(err, identity.ErrForbidden) {
			fail(w, 403, "forbidden")
		} else {
			fail(w, 503, "session_unavailable")
		}
		return
	}
	c, _ := r.Cookie(s.cookieName)
	if _, err := s.pool.Exec(r.Context(), `DELETE FROM browser_sessions WHERE digest=$1`, digest(c.Value)); err != nil {
		fail(w, 503, "session_unavailable")
		return
	}
	s.cookie(w, s.cookieName, "", "/", -1)
	w.WriteHeader(204)
}
