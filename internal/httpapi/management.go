// Package httpapi exposes the management API; control enforces business roles.
package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/admission"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
)

type principalKey struct{}
type Handler struct {
	Store *control.Store
	Auth  identity.Authenticator
}
type SessionAuthenticator interface {
	AuthenticateRequest(*http.Request) (identity.Principal, error)
}

func New(store *control.Store, auth identity.Authenticator) http.Handler {
	return NewWithSession(store, auth, nil)
}
func NewWithSession(store *control.Store, auth identity.Authenticator, session SessionAuthenticator) http.Handler {
	h := &Handler{Store: store, Auth: auth}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/models", func(w http.ResponseWriter, r *http.Request) {
		send(w, 200, map[string]any{"data": store.Models(), "next_cursor": ""})
	})
	mux.HandleFunc("GET /api/v1/organizations", h.organizations)
	mux.HandleFunc("POST /api/v1/organizations", h.createOrganization)
	mux.HandleFunc("PUT /api/v1/organizations/{org}/members", h.orgMember)
	mux.HandleFunc("GET /api/v1/organizations/{org}/projects", h.projects)
	mux.HandleFunc("POST /api/v1/organizations/{org}/projects", h.createProject)
	mux.HandleFunc("GET /api/v1/organizations/{org}/projects/{project}", h.project)
	mux.HandleFunc("PUT /api/v1/organizations/{org}/projects/{project}/members", h.projectMember)
	mux.HandleFunc("PUT /api/v1/organizations/{org}/projects/{project}/models", h.models)
	mux.HandleFunc("POST /api/v1/organizations/{org}/projects/{project}/keys", h.createKey)
	mux.HandleFunc("GET /api/v1/organizations/{org}/projects/{project}/keys", h.keys)
	mux.HandleFunc("DELETE /api/v1/organizations/{org}/projects/{project}/keys/{key}", h.revokeKey)
	mux.HandleFunc("GET /api/v1/organizations/{org}/projects/{project}/usage", h.usage)
	mux.HandleFunc("GET /api/v1/organizations/{org}/projects/{project}/limits", h.limits)
	mux.HandleFunc("PUT /api/v1/organizations/{org}/projects/{project}/limits", h.setLimits)
	mux.HandleFunc("GET /api/v1/organizations/{org}/projects/{project}/inspection", h.inspect)
	mux.HandleFunc("GET /api/v1/organizations/{org}/projects/{project}/requests", h.requests)
	mux.HandleFunc("GET /api/v1/organizations/{org}/projects/{project}/requests/{request}", h.request)
	mux.HandleFunc("GET /api/v1/organizations/{org}/audit", h.audit)
	mux.HandleFunc("/api/v1/", func(w http.ResponseWriter, r *http.Request) { failure(w, r, control.ErrNotFound) })
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", rand.Text())
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if store == nil || auth == nil {
			failure(w, r, errors.New("not configured"))
			return
		}
		var p identity.Principal
		var err error
		if len(r.Header.Values("Authorization")) == 0 && session != nil {
			p, err = session.AuthenticateRequest(r)
		} else if len(r.Header.Values("Authorization")) == 1 {
			var token string
			token, err = identity.Bearer(r.Header.Get("Authorization"))
			if err == nil {
				p, err = auth.Authenticate(r.Context(), token)
			}
		} else {
			err = identity.ErrUnauthenticated
		}
		if err != nil {
			failure(w, r, err)
			return
		}
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}
func principal(r *http.Request) identity.Principal {
	return r.Context().Value(principalKey{}).(identity.Principal)
}
func send(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
func failure(w http.ResponseWriter, r *http.Request, err error) {
	status, code := 503, "service_unavailable"
	switch {
	case errors.Is(err, identity.ErrUnauthenticated):
		status, code = 401, "unauthenticated"
		w.Header().Set("WWW-Authenticate", `Bearer realm="management"`)
	case errors.Is(err, identity.ErrForbidden):
		status, code = 403, "forbidden"
	case errors.Is(err, control.ErrNotFound):
		status, code = 404, "not_found"
	case errors.Is(err, admission.ErrConflict):
		status, code = 409, "conflict"
	case errors.Is(err, control.ErrInvalid) || errors.Is(err, admission.ErrInvalid):
		status, code = 400, "invalid_request"
	}
	send(w, status, map[string]any{"error": map[string]string{"code": code, "message": code, "request_id": w.Header().Get("X-Request-ID")}})
}
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		failure(w, r, control.ErrInvalid)
		return false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		failure(w, r, control.ErrInvalid)
		return false
	}
	return true
}
func page(w http.ResponseWriter, r *http.Request) (after string, limit int, ok bool) {
	return pageWithCursorLimit(w, r, 128)
}
func pageWithCursorLimit(w http.ResponseWriter, r *http.Request, maxCursor int) (after string, limit int, ok bool) {
	q := r.URL.Query()
	limit = 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			failure(w, r, control.ErrInvalid)
			return "", 0, false
		}
		limit = n
	}
	after = q.Get("after")
	if len(after) > maxCursor {
		failure(w, r, control.ErrInvalid)
		return "", 0, false
	}
	return after, limit, true
}
func list[T any](w http.ResponseWriter, items []T, limit int, cursor func(T) string) {
	next := ""
	if len(items) > limit {
		items = items[:limit]
		next = cursor(items[len(items)-1])
	}
	send(w, 200, map[string]any{"data": items, "next_cursor": next})
}
func (h *Handler) organizations(w http.ResponseWriter, r *http.Request) {
	a, l, ok := page(w, r)
	if !ok {
		return
	}
	items, err := h.Store.ListOrganizations(r.Context(), principal(r), a, l+1)
	if err != nil {
		failure(w, r, err)
		return
	}
	list(w, items, l, func(o control.Organization) string { return o.ID })
}
func (h *Handler) createOrganization(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Currency string `json:"currency"`
		Name     string `json:"name"`
		Period   string `json:"period"`
		Limit    *int64 `json:"limit_amount"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Limit == nil {
		failure(w, r, control.ErrInvalid)
		return
	}
	if in.Currency == "" {
		in.Currency = "MOCK"
	}
	o, err := h.Store.CreateOrganizationWithCurrency(r.Context(), principal(r), in.Name, in.Period, in.Currency, *in.Limit)
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 201, o)
}
func (h *Handler) orgMember(w http.ResponseWriter, r *http.Request) {
	var m control.Member
	if !decode(w, r, &m) {
		return
	}
	if err := h.Store.PutOrganizationMember(r.Context(), principal(r), r.PathValue("org"), m); err != nil {
		failure(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func (h *Handler) projects(w http.ResponseWriter, r *http.Request) {
	a, l, ok := page(w, r)
	if !ok {
		return
	}
	items, err := h.Store.ListProjects(r.Context(), principal(r), r.PathValue("org"), a, l+1)
	if err != nil {
		failure(w, r, err)
		return
	}
	list(w, items, l, func(p control.Project) string { return p.ID })
}

func (h *Handler) project(w http.ResponseWriter, r *http.Request) {
	p, err := h.Store.GetProject(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"))
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 200, p)
}
func (h *Handler) createProject(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name  string `json:"name"`
		Limit *int64 `json:"limit_amount"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Limit == nil {
		failure(w, r, control.ErrInvalid)
		return
	}
	p, err := h.Store.CreateProject(r.Context(), principal(r), r.PathValue("org"), in.Name, *in.Limit)
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 201, p)
}
func (h *Handler) projectMember(w http.ResponseWriter, r *http.Request) {
	var m control.Member
	if !decode(w, r, &m) {
		return
	}
	if err := h.Store.PutProjectMember(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), m); err != nil {
		failure(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func (h *Handler) models(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Models []string `json:"models"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Models == nil {
		failure(w, r, control.ErrInvalid)
		return
	}
	if err := h.Store.SetModels(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), in.Models); err != nil {
		failure(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func (h *Handler) createKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name   string   `json:"name"`
		Models []string `json:"models"`
		TTL    int64    `json:"ttl_seconds"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.TTL < 60 || in.TTL > 90*24*3600 {
		failure(w, r, control.ErrInvalid)
		return
	}
	k, err := h.Store.CreateKey(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), in.Name, in.Models, time.Duration(in.TTL)*time.Second)
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 201, k)
}
func (h *Handler) keys(w http.ResponseWriter, r *http.Request) {
	a, l, ok := page(w, r)
	if !ok {
		return
	}
	items, err := h.Store.ListKeys(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), a, l+1)
	if err != nil {
		failure(w, r, err)
		return
	}
	list(w, items, l, func(k control.Key) string { return k.ID })
}
func (h *Handler) revokeKey(w http.ResponseWriter, r *http.Request) {
	if err := h.Store.RevokeKey(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), r.PathValue("key")); err != nil {
		failure(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func (h *Handler) usage(w http.ResponseWriter, r *http.Request) {
	u, err := h.Store.Usage(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"))
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 200, u)
}
func (h *Handler) audit(w http.ResponseWriter, r *http.Request) {
	a, l, ok := page(w, r)
	if !ok {
		return
	}
	var after int64
	var err error
	if a != "" {
		after, err = strconv.ParseInt(a, 10, 64)
		if err != nil || after < 0 {
			failure(w, r, control.ErrInvalid)
			return
		}
	}
	items, err := h.Store.ListAudit(r.Context(), principal(r), r.PathValue("org"), after, l+1)
	if err != nil {
		failure(w, r, err)
		return
	}
	list(w, items, l, func(a control.Audit) string { return strconv.FormatInt(a.ID, 10) })
}
