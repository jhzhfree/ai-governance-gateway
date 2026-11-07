package httpapi

import (
	"net/http"
	"strconv"

	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/newapibridge"
)

func (h *Handler) newAPIChannels(w http.ResponseWriter, r *http.Request) {
	v, err := h.Store.NewAPIChannels(r.Context(), principal(r), r.URL.Query().Get("after"))
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 200, v)
}
func (h *Handler) writeNewAPIChannel(w http.ResponseWriter, r *http.Request) {
	id := 0
	if raw := r.PathValue("channel"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || strconv.Itoa(n) != raw {
			failure(w, r, control.ErrInvalid)
			return
		}
		id = n
	}
	var in newapibridge.ChannelWrite
	if !decodeLimit(w, r, &in, 80<<10) {
		return
	}
	v, err := h.Store.WriteNewAPIChannel(r.Context(), principal(r), id, in)
	in.APIKey = nil
	if err != nil {
		failure(w, r, err)
		return
	}
	status := 200
	if v.State == "pending" {
		status = 202
	}
	send(w, status, v)
}
func (h *Handler) newAPIChannelOperations(w http.ResponseWriter, r *http.Request) {
	v, err := h.Store.NewAPIChannelOperations(r.Context(), principal(r))
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 200, map[string]any{"data": v})
}
func (h *Handler) recoverNewAPIChannelOperation(w http.ResponseWriter, r *http.Request) {
	v, err := h.Store.RecoverNewAPIChannelOperation(r.Context(), principal(r), r.PathValue("operation"))
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 200, v)
}
