package httpapi

import "net/http"

func (h *Handler) runtime(w http.ResponseWriter, r *http.Request) {
	org, project := r.PathValue("org"), r.PathValue("project")
	v, err := h.Store.Runtime(r.Context(), principal(r), org, project)
	if err != nil {
		failure(w, r, err)
		return
	}
	v.Instance = h.Monitor.Snapshot(org, project)
	send(w, 200, v)
}
