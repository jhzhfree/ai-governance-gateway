package gateway

import (
	"context"
	"errors"
	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/circuit"
	"net/http"
	"strconv"
	"time"
)

type measuredWriter struct {
	http.ResponseWriter
	status int
}

func (w *measuredWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *measuredWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *measuredWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(data)
}
func (h *Handler) measure(w http.ResponseWriter, scope budget.Scope) (http.ResponseWriter, func()) {
	started := time.Now()
	mw := &measuredWriter{ResponseWriter: w}
	return mw, func() {
		status := mw.status
		if status == 0 {
			status = 200
		}
		h.Monitor.Record(scope.Organization, scope.Project, status, time.Since(started))
	}
}
func (h *Handler) protect(w http.ResponseWriter, r *http.Request, id, key string) (circuit.Permit, bool) {
	if h.Circuits == nil {
		problem(w, 503, "circuit_unavailable", id)
		return nil, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	ticket, err := h.Circuits.Acquire(ctx, key)
	if err != nil {
		var denied *circuit.Denied
		if errors.As(err, &denied) {
			w.Header().Set("Retry-After", strconv.Itoa(denied.RetryAfter))
			problem(w, 503, "supplier_circuit_open", id)
		} else {
			h.report(id, "circuit_unavailable")
			problem(w, 503, "circuit_unavailable", id)
		}
		return nil, false
	}
	return ticket, true
}
