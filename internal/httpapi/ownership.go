package httpapi

import (
	"net/http"

	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
)

func (h *Handler) ownership(w http.ResponseWriter, r *http.Request) {
	v, err := h.Store.PendingOwnership(r.Context(), principal(r), r.PathValue("org"))
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 200, map[string]any{"transfer": v})
}
func (h *Handler) requestOwnership(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Target   identity.Principal `json:"target"`
		Expected string             `json:"expected_role"`
		Reason   string             `json:"reason"`
	}
	if !decode(w, r, &body) {
		return
	}
	v, err := h.Store.RequestOwnership(r.Context(), principal(r), r.PathValue("org"), body.Target, body.Expected, body.Reason)
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 201, v)
}
func (h *Handler) finishOwnership(w http.ResponseWriter, r *http.Request) {
	if err := h.Store.FinishOwnership(r.Context(), principal(r), r.PathValue("org"), r.PathValue("transfer"), r.PathValue("action") == "accept"); err != nil {
		failure(w, r, err)
		return
	}
	w.WriteHeader(204)
}
