// Package gatewaybridge connects one immutable, server-owned execution binding
// to the existing governance pipeline. It does not own identity, prices or money.
package gatewaybridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/jhzhfree/ai-governance-gateway/internal/gateway"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/modules/engine/execution"
)

// Binding must be rebuilt by its owner when the trusted route or credentials
// change. It never chooses a fallback or creates a second physical attempt.
// The owner closes the Executor after all bound requests have been reclaimed.
type Binding struct {
	executor               execution.Executor
	revision               [32]byte
	price, alias, upstream string
}

var _ gateway.ProviderExecutor = (*Binding)(nil)

func New(route *provider.Route, executor execution.Executor) (*Binding, error) {
	if route == nil || !route.Ready() || executor == nil {
		return nil, execution.ErrInvalidRequest
	}
	alias, upstream := route.ExecutionModel()
	return &Binding{revision: route.ExecutionIdentity(), executor: executor, price: route.PriceVersion(), alias: alias, upstream: upstream}, nil
}

func (b *Binding) Validate(route *provider.Route, req provider.Request) error {
	if b == nil || route == nil || !route.Ready() || route.PriceVersion() != b.price || route.ExecutionIdentity() != b.revision || req.Model != b.alias || req.MaxTokens < 16 || !provider.ValidateRequest(req) || req.HasTools() {
		return execution.ErrInvalidRequest
	}
	if _, err := route.Bound(req.MaxTokens); err != nil {
		return execution.ErrInvalidRequest
	}
	return nil
}

// Open adapts synchronous events to a bounded, unbuffered response body. Closing
// the body cancels the engine and waits for its producer: no detached consumers.
// The response opens only after the adapter supplies a successful upstream event.
func (b *Binding) Open(ctx context.Context, route *provider.Route, req provider.Request, requestID, attemptID string) (*http.Response, error) {
	if err := b.Validate(route, req); err != nil {
		return nil, err
	}
	if requestID == "" || attemptID == "" {
		return nil, execution.ErrInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	req.Model = b.upstream
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, execution.ErrInvalidRequest
	}
	ctx, cancel := context.WithCancel(ctx)
	reader, writer := io.Pipe()
	done := make(chan struct{})
	ready := make(chan error, 1)
	stop := context.AfterFunc(ctx, func() { _ = writer.CloseWithError(ctx.Err()) })
	body := &responseBody{PipeReader: reader, cancel: cancel, done: done}
	go func() {
		defer close(done)
		defer cancel()
		defer stop()
		count := 0
		totalBytes := 0
		usageSeen := false
		err := b.executor.Execute(ctx, execution.Request{RequestID: requestID, AttemptID: attemptID, Body: payload}, func(ctx context.Context, event execution.Event) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if event.UpstreamStatus != http.StatusOK || event.RequestID != requestID || event.AttemptID != attemptID || len(event.Payload) > 1<<20 || (!req.Stream && count != 0) {
				return execution.ErrUpstream
			}
			var data map[string]json.RawMessage
			if json.Unmarshal(event.Payload, &data) != nil || data == nil {
				return execution.ErrUpstream
			}
			// SDK-normalized payload can contain synthetic zero usage. Only explicit
			// evidence is allowed to reach the authoritative billing validator.
			delete(data, "usage")
			if u := event.Usage; u != nil {
				if usageSeen || u.InputTokens < 1 || u.InputTokens > 2000000 || u.OutputTokens < 0 || u.OutputTokens > req.MaxTokens || u.TotalTokens != u.InputTokens+u.OutputTokens {
					return execution.ErrUpstream
				}
				usageSeen = true
				raw, _ := json.Marshal(map[string]int{"prompt_tokens": u.InputTokens, "completion_tokens": u.OutputTokens, "total_tokens": u.TotalTokens})
				data["usage"] = raw
			}
			normalized, err := json.Marshal(data)
			if err != nil {
				return execution.ErrUpstream
			}
			if req.Stream {
				normalized = append(append([]byte("data: "), normalized...), '\n', '\n')
			}
			totalBytes += len(normalized)
			if totalBytes > 16<<20 || (req.Stream && len(normalized) > 64<<10) || (!req.Stream && totalBytes > 1<<20) {
				return execution.ErrUpstream
			}
			if count == 0 {
				ready <- nil
			}
			count++
			_, err = writer.Write(normalized)
			return err
		})
		if err == nil && count == 0 {
			err = execution.ErrUpstream
		}
		if err == nil && req.Stream {
			_, err = io.WriteString(writer, "data: [DONE]\n\n")
		}
		if err != nil {
			// Never expose engine/provider error strings through the response reader.
			if errors.Is(err, context.Canceled) {
				err = context.Canceled
			} else if errors.Is(err, context.DeadlineExceeded) {
				err = context.DeadlineExceeded
			} else {
				err = execution.ErrUpstream
			}
		}
		if count == 0 {
			ready <- err
		}
		_ = writer.CloseWithError(err)
	}()
	select {
	case err := <-ready:
		if err != nil {
			_ = body.Close()
			return nil, err
		}
	case <-ctx.Done():
		_ = body.Close()
		return nil, ctx.Err()
	}
	media := "application/json"
	if req.Stream {
		media = "text/event-stream"
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{media}}, Body: body}, nil
}

type responseBody struct {
	*io.PipeReader
	cancel context.CancelFunc
	done   <-chan struct{}
	once   sync.Once
}

func (b *responseBody) Close() error {
	b.once.Do(func() { b.cancel(); _ = b.PipeReader.Close(); <-b.done })
	return nil
}
