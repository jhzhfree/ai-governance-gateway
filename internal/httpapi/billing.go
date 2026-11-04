package httpapi

import (
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"net/http"
)

func (h *Handler) importStatement(w http.ResponseWriter, r *http.Request) {
	var in control.ImportInput
	if !decode(w, r, &in) {
		return
	}
	v, err := h.Store.ImportStatement(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), in)
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 200, v)
}
func (h *Handler) billingRecords(w http.ResponseWriter, r *http.Request) {
	after, limit, ok := pageWithCursorLimit(w, r, 512)
	if !ok {
		return
	}
	items, next, err := h.Store.BillingRecords(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), after, r.URL.Query().Get("decision"), limit)
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 200, map[string]any{"data": items, "next_cursor": next})
}
func (h *Handler) reviewBill(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Action    string `json:"action"`
		RequestID string `json:"request_id"`
		Expected  *int   `json:"expected_revision"`
		Final     *bool  `json:"confirmed_final"`
		Reason    string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Expected == nil || in.Final == nil {
		failure(w, r, control.ErrInvalid)
		return
	}
	v, err := h.Store.ReviewBill(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), r.PathValue("record"), control.ReviewInput{Action: in.Action, RequestID: in.RequestID, ExpectedRevision: *in.Expected, ConfirmedFinal: *in.Final, Reason: in.Reason})
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 200, v)
}
