package httpapi

import (
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"net/http"
)

func (h *Handler) members(w http.ResponseWriter, r *http.Request) {
	after, limit, ok := pageWithCursorLimit(w, r, 4096)
	if !ok {
		return
	}
	items, next, err := h.Store.ListMembers(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), after, limit)
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 200, map[string]any{"data": items, "next_cursor": next})
}
func (h *Handler) changeMember(w http.ResponseWriter, r *http.Request) {
	var body struct {
		control.Member
		Expected *string `json:"expected_role"`
	}
	if !decode(w, r, &body) {
		return
	}
	err := h.Store.ChangeMember(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), body.Member, body.Expected)
	if err != nil {
		failure(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func (h *Handler) removeProjectMember(w http.ResponseWriter, r *http.Request) {
	var body struct {
		identity.Principal
		Expected *string `json:"expected_role"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Expected == nil {
		failure(w, r, control.ErrInvalid)
		return
	}
	if err := h.Store.RemoveProjectMember(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), body.Principal, *body.Expected); err != nil {
		failure(w, r, err)
		return
	}
	w.WriteHeader(204)
}
