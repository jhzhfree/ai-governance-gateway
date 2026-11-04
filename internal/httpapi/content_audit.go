package httpapi

import (
	"github.com/jhzhfree/ai-governance-gateway/internal/contentaudit"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"net/http"
	"strconv"
)

func (h *Handler) contentPolicy(w http.ResponseWriter, r *http.Request) {
	v, err := h.Store.ContentPolicy(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"))
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 200, v)
}
func (h *Handler) setContentPolicy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled   *bool  `json:"enabled"`
		Retention *int   `json:"retention_days"`
		Max       *int   `json:"max_bytes"`
		Consent   string `json:"consent_ref"`
		Expected  *int64 `json:"expected_revision"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Enabled == nil || body.Retention == nil || body.Max == nil || body.Expected == nil {
		failure(w, r, control.ErrInvalid)
		return
	}
	err := h.Store.SetContentPolicy(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), contentaudit.Policy{Enabled: *body.Enabled, RetentionDays: *body.Retention, MaxBytes: *body.Max, ConsentRef: body.Consent, Revision: *body.Expected})
	if err != nil {
		failure(w, r, err)
		return
	}
	h.contentPolicy(w, r)
}
func (h *Handler) contentAuditor(w http.ResponseWriter, r *http.Request) {
	var body struct {
		identity.Principal
		Active *bool `json:"active"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Active == nil {
		failure(w, r, control.ErrInvalid)
		return
	}
	if err := h.Store.GrantContentAuditor(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), control.ContentAuditor{Principal: body.Principal, Active: *body.Active}); err != nil {
		failure(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func (h *Handler) contentRecords(w http.ResponseWriter, r *http.Request) {
	after, limit, ok := pageWithCursorLimit(w, r, 512)
	if !ok {
		return
	}
	data, next, err := h.Store.ContentRecords(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), after, limit)
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 200, map[string]any{"data": data, "next_cursor": next})
}
func (h *Handler) contentReveal(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &body) {
		return
	}
	v, err := h.Store.ReadContent(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), r.PathValue("request"), body.Reason)
	if err != nil {
		failure(w, r, err)
		return
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	send(w, 200, v)
}
func (h *Handler) contentAccess(w http.ResponseWriter, r *http.Request) {
	after, limit, ok := page(w, r)
	if !ok {
		return
	}
	cursor := int64(0)
	if after != "" {
		var err error
		cursor, err = strconv.ParseInt(after, 10, 64)
		if err != nil {
			failure(w, r, control.ErrInvalid)
			return
		}
	}
	items, err := h.Store.ContentAccessLog(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), cursor, limit+1)
	if err != nil {
		failure(w, r, err)
		return
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		next = strconv.FormatInt(items[len(items)-1].ID, 10)
	}
	send(w, 200, map[string]any{"data": items, "next_cursor": next})
}

func (h *Handler) contentAuditors(w http.ResponseWriter, r *http.Request) {
	after, limit, ok := pageWithCursorLimit(w, r, 4096)
	if !ok {
		return
	}
	data, next, err := h.Store.ContentAuditors(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), after, limit)
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 200, map[string]any{"data": data, "next_cursor": next})
}
