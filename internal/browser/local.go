package browser

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/localauth"
)

func NewLocal(pool *pgxpool.Pool, local *localauth.Service, origin string, allowHTTP bool) (*Service, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(allowHTTP && u.Scheme == "http" && net.ParseIP(u.Hostname()).IsLoopback())) || local == nil {
		return nil, localauth.ErrInvalid
	}
	secure := u.Scheme == "https"
	name := "__Host-aigw_session"
	if !secure {
		name = "aigw_session_dev"
	}
	return &Service{pool: pool, local: local, issuer: local.Issuer(), origin: origin, host: u.Host, cookieName: name, secure: secure}, nil
}
func (s *Service) authConfiguration(w http.ResponseWriter, r *http.Request) {
	mode := "oidc"
	if s.local != nil {
		mode = "local"
	}
	reply(w, 200, map[string]any{"mode": mode})
}
func (s *Service) localFailure(w http.ResponseWriter, err error) {
	status, code := 503, "local_auth_unavailable"
	switch {
	case errors.Is(err, identity.ErrUnauthenticated):
		status, code = 401, "unauthenticated"
	case errors.Is(err, identity.ErrForbidden):
		status, code = 403, "forbidden"
	case errors.Is(err, localauth.ErrPasswordChange):
		status, code = 403, "password_change_required"
	case errors.Is(err, localauth.ErrInvalid):
		status, code = 400, "invalid_request"
	case errors.Is(err, localauth.ErrConflict):
		status, code = 409, "conflict"
	case errors.Is(err, localauth.ErrRateLimited):
		status, code = 429, "login_rate_limited"
		w.Header().Set("Retry-After", "60")
	}
	reply(w, status, map[string]any{"error": map[string]string{"code": code, "message": code, "request_id": w.Header().Get("X-Request-ID")}})
}
func (s *Service) localDecode(w http.ResponseWriter, r *http.Request, value any) bool {
	if r.Header.Get("Origin") != s.origin {
		s.localFailure(w, identity.ErrForbidden)
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil {
		s.localFailure(w, localauth.ErrInvalid)
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		s.localFailure(w, localauth.ErrInvalid)
		return false
	}
	return true
}
func (s *Service) localRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /auth/local/login", s.localLogin)
	mux.HandleFunc("POST /auth/local/token", s.localToken)
	mux.HandleFunc("POST /auth/local/logout", s.localTokenLogout)
	mux.HandleFunc("POST /auth/local/password", s.localPassword)
	mux.HandleFunc("GET /auth/local/users", s.localUsers)
	mux.HandleFunc("GET /auth/local/events", s.localEvents)
	mux.HandleFunc("POST /auth/local/users", s.localCreateUser)
	mux.HandleFunc("PUT /auth/local/users/{user}", s.localSetUser)
	mux.HandleFunc("POST /auth/local/users/{user}/password", s.localResetPassword)
	mux.HandleFunc("POST /auth/local/users/{user}/revoke", s.localRevoke)
}
func (s *Service) localLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !s.localDecode(w, r, &body) {
		return
	}
	user, err := s.local.CheckPassword(r.Context(), body.Username, body.Password)
	body.Password = ""
	if err != nil {
		s.localFailure(w, err)
		return
	}
	secret := rand.Text() + rand.Text()
	var old []byte
	if c, e := r.Cookie(s.cookieName); e == nil {
		old = digest(c.Value)
	}
	expiry := time.Now().Add(localauth.TokenTTL)
	if err := s.local.OpenSession(r.Context(), user, secret, expiry, old); err != nil {
		s.localFailure(w, err)
		return
	}
	s.cookie(w, s.cookieName, secret, "/", int(localauth.TokenTTL.Seconds()))
	w.WriteHeader(204)
}
func (s *Service) localToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !s.localDecode(w, r, &body) {
		return
	}
	token, err := s.local.Token(r.Context(), body.Username, body.Password)
	body.Password = ""
	if err != nil {
		s.localFailure(w, err)
		return
	}
	reply(w, 200, token)
}
func (s *Service) localPassword(w http.ResponseWriter, r *http.Request) {
	p, err := s.AuthenticateRequest(r)
	if err != nil {
		s.localFailure(w, err)
		return
	}
	var body struct {
		Current string `json:"current_password"`
		Next    string `json:"new_password"`
	}
	if !s.localDecode(w, r, &body) {
		return
	}
	err = s.local.ChangePassword(r.Context(), p, body.Current, body.Next)
	body.Current = ""
	body.Next = ""
	if err != nil {
		s.localFailure(w, err)
		return
	}
	s.cookie(w, s.cookieName, "", "/", -1)
	w.WriteHeader(204)
}
func (s *Service) localAdmin(w http.ResponseWriter, r *http.Request) (identity.Principal, bool) {
	var p identity.Principal
	var err error
	if len(r.Header.Values("Authorization")) > 1 {
		s.localFailure(w, identity.ErrUnauthenticated)
		return p, false
	}
	if r.Header.Get("Authorization") != "" {
		token, e := identity.Bearer(r.Header.Get("Authorization"))
		if e != nil {
			err = e
		} else {
			p, err = s.local.Authenticate(r.Context(), token)
		}
	} else {
		p, err = s.AuthenticateRequest(r)
	}
	if err == nil && !s.local.IsAdmin(r.Context(), p) {
		err = identity.ErrForbidden
	}
	if err != nil {
		s.localFailure(w, err)
		return p, false
	}
	return p, true
}
func (s *Service) localUsers(w http.ResponseWriter, r *http.Request) {
	p, ok := s.localAdmin(w, r)
	if !ok {
		return
	}
	items, err := s.local.Users(r.Context(), p, r.URL.Query().Get("after"))
	if err != nil {
		s.localFailure(w, err)
		return
	}
	next := ""
	if len(items) > 100 {
		items = items[:100]
		next = items[99].ID
	}
	reply(w, 200, map[string]any{"data": items, "next_cursor": next})
}
func (s *Service) localCreateUser(w http.ResponseWriter, r *http.Request) {
	p, ok := s.localAdmin(w, r)
	if !ok {
		return
	}
	var body struct {
		Username      string `json:"username"`
		Password      string `json:"password"`
		Administrator bool   `json:"administrator"`
	}
	if !s.localDecode(w, r, &body) {
		return
	}
	user, err := s.local.Create(r.Context(), p, body.Username, body.Password, body.Administrator)
	body.Password = ""
	if err != nil {
		s.localFailure(w, err)
		return
	}
	reply(w, 201, user)
}
func (s *Service) localSetUser(w http.ResponseWriter, r *http.Request) {
	p, ok := s.localAdmin(w, r)
	if !ok {
		return
	}
	var body struct {
		Expected      *int64 `json:"expected_revision"`
		Enabled       *bool  `json:"enabled"`
		Administrator *bool  `json:"administrator"`
	}
	if !s.localDecode(w, r, &body) {
		return
	}
	if body.Expected == nil || body.Enabled == nil || body.Administrator == nil {
		s.localFailure(w, localauth.ErrInvalid)
		return
	}
	if err := s.local.SetUser(r.Context(), p, r.PathValue("user"), *body.Expected, *body.Enabled, *body.Administrator); err != nil {
		s.localFailure(w, err)
		return
	}
	w.WriteHeader(204)
}
func (s *Service) localResetPassword(w http.ResponseWriter, r *http.Request) {
	p, ok := s.localAdmin(w, r)
	if !ok {
		return
	}
	var body struct {
		Password string `json:"password"`
		Expected *int64 `json:"expected_revision"`
	}
	if !s.localDecode(w, r, &body) {
		return
	}
	if body.Expected == nil {
		s.localFailure(w, localauth.ErrInvalid)
		return
	}
	err := s.local.ResetPassword(r.Context(), p, r.PathValue("user"), body.Password, *body.Expected)
	body.Password = ""
	if err != nil {
		s.localFailure(w, err)
		return
	}
	w.WriteHeader(204)
}
func (s *Service) localRevoke(w http.ResponseWriter, r *http.Request) {
	p, ok := s.localAdmin(w, r)
	if !ok {
		return
	}
	if r.Header.Get("Origin") != s.origin {
		s.localFailure(w, identity.ErrForbidden)
		return
	}
	if err := s.local.RevokeUser(r.Context(), p, r.PathValue("user")); err != nil {
		s.localFailure(w, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Service) localTokenLogout(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != s.origin || len(r.Header.Values("Authorization")) != 1 {
		s.localFailure(w, identity.ErrForbidden)
		return
	}
	secret, err := identity.Bearer(r.Header.Get("Authorization"))
	if err == nil {
		err = s.local.RevokeToken(r.Context(), secret)
	}
	if err != nil {
		s.localFailure(w, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Service) localEvents(w http.ResponseWriter, r *http.Request) {
	p, ok := s.localAdmin(w, r)
	if !ok {
		return
	}
	before := int64(0)
	var err error
	if text := r.URL.Query().Get("after"); text != "" {
		before, err = strconv.ParseInt(text, 10, 64)
		if err != nil || before < 1 {
			s.localFailure(w, localauth.ErrInvalid)
			return
		}
	}
	items, err := s.local.Events(r.Context(), p, before)
	if err != nil {
		s.localFailure(w, err)
		return
	}
	next := ""
	if len(items) > 100 {
		items = items[:100]
		next = strconv.FormatInt(items[99].ID, 10)
	}
	reply(w, 200, map[string]any{"data": items, "next_cursor": next})
}
