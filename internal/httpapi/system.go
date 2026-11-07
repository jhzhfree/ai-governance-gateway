package httpapi

import (
	"github.com/jhzhfree/ai-governance-gateway/internal/systemhost"
	"net/http"
)

func (h *Handler) systemSnapshot(w http.ResponseWriter, r *http.Request) {
	v, e := h.Store.SystemSnapshot(r.Context(), principal(r))
	if e != nil {
		failure(w, r, e)
		return
	}
	send(w, 200, v)
}
func (h *Handler) systemEvents(w http.ResponseWriter, r *http.Request) {
	v, e := h.Store.SystemEvents(r.Context(), principal(r))
	if e != nil {
		failure(w, r, e)
		return
	}
	send(w, 200, map[string]any{"data": v, "next_cursor": ""})
}
func (h *Handler) systemApply(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Node   string            `json:"node"`
		Reason string            `json:"reason"`
		Change systemhost.Change `json:"change"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, e := h.Store.ChangeSystem(r.Context(), principal(r), b.Node, "apply", "", b.Reason, b.Change)
	if e != nil {
		failure(w, r, e)
		return
	}
	send(w, 200, v)
}
func (h *Handler) systemFinish(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Node   string `json:"node"`
		Reason string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, e := h.Store.ChangeSystem(r.Context(), principal(r), b.Node, r.PathValue("action"), r.PathValue("id"), b.Reason, systemhost.Change{})
	if e != nil {
		failure(w, r, e)
		return
	}
	send(w, 200, v)
}
