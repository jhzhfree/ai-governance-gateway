package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
)

func (h *Handler) models(w http.ResponseWriter, r *http.Request, id string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != "GET" {
		w.Header().Set("Allow", "GET")
		problem(w, 405, "method_not_allowed", id)
		return
	}
	if len(r.Header.Values("Authorization")) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="inference"`)
		problem(w, 401, "unauthenticated", id)
		return
	}
	token, err := identity.Bearer(r.Header.Get("Authorization"))
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="inference"`)
		problem(w, 401, "unauthenticated", id)
		return
	}
	if h.ListModels == nil {
		problem(w, 503, "authorization_unavailable", id)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	items, err := h.ListModels(ctx, token)
	if err != nil {
		if errors.Is(err, identity.ErrUnauthenticated) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="inference"`)
			problem(w, 401, "unauthenticated", id)
		} else {
			problem(w, 503, "authorization_unavailable", id)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(2 * time.Second))
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": items})
}
