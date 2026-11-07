package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/contentaudit"
)

// auditWriter observes normalized gateway output, never supplier headers or credentials.
type auditWriter struct {
	http.ResponseWriter
	capture               *contentaudit.Capture
	ctx                   context.Context
	failed                bool
	status                int
	stream, started, done bool
	buffer                bytes.Buffer
}

func (w *auditWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *auditWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *auditWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	if w.failed {
		return 0, contentaudit.ErrUnavailable
	}
	checkpoint, stop := context.WithTimeout(w.ctx, 3*time.Second)
	err := w.capture.Checkpoint(checkpoint, data)
	stop()
	if err != nil {
		w.failed = true
		return 0, contentaudit.ErrUnavailable
	}
	// The persistence round trip must not consume the downstream write deadline.
	_ = http.NewResponseController(w.ResponseWriter).SetWriteDeadline(time.Now().Add(2 * time.Second))
	if strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
		w.stream = true
		if bytes.Equal(data, []byte("data: [DONE]\n\n")) {
			w.done = true
			return len(data), nil
		}
		if !w.started {
			w.ResponseWriter.WriteHeader(w.status)
			w.started = true
		}
		return w.ResponseWriter.Write(data)
	}
	// Non-stream responses are already normalized and bounded by the gateway.
	if w.buffer.Len()+len(data) > 2<<20 {
		return 0, contentaudit.ErrUnavailable
	}
	return w.buffer.Write(data)
}
func (h *Handler) capture(w http.ResponseWriter, r *http.Request, id, token string, scope budget.Scope, req Request) (http.ResponseWriter, func(), bool) {
	if h.Content == nil {
		return w, func() {}, true
	}
	body, err := json.Marshal(req)
	if req.HasImages() {
		images, imageErr := req.ImageMetadata()
		if imageErr != nil {
			problem(w, 400, "invalid_image_input", id)
			return w, nil, false
		}
		// Put attachment identity before binary content so bounded captures retain
		// every image digest even when the consented payload limit truncates bytes.
		body, err = json.Marshal(struct {
			Images  any     `json:"images"`
			Request Request `json:"request"`
		}{images, req})
	}
	if err != nil {
		problem(w, 503, "content_audit_unavailable", id)
		return w, nil, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	c, err := h.Content.Begin(ctx, contentaudit.Meta{RequestID: id, Organization: scope.Organization, Project: scope.Project, Model: req.Model, KeyPrefix: token[:min(16, len(token))]}, body)
	cancel()
	if err != nil {
		h.report(id, "content_audit_start_failed")
		problem(w, 503, "content_audit_unavailable", id)
		return w, nil, false
	}
	if c == nil {
		return w, func() {}, true
	}
	wrapped := &auditWriter{ResponseWriter: w, capture: c, ctx: r.Context()}
	finish := func() {
		status := wrapped.status
		if status == 0 {
			status = 500
		}
		state := "incomplete"
		if (!wrapped.stream && wrapped.buffer.Len() > 0) || wrapped.done {
			state = "complete"
		}
		if wrapped.failed {
			if !wrapped.started {
				status = 503
			}
			state = "incomplete"
		}
		if r.Context().Err() != nil {
			state = "canceled"
		}
		persist, stop := context.WithTimeout(context.Background(), 3*time.Second)
		err := c.Finish(persist, status, state)
		stop()
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(2 * time.Second))
		if err != nil || wrapped.failed {
			h.report(id, "content_audit_seal_failed")
			if wrapped.started {
				_, _ = w.Write([]byte("data: {\"error\":{\"code\":\"content_audit_unavailable\"}}\n\n"))
				_ = http.NewResponseController(w).Flush()
			} else {
				problem(w, 503, "content_audit_unavailable", id)
			}
			return
		}
		if wrapped.stream {
			if wrapped.done {
				_, _ = w.Write([]byte("data: [DONE]\n\n"))
				_ = http.NewResponseController(w).Flush()
			}
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write(wrapped.buffer.Bytes())
	}
	return wrapped, finish, true
}
