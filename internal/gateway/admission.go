package gateway

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/admission"
	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
)

func (h *Handler) admit(w http.ResponseWriter, r *http.Request, id string, scope budget.Scope) (*http.Request, func(), bool) {
	if h.Admission == nil {
		problem(w, 503, "admission_unavailable", id)
		return r, nil, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), admission.MaxRequestDuration)
	acquireCtx, stopAcquire := context.WithTimeout(ctx, 3*time.Second)
	permit, err := h.Admission.Acquire(acquireCtx, scope.Organization, scope.Project, id)
	stopAcquire()
	if err != nil {
		cancel()
		var denied *admission.Denied
		if errors.As(err, &denied) {
			w.Header().Set("Retry-After", strconv.Itoa(denied.RetryAfter))
			problem(w, 429, denied.Code, id)
		} else {
			h.report(id, "admission_unavailable")
			problem(w, 503, "admission_unavailable", id)
		}
		return r, nil, false
	}
	release := func() {
		cancel()
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if permit.Release(cleanup) != nil {
			h.report(id, "admission_release_failed")
		}
	}
	return r.WithContext(ctx), release, true
}
