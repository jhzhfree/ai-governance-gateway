package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
)

func (h *Handler) serveProvider(w http.ResponseWriter, r *http.Request, id, token string, req Request) {
	route, ok := h.Providers.Find(req.Model)
	if !ok || !provider.ValidateRequest(req) {
		problem(w, 400, "unsupported_request", id)
		return
	}
	if !route.Ready() {
		problem(w, 503, "residency_review_required", id)
		return
	}
	amount, err := route.Bound(req.MaxTokens)
	if err != nil {
		problem(w, 400, "unsupported_max_tokens", id)
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
	if scope.Currency != provider.Currency {
		problem(w, 403, "currency_mismatch", id)
		return
	}
	created, err := h.Ledger.Reserve(r.Context(), budget.Quote{Scope: scope, RequestID: id, PriceVersion: route.PriceVersion(), Amount: amount})
	if err != nil || !created {
		if errors.Is(err, budget.ErrExceeded) {
			problem(w, 429, "budget_exceeded", id)
		} else {
			problem(w, 503, "reservation_failed", id)
		}
		return
	}
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
	trace, err := h.beginAttempt(r.Context(), id, req, route.Provider(), route.Region())
	if err != nil {
		h.report(id, "metadata_start_failed")
		problem(w, 503, "metadata_start_failed", id)
		return
	}
	defer trace.abort()
	reply := func(status int, code string) { trace.failed(code); problem(w, status, code, id) }
	dispatchAttempted = true
	if h.Ledger.Dispatch(r.Context(), id) != nil {
		reply(503, "dispatch_failed")
		return
	}
	deadline := time.Now().Add(120 * time.Second)
	if route.ReviewDeadline().Before(deadline) {
		deadline = route.ReviewDeadline()
	}
	ctx, cancel := context.WithDeadline(r.Context(), deadline)
	defer func() {
		if ctx.Err() == context.DeadlineExceeded && !trace.done {
			trace.failed("upstream_timeout")
		}
		cancel()
	}()
	response, err := route.Open(ctx, req)
	if err != nil {
		reply(502, "upstream_unavailable")
		return
	}
	defer response.Body.Close()
	trace.status(response.StatusCode)
	if response.StatusCode != 200 {
		reply(502, "upstream_rejected")
		return
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || (!req.Stream && media != "application/json") || (req.Stream && media != "text/event-stream") {
		reply(502, "upstream_protocol_error")
		return
	}
	settle := func(actual int64) error {
		finish, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := h.Ledger.Settle(finish, id, actual); err != nil {
			return err
		}
		settled = true
		return nil
	}
	if !req.Stream {
		data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
		if err != nil || len(data) > 1<<20 {
			reply(502, "upstream_response_incomplete")
			return
		}
		value, err := provider.Normalize(data, req.Model, id, false)
		if err != nil {
			reply(502, "upstream_protocol_error")
			return
		}
		actual, err := route.ValidateUsage(value.Usage, req.MaxTokens)
		if err != nil {
			reply(502, "upstream_usage_invalid")
			return
		}
		trace.usage(*value.Usage.PromptTokens, *value.Usage.CompletionTokens)
		if settle(actual) != nil {
			reply(503, "settlement_failed")
			return
		}
		if trace.complete() != nil {
			reply(503, "metadata_finalize_failed")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(value)
		return
	}
	streamProvider(w, response.Body, req, id, route, settle, h.report, trace, ctx)
}

func streamProvider(w http.ResponseWriter, body io.Reader, req Request, id string, route *provider.Route, settle func(int64) error, report func(string, string), trace *attemptTrace, ctx context.Context) {
	started, finished := false, false
	var actual *int64
	write := func(data string) error {
		if !started {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-store")
			started = true
		}
		rc := http.NewResponseController(w)
		if err := rc.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
		if _, err := io.WriteString(w, "data: "+data+"\n\n"); err != nil {
			return err
		}
		return rc.Flush()
	}
	fail := func(code string) {
		trace.failed(code)
		if ctx.Err() == context.DeadlineExceeded {
			trace.failed("upstream_timeout")
		}
		report(id, code)
		if started {
			data, _ := json.Marshal(map[string]any{"error": map[string]string{"code": code, "request_id": id}})
			_ = write(string(data))
		} else {
			problem(w, 502, code, id)
		}
	}
	scanner := bufio.NewScanner(io.LimitReader(body, 16<<20))
	scanner.Buffer(make([]byte, 4096), 64<<10)
	var lines []string
	size := 0
	for scanner.Scan() {
		line := scanner.Text()
		size += len(line)
		if size > 64<<10 {
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
		size = 0
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			if actual == nil || !finished {
				fail("upstream_stream_incomplete")
				return
			}
			if settle(*actual) != nil {
				fail("settlement_failed")
				return
			}
			if trace.complete() != nil {
				fail("metadata_finalize_failed")
				return
			}
			_ = write("[DONE]")
			return
		}
		value, err := provider.Normalize([]byte(data), req.Model, id, true)
		if err != nil {
			fail("upstream_stream_incomplete")
			return
		}
		if finished && len(value.Choices) > 0 {
			fail("upstream_stream_incomplete")
			return
		}
		for _, choice := range value.Choices {
			if choice.FinishReason != nil {
				finished = true
			}
		}
		if value.Usage != nil {
			cost, err := route.ValidateUsage(value.Usage, req.MaxTokens)
			if err != nil || actual != nil || !finished {
				fail("upstream_usage_invalid")
				return
			}
			trace.usage(*value.Usage.PromptTokens, *value.Usage.CompletionTokens)
			actual = &cost
		}
		normalized, _ := json.Marshal(value)
		if write(string(normalized)) != nil {
			trace.failed("downstream_write_failed")
			return
		}
	}
	fail("upstream_stream_incomplete")
}
