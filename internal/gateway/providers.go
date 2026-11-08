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
	"github.com/jhzhfree/ai-governance-gateway/internal/circuit"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/keyword"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
)

func (h *Handler) serveProvider(w http.ResponseWriter, r *http.Request, id, token string, req Request) {
	if !h.Providers.Ready() {
		problem(w, 503, "provider_configuration_unavailable", id)
		return
	}
	route, ok := h.Providers.Find(req.Model)
	if !ok || !provider.ValidateRequest(req) || ((req.IsEmbedding() || req.HasTools() || req.HasParts()) && h.Executor == nil) || (ok && (route.API() == "embeddings") != req.IsEmbedding()) {
		problem(w, 400, "unsupported_request", id)
		return
	}
	if r.URL.Path == "/v1/responses" && !h.supportsResponses(route, req) {
		problem(w, 400, "responses_requires_governed_execution", id)
		return
	}
	if r.URL.Path == "/v1/messages" && (h.Executor == nil || route.GovernanceChannelID() <= 0 || !route.RequiresCacheEvidence()) {
		problem(w, 400, "messages_requires_governed_cache_evidence", id)
		return
	}
	if !route.Ready() {
		problem(w, 503, "residency_review_required", id)
		return
	}
	if h.Executor != nil && h.Executor.Validate(route, req) != nil {
		problem(w, 400, "unsupported_execution_request", id)
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
	w, measured := h.measure(w, scope)
	defer measured()
	w, sealContent, captured := h.capture(w, r, id, token, scope, req)
	if !captured {
		return
	}
	defer sealContent()
	guard, allowedKeywords := h.beginKeywords(w, r, id, scope, req)
	if !allowedKeywords {
		return
	}
	r, release, ok := h.admit(w, r, id, scope)
	if !ok {
		return
	}
	defer release()
	ticket, allowed := h.protect(w, r, id, route.PriceVersion())
	if !allowed {
		return
	}
	outcome := circuit.Neutral
	defer func() {
		if r.Context().Err() == context.Canceled {
			outcome = circuit.Neutral
		}
		finish, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if ticket.Complete(finish, outcome) != nil {
			h.report(id, "circuit_update_failed")
		}
	}()
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
	outcome = circuit.Failure
	var response *http.Response
	if h.Executor == nil {
		response, err = route.Open(ctx, req)
	} else {
		response, err = h.Executor.Open(ctx, route, req, id, trace.id)
	}
	if err != nil || response == nil || response.Body == nil {
		reply(502, "upstream_unavailable")
		return
	}
	defer response.Body.Close()
	trace.status(response.StatusCode)
	if response.StatusCode != 200 {
		if response.StatusCode != 408 && response.StatusCode != 429 && response.StatusCode < 500 {
			outcome = circuit.Neutral
		}
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
		var err error
		if h.Completions != nil {
			err = h.Completions.Settle(finish, id, trace.id, actual)
		} else {
			err = h.Ledger.Settle(finish, id, actual)
		}
		if err != nil {
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
		if req.IsEmbedding() {
			value, usage, err := provider.NormalizeEmbedding(data, req)
			if err != nil {
				reply(502, "upstream_protocol_error")
				return
			}
			actual, err := route.ValidateUsage(usage, 0)
			if err != nil {
				reply(502, "upstream_usage_invalid")
				return
			}
			trace.usage(*usage.PromptTokens, 0)
			outcome = circuit.Success
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
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(2 * time.Second))
			_ = json.NewEncoder(w).Encode(value)
			return
		}
		value, err := provider.NormalizeRequest(data, req, id)
		if err != nil || provider.NewToolResponseState(req).Apply(&value, false) != nil {
			reply(502, "upstream_protocol_error")
			return
		}
		actual, err := route.ValidateUsage(value.Usage, req.MaxTokens)
		if err != nil {
			reply(502, "upstream_usage_invalid")
			return
		}
		trace.usage(*value.Usage.PromptTokens, *value.Usage.CompletionTokens)
		outcome = circuit.Success
		if settle(actual) != nil {
			reply(503, "settlement_failed")
			return
		}
		guardCtx, guardCancel := keywordFinishContext()
		guardErr := keywordResponse(guardCtx, guard, &value, false)
		guardCancel()
		if guardErr != nil {
			status, code := keywordProblem(guardErr)
			reply(status, code)
			return
		}
		if trace.complete() != nil {
			reply(503, "metadata_finalize_failed")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(2 * time.Second))
		if r.URL.Path == "/v1/messages" {
			result, err := provider.PublicMessages(value)
			if err != nil {
				reply(502, "upstream_protocol_error")
				return
			}
			_ = json.NewEncoder(w).Encode(result)
		} else if r.URL.Path == "/v1/responses" {
			result, err := provider.PublicResponses(value, req)
			if err != nil {
				reply(502, "upstream_protocol_error")
				return
			}
			_ = json.NewEncoder(w).Encode(result)
		} else {
			_ = json.NewEncoder(w).Encode(value)
		}
		return
	}
	if r.URL.Path == "/v1/responses" {
		w = &responsesStreamWriter{ResponseWriter: w, request: req, id: id}
	}
	var initial *provider.MessagesUsage
	if req.MessagesStream {
		var err error
		initial, err = provider.DecodeMessagesInitialUsage(response.Header.Get("X-Aigw-Initial-Usage"), route.PriceSnapshot().MaxInputTokens, int64(req.MaxTokens))
		if err != nil {
			reply(502, "upstream_usage_invalid")
			return
		}
		w = &messagesStreamWriter{ResponseWriter: w, request: req, id: id, initial: initial}
	}
	outcome = streamProvider(w, response.Body, req, id, route, settle, h.report, trace, ctx, guard, initial)
}

func (h *Handler) supportsResponses(route *provider.Route, req Request) bool {
	if h.Executor == nil {
		return false
	}
	// Preserve the separately governed legacy data-plane contract.
	if route.GovernanceChannelID() > 0 {
		return true
	}
	executor, ok := h.Executor.(PublicProtocolExecutor)
	return h.Completions != nil && ok && executor.SupportsPublicProtocol(route, req, "/v1/responses")
}

func streamProvider(w http.ResponseWriter, body io.Reader, req Request, id string, route *provider.Route, settle func(int64) error, report func(string, string), trace *attemptTrace, ctx context.Context, guard *keyword.Session, initial *provider.MessagesUsage) circuit.Outcome {
	started, finished := false, false
	tools := provider.NewToolResponseState(req)
	var pendingToolFrames [][]byte
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
			status := 502
			if code == "keyword_unavailable" {
				status = 503
			}
			problem(w, status, code, id)
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
			return circuit.Failure
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
				return circuit.Failure
			}
			if settle(*actual) != nil {
				fail("settlement_failed")
				return circuit.Success
			}
			if trace.complete() != nil {
				fail("metadata_finalize_failed")
				return circuit.Success
			}
			for _, frame := range pendingToolFrames {
				if write(string(frame)) != nil {
					trace.failed("downstream_write_failed")
					return circuit.Neutral
				}
			}
			if err := write("[DONE]"); err != nil {
				report(id, "downstream_write_failed")
				return circuit.Neutral
			}
			return circuit.Success
		}
		value, err := provider.NormalizeRequest([]byte(data), req, id)
		if err != nil || tools.Apply(&value, true) != nil {
			fail("upstream_stream_incomplete")
			return circuit.Failure
		}
		if finished && len(value.Choices) > 0 {
			fail("upstream_stream_incomplete")
			return circuit.Failure
		}
		for _, choice := range value.Choices {
			if choice.FinishReason != nil {
				finished = true
			}
		}
		if value.Usage != nil {
			if initial != nil && !initial.MatchesFinal(value.Usage) {
				fail("upstream_usage_invalid")
				return circuit.Failure
			}
			cost, err := route.ValidateUsage(value.Usage, req.MaxTokens)
			if err != nil || actual != nil || !finished {
				fail("upstream_usage_invalid")
				return circuit.Failure
			}
			trace.usage(*value.Usage.PromptTokens, *value.Usage.CompletionTokens)
			actual = &cost
		}
		if err := keywordResponse(ctx, guard, &value, true); err != nil {
			_, code := keywordProblem(err)
			fail(code)
			return circuit.Neutral
		}
		normalized, _ := json.Marshal(value)
		hasCalls := false
		for _, choice := range value.Choices {
			if choice.Delta != nil && len(choice.Delta.ToolCalls) > 0 {
				hasCalls = true
			}
		}
		if hasCalls || len(pendingToolFrames) > 0 {
			if len(pendingToolFrames) >= 2 || len(normalized) > 64<<10 {
				fail("upstream_stream_incomplete")
				return circuit.Failure
			}
			pendingToolFrames = append(pendingToolFrames, normalized)
			continue
		}
		if write(string(normalized)) != nil {
			trace.failed("downstream_write_failed")
			return circuit.Neutral
		}
	}
	fail("upstream_stream_incomplete")
	return circuit.Failure
}
