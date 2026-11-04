// Package gateway enforces project authorization and durable budget settlement.
package gateway

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/admission"
	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/circuit"
	"github.com/jhzhfree/ai-governance-gateway/internal/contentaudit"
	"github.com/jhzhfree/ai-governance-gateway/internal/execution"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/observe"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
)

type Ledger interface {
	Reserve(context.Context, budget.Quote) (bool, error)
	Dispatch(context.Context, string) error
	MarkUncertain(context.Context, string) error
	Settle(context.Context, string, int64) error
}

type Handler struct {
	Content    *contentaudit.Service
	ListModels func(context.Context, string) ([]provider.InferenceModel, error)
	Circuits   circuit.Gate
	Monitor    *observe.Registry
	Admission  admission.Gate
	Attempts   execution.Recorder
	Providers  provider.Routing
	Ledger     Ledger
	Authorize  func(context.Context, string, string) (budget.Scope, error)
	Client     *http.Client
	Upstream   string // trusted fixture URL, not a client-supplied URL
	// Report receives operational failures without prompts, tokens or raw upstream errors.
	Report func(requestID, code string)
}

type Message = provider.Message
type Request = provider.Request

func problem(w http.ResponseWriter, status int, code, id string) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(2 * time.Second))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": code, "request_id": id}})
}

