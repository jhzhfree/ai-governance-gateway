package httpapi

import (
	"net/http"
	"strconv"

	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
)

func (h *Handler) memberEnabled(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Target      identity.Principal `json:"target"`
		Expected    *int64             `json:"expected_revision"`
		Enabled     *bool              `json:"enabled"`
		RestoreRole string             `json:"restore_role"`
		Reason      string             `json:"reason"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Expected == nil || body.Enabled == nil {
		failure(w, r, control.ErrInvalid)
		return
	}
	v, err := h.Store.SetMemberEnabled(r.Context(), principal(r), r.PathValue("org"), body.Target, *body.Expected, *body.Enabled, body.RestoreRole, body.Reason)
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 200, v)
}
func (h *Handler) memberLifecycle(w http.ResponseWriter, r *http.Request) {
	after, ok := cursorNumber(w, r)
	if !ok {
		return
	}
	target := identity.Principal{Issuer: r.URL.Query().Get("issuer"), Subject: r.URL.Query().Get("subject")}
	items, err := h.Store.MemberLifecycle(r.Context(), principal(r), r.PathValue("org"), target, after)
	if err != nil {
		failure(w, r, err)
		return
	}
	next := ""
	if len(items) > 50 {
		items = items[:50]
		next = strconv.FormatInt(items[49].Revision, 10)
	}
	send(w, 200, map[string]any{"data": items, "next_cursor": next})
}
