// Package gateway is a mock-only SSE lifecycle prototype. It deliberately does
// not implement general tokenization or production supplier routing.
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

	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
)

type Ledger interface {
	Reserve(context.Context, budget.Quote) (bool, error)
	Dispatch(context.Context, string) error
	MarkUncertain(context.Context, string) error
	Settle(context.Context, string, int64) error
}

type Handler struct {
	Ledger    Ledger
	Authorize func(context.Context, string, string) (budget.Scope, error)
	Client    *http.Client
	Upstream  string // trusted fixture URL, not a client-supplied URL
	// Report receives operational failures without prompts, tokens or raw upstream errors.
	Report func(requestID, code string)
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type Request struct {
	Model     string    `json:"model"`
	Stream    bool      `json:"stream"`
	MaxTokens int       `json:"max_tokens"`
	Messages  []Message `json:"messages"`
}

func problem(w http.ResponseWriter, status int, code, id string) {
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
	if r.URL.Path != "/v1/chat/completions" {
		problem(w, 404, "not_found", id)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		problem(w, 405, "method_not_allowed", id)
		return
	}
	if h.Ledger == nil || h.Client == nil || h.Upstream == "" || h.Authorize == nil {
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
	defer func() {
		if !settled {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := h.Ledger.MarkUncertain(ctx, id); err != nil {
				h.report(id, "uncertain_transition_failed")
			}
		}
	}()
	if err := h.Ledger.Dispatch(r.Context(), id); err != nil {
		problem(w, 503, "dispatch_failed", id)
		return
	}
	body, err := json.Marshal(req)
	if err != nil {
		problem(w, 500, "encode_failed", id)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, h.Upstream, bytes.NewReader(body))
	if err != nil {
		problem(w, 502, "upstream_request_failed", id)
		return
	}
	upstream.Header.Set("Content-Type", "application/json")
	resp, err := h.Client.Do(upstream)
	if err != nil {
		problem(w, 502, "upstream_unavailable", id)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || strings.Split(resp.Header.Get("Content-Type"), ";")[0] != "text/event-stream" {
		problem(w, 502, "upstream_rejected", id)
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
			cost := u.Total
			actual = &cost
		}
		if err := write(data); err != nil {
			return
		}
	}
	// EOF without DONE, malformed/oversized events and transport errors are all
	// incomplete; no success marker and no automatic replay/refund.
	fail("upstream_stream_incomplete")
}
