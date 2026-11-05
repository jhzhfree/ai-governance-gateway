package httpapi

import (
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"net/http"
	"strconv"
)

func (h *Handler) departments(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		v, err := h.Store.Departments(r.Context(), principal(r), r.PathValue("org"))
		if err != nil {
			failure(w, r, err)
			return
		}
		send(w, 200, v)
		return
	}
	var body struct {
		Configuration control.DepartmentConfiguration `json:"configuration"`
		Reason        string                          `json:"reason"`
	}
	if !decodeLimit(w, r, &body, 1<<20) {
		return
	}
	v, err := h.Store.SetDepartments(r.Context(), principal(r), r.PathValue("org"), body.Configuration, body.Reason)
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 200, v)
}
func (h *Handler) departmentHistory(w http.ResponseWriter, r *http.Request) {
	after, ok := cursorNumber(w, r)
	if !ok {
		return
	}
	items, err := h.Store.DepartmentHistory(r.Context(), principal(r), r.PathValue("org"), after)
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
