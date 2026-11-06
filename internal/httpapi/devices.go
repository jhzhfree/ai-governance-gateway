package httpapi

import (
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"net/http"
	"strconv"
)

func (h *Handler) devices(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		after, _, ok := page(w, r)
		if !ok {
			return
		}
		items, err := h.Store.Devices(r.Context(), principal(r), r.PathValue("org"), after)
		if err != nil {
			failure(w, r, err)
			return
		}
		list(w, items, 50, func(v control.Device) string { return v.ID })
		return
	}
	var body struct {
		Name      string `json:"name"`
		PublicKey string `json:"public_key"`
	}
	if !decode(w, r, &body) {
		return
	}
	v, err := h.Store.RegisterDevice(r.Context(), principal(r), r.PathValue("org"), body.Name, body.PublicKey)
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 201, v)
}
func (h *Handler) changeDevice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		State    string `json:"state"`
		Revision *int64 `json:"expected_revision"`
		Reason   string `json:"reason"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Revision == nil {
		failure(w, r, control.ErrInvalid)
		return
	}
	v, err := h.Store.ChangeDevice(r.Context(), principal(r), r.PathValue("org"), r.PathValue("device"), *body.Revision, body.State, body.Reason)
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 200, v)
}
func (h *Handler) devicePolicy(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		v, err := h.Store.DevicePolicy(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"))
		if err != nil {
			failure(w, r, err)
			return
		}
		send(w, 200, v)
		return
	}
	var body struct {
		Required *bool  `json:"required"`
		Revision *int64 `json:"expected_revision"`
		Reason   string `json:"reason"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Required == nil || body.Revision == nil {
		failure(w, r, control.ErrInvalid)
		return
	}
	v, err := h.Store.SetDevicePolicy(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), *body.Revision, *body.Required, body.Reason)
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 200, v)
}
func (h *Handler) issueDeviceSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Models []string `json:"models"`
	}
	if !decode(w, r, &body) {
		return
	}
	if len(r.Header.Values("DPoP")) != 1 {
		failure(w, r, identity.ErrUnauthenticated)
		return
	}
	v, err := h.Store.IssueDeviceSession(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), r.PathValue("device"), r.Header.Get("DPoP"), body.Models)
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 201, v)
}
func (h *Handler) deviceSessions(w http.ResponseWriter, r *http.Request) {
	after, _, ok := page(w, r)
	if !ok {
		return
	}
	items, err := h.Store.DeviceSessions(r.Context(), principal(r), r.PathValue("org"), r.PathValue("device"), after)
	if err != nil {
		failure(w, r, err)
		return
	}
	list(w, items, 50, func(v control.DeviceSession) string { return v.ID })
}
func (h *Handler) revokeDeviceSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := h.Store.RevokeDeviceSession(r.Context(), principal(r), r.PathValue("org"), r.PathValue("device"), r.PathValue("session"), body.Reason); err != nil {
		failure(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func (h *Handler) deviceRequests(w http.ResponseWriter, r *http.Request) {
	after, _, ok := page(w, r)
	if !ok {
		return
	}
	items, err := h.Store.DeviceRequests(r.Context(), principal(r), r.PathValue("org"), r.PathValue("device"), after)
	if err != nil {
		failure(w, r, err)
		return
	}
	list(w, items, 50, func(v control.DeviceRequest) string { return v.RequestID })
}
func (h *Handler) deviceEvents(w http.ResponseWriter, r *http.Request) {
	after, ok := cursorNumber(w, r)
	if !ok {
		return
	}
	items, err := h.Store.DeviceEvents(r.Context(), principal(r), r.PathValue("org"), r.PathValue("device"), after)
	if err != nil {
		failure(w, r, err)
		return
	}
	list(w, items, 50, func(v control.DeviceEvent) string { return strconv.FormatInt(v.Revision, 10) })
}
