package gateway

import (
	"errors"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"net/http"
	"strings"
)

func inferenceToken(r *http.Request) (string, error) {
	if len(r.Header.Values("Authorization")) != 1 {
		return "", identity.ErrUnauthenticated
	}
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || len(parts[1]) > 16<<10 {
		return "", identity.ErrUnauthenticated
	}
	if strings.HasPrefix(parts[1], "aigw_dt_") {
		if !strings.EqualFold(parts[0], "DPoP") {
			return "", identity.ErrUnauthenticated
		}
		return parts[1], nil
	}
	return identity.Bearer(r.Header.Get("Authorization"))
}
func (h *Handler) deviceRequest(w http.ResponseWriter, r *http.Request, id, token string) (*http.Request, bool) {
	if !strings.HasPrefix(token, "aigw_dt_") {
		return r, true
	}
	if h.DeviceProof == nil {
		problem(w, 401, "unauthenticated", id)
		return r, false
	}
	ctx, err := h.DeviceProof(r.Context(), r, token)
	if err != nil {
		status, code := 503, "device_admission_unavailable"
		if errors.Is(err, identity.ErrUnauthenticated) {
			status, code = 401, "invalid_device_proof"
		} else if errors.Is(err, identity.ErrForbidden) {
			status, code = 403, "device_forbidden"
		}
		problem(w, status, code, id)
		return r, false
	}
	return r.WithContext(ctx), true
}
