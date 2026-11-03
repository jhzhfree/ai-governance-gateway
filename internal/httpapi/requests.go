package httpapi

import (
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"net/http"
)

func (h *Handler) requests(w http.ResponseWriter, r *http.Request) {
	after, limit, ok := pageWithCursorLimit(w, r, 512)
	if !ok {
		return
	}
	state := r.URL.Query().Get("state")
	items, err := h.Store.ListRequests(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), after, state, limit+1)
	if err != nil {
		failure(w, r, err)
		return
	}
	list(w, items, limit, func(item control.RequestRecord) string { return item.Cursor() })
}
func (h *Handler) request(w http.ResponseWriter, r *http.Request) {
	item, err := h.Store.GetRequest(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), r.PathValue("request"))
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 200, item)
}
