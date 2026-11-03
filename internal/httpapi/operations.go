package httpapi

import (
	"net/http"
	"strconv"

	"github.com/jhzhfree/ai-governance-gateway/internal/admission"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
)

func (h *Handler) limits(w http.ResponseWriter, r *http.Request) {
	v, err := h.Store.Limits(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"))
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 200, v)
}
func (h *Handler) setLimits(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RPM        *int   `json:"requests_per_minute"`
		Concurrent *int   `json:"max_concurrent"`
		Expected   *int64 `json:"expected_revision"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.RPM == nil || body.Concurrent == nil || body.Expected == nil {
		failure(w, r, control.ErrInvalid)
		return
	}
	v, err := h.Store.SetLimits(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), admission.Policy{RequestsPerMinute: *body.RPM, MaxConcurrent: *body.Concurrent, Revision: *body.Expected})
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 200, v)
}
func (h *Handler) inspect(w http.ResponseWriter, r *http.Request) {
	after, limit, ok := pageWithCursorLimit(w, r, 512)
	if !ok {
		return
	}
	older := 300
	if text := r.URL.Query().Get("older_than_seconds"); text != "" {
		n, err := strconv.Atoi(text)
		if err != nil {
			failure(w, r, control.ErrInvalid)
			return
		}
		older = n
	}
	v, err := h.Store.Inspect(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), after, older, limit)
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 200, v)
}