func (h *Handler) report(id, code string) {
	if h.Report != nil {
		h.Report(id, code)
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := rand.Text()
	w.Header().Set("X-Request-ID", id)
	if r.URL.Path == "/v1/models" {
		h.models(w, r, id)
		return
	}
	if r.URL.Path != "/v1/chat/completions" {
		problem(w, 404, "not_found", id)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		problem(w, 405, "method_not_allowed", id)
		return
	}
	if h.Ledger == nil || h.Authorize == nil || (h.Providers == nil && (h.Client == nil || h.Upstream == "")) {
		problem(w, 503, "not_configured", id)
		return
	}
	var req Request
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
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		problem(w, 400, "invalid_request", id)
		return
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		problem(w, 400, "invalid_request", id)
		return
	}
	if h.Providers != nil {
		h.serveProvider(w, r, id, token, req)
		return
	}
	if req.Model != "mock-text" || !req.Stream || req.MaxTokens < 1 || req.MaxTokens > 8 || len(req.Messages) != 1 || req.Messages[0].Role != "user" {
		problem(w, 400, "unsupported_mock_request", id)
		return
	}
	scope, err := h.Authorize(r.Context(), token, req.Model)
	if err != nil {
		switch {
		case errors.Is(err, identity.ErrUnauthenticated):
			w.Header().Set("WWW-Authenticate", `Bearer realm="inference"`)
			problem(w, 401, "unauthenticated", id)
		case errors.Is(err, identity.ErrForbidden):
			problem(w, 403, "model_forbidden", id)
		default:
			problem(w, 503, "authorization_unavailable", id)
		}
		return
	}
	w, measured := h.measure(w, scope)
	defer measured()
	w, sealContent, captured := h.capture(w, r, id, token, scope, req)
	if !captured {
		return
	}
	defer sealContent()
	r, release, ok := h.admit(w, r, id, scope)
	if !ok {
		return
	}
	defer release()
	q := budget.Quote{Scope: scope, RequestID: id, PriceVersion: "mock-v1", Amount: int64(2 + req.MaxTokens)}
	created, err := h.Ledger.Reserve(r.Context(), q)
	if err != nil || !created {
		if errors.Is(err, budget.ErrExceeded) {
			problem(w, 429, "budget_exceeded", id)
		} else {
			problem(w, 503, "reservation_failed", id)
		}
		return
	}
	// Once a reservation exists, terminal cleanup survives client cancellation.
	// A dispatch failure may have committed despite a lost DB response: preserve
	// the hold, never assume that refunding is safe.
	settled := false
	dispatchAttempted := false
	defer func() {
		if !settled && dispatchAttempted {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := h.Ledger.MarkUncertain(ctx, id); err != nil {
				h.report(id, "uncertain_transition_failed")
			}
		}
	}()
	trace, err := h.beginAttempt(r.Context(), id, req, "mock", "local")
	if err != nil {
		h.report(id, "metadata_start_failed")
		problem(w, 503, "metadata_start_failed", id)
		return
	}
	defer trace.abort()
	reply := func(status int, code string) { trace.failed(code); problem(w, status, code, id) }
	dispatchAttempted = true
	if err := h.Ledger.Dispatch(r.Context(), id); err != nil {
		reply(503, "dispatch_failed")
		return
	}
	body, err := json.Marshal(req)
	if err != nil {
		reply(500, "encode_failed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer func() {
		if ctx.Err() == context.DeadlineExceeded && !trace.done {
			trace.failed("upstream_timeout")
		}
		cancel()
	}()
	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, h.Upstream, bytes.NewReader(body))
	if err != nil {
		reply(502, "upstream_request_failed")
		return
	}
	upstream.Header.Set("Content-Type", "application/json")
	resp, err := h.Client.Do(upstream)
	if err != nil {
		reply(502, "upstream_unavailable")
		return
	}
	defer resp.Body.Close()
	trace.status(resp.StatusCode)
	if resp.StatusCode != 200 || strings.Split(resp.Header.Get("Content-Type"), ";")[0] != "text/event-stream" {
		reply(502, "upstream_rejected")
		return
	}
	started := false
	var actual *int64
	write := func(data string) error {
		if !started {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			started = true
		}
		rc := http.NewResponseController(w)
		if err := rc.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
		// Preserve multiline SSE data framing rather than writing bare lines.
		framed := "data: " + strings.ReplaceAll(data, "\n", "\ndata: ") + "\n\n"
		if _, err := io.WriteString(w, framed); err != nil {
			return err
		}
		return rc.Flush()
	}
	fail := func(code string) {
		trace.failed(code)
		if ctx.Err() == context.DeadlineExceeded {
			trace.failed("upstream_timeout")
		}
		h.report(id, code)
		if started {
			data, _ := json.Marshal(map[string]any{"error": map[string]string{"code": code, "request_id": id}})
			_ = write(string(data))
		} else {
			problem(w, 502, code, id)
		}
	}
	scan := bufio.NewScanner(resp.Body)
	scan.Buffer(make([]byte, 4096), 64<<10)
	var lines []string
	eventBytes := 0
	for scan.Scan() {
		line := scan.Text()
		eventBytes += len(line)
		if eventBytes > 64<<10 {
			fail("upstream_stream_incomplete")
			return
		}
		if line != "" {
			if strings.HasPrefix(line, "data:") {
				lines = append(lines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
			continue
		}
		data := strings.Join(lines, "\n")
		lines = nil
		eventBytes = 0
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			if actual == nil {
				fail("upstream_stream_incomplete")
				return
			}
			finish, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			err := h.Ledger.Settle(finish, id, *actual)
			cancel()
			if err != nil {
				fail("settlement_failed")
				return
			}
			settled = true
			if trace.complete() != nil {
				fail("metadata_finalize_failed")
				return
			}
			_ = write("[DONE]")
			return
		}
		var chunk struct {
			Error   json.RawMessage   `json:"error"`
			Choices []json.RawMessage `json:"choices"`
			Usage   *struct {
				Prompt     int64 `json:"prompt_tokens"`
				Completion int64 `json:"completion_tokens"`
				Total      int64 `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil || len(chunk.Error) > 0 || (chunk.Usage == nil && chunk.Choices == nil) {
			fail("upstream_stream_incomplete")
			return
		}
		if chunk.Usage != nil {
			u := chunk.Usage
			if actual != nil || u.Prompt != 2 || u.Completion < 0 || u.Completion > int64(req.MaxTokens) || u.Total != u.Prompt+u.Completion {
				fail("upstream_stream_incomplete")
				return
			}
			trace.usage(u.Prompt, u.Completion)
			cost := u.Total
			actual = &cost
		}
		if err := write(data); err != nil {
			trace.failed("downstream_write_failed")
			return
		}
	}
	// EOF without DONE, malformed/oversized events and transport errors are all
	// incomplete; no success marker and no automatic replay/refund.
	fail("upstream_stream_incomplete")
}
