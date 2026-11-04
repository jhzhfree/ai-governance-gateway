package httpapi

import (
	"net/http"
	"strconv"

	"github.com/jhzhfree/ai-governance-gateway/internal/control"
)

func (h *Handler) alertPolicy(w http.ResponseWriter, r *http.Request) {
	v, err := h.Store.AlertPolicy(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"))
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 200, v)
}
func (h *Handler) setAlertPolicy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled    *bool  `json:"enabled"`
		Thresholds []int  `json:"thresholds"`
		Expected   *int64 `json:"expected_revision"`
		Reason     string `json:"reason"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Enabled == nil || body.Expected == nil {
		failure(w, r, control.ErrInvalid)
		return
	}
	v := control.AlertPolicy{Enabled: *body.Enabled, Thresholds: body.Thresholds, Revision: *body.Expected}
	if err := h.Store.SetAlertPolicy(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), v, body.Reason); err != nil {
		failure(w, r, err)
		return
	}
	h.alertPolicy(w, r)
}
func (h *Handler) budgetAlerts(w http.ResponseWriter, r *http.Request) {
	after, ok := cursorNumber(w, r)
	if !ok {
		return
	}
	items, err := h.Store.BudgetAlerts(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), after)
	if err != nil {
		failure(w, r, err)
		return
	}
	next := ""
	if len(items) > 50 {
		items = items[:50]
		next = strconv.FormatInt(items[49].ID, 10)
	}
	send(w, 200, map[string]any{"data": items, "next_cursor": next})
}
func (h *Handler) acknowledgeBudgetAlert(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("alert"), 10, 64)
	if err != nil || id < 1 {
		failure(w, r, control.ErrInvalid)
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := h.Store.AcknowledgeBudgetAlert(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), id, body.Reason); err != nil {
		failure(w, r, err)
		return
	}
	w.WriteHeader(204)
}
