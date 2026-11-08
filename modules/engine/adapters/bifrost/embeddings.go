package bifrost

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/completion"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/modules/engine/execution"
)

func (e *Executor) embedding(ctx context.Context, req execution.Request, emit execution.Emit) error {
	if _, ok := object(req.Body, "model", "input", "encoding_format", "dimensions"); !ok {
		return execution.ErrInvalidRequest
	}
	var public provider.EmbeddingRequest
	if json.Unmarshal(req.Body, &public) != nil || public.Model != e.model {
		return execution.ErrInvalidRequest
	}
	governed, err := public.Request()
	if err != nil || provider.ValidateEmbeddedEmbedding(e.profile.id, governed) != nil {
		return execution.ErrInvalidRequest
	}
	// The SDK's JSON path compacts raw response bytes before exposing them.
	// Like the strict SSE receiver, use the existing guarded network client so
	// the wire-size cap precedes parsing and evidence bytes remain unchanged.
	ctx, release, err := e.lifecycle.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	parent := ctx
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, e.streamURL, bytes.NewReader(req.Body))
	if err != nil {
		return execution.ErrInvalidRequest
	}
	request.Header.Set("Authorization", "Bearer "+e.streamKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", "identity")
	timer := time.AfterFunc(10*time.Second, func() { cancel(context.DeadlineExceeded) })
	response, err := e.streamHTTP.Do(request)
	timer.Stop()
	if err != nil {
		return streamError(parent, ctx, execution.ErrUpstream)
	}
	defer response.Body.Close()
	media, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || mediaErr != nil || media != "application/json" || response.ContentLength > 1<<20 || (response.Header.Get("Content-Encoding") != "" && response.Header.Get("Content-Encoding") != "identity") {
		return execution.ErrUpstream
	}
	raw, err := io.ReadAll(io.LimitReader(&idleReader{Reader: response.Body, cancel: cancel}, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return streamError(parent, ctx, execution.ErrUpstream)
	}
	fields, ok := object(raw, "id", "model", "object", "data", "usage")
	if !ok {
		return execution.ErrUpstream
	}
	counters, err := completion.EmbeddingUsage(fields["usage"])
	if err != nil {
		return execution.ErrUpstream
	}
	var entries []json.RawMessage
	if json.Unmarshal(fields["data"], &entries) != nil {
		return execution.ErrUpstream
	}
	for _, entry := range entries {
		values, ok := object(entry, "object", "index", "embedding")
		if !ok {
			return execution.ErrUpstream
		}
		var vector []*float64
		if json.Unmarshal(values["embedding"], &vector) != nil {
			return execution.ErrUpstream
		}
		for _, value := range vector {
			if value == nil {
				return execution.ErrUpstream
			}
		}
	}
	normalized, _, err := provider.NormalizeEmbedding(raw, governed)
	if err != nil {
		return execution.ErrUpstream
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return execution.ErrUpstream
	}
	// Raw provider content remains local; only the numeric source object crosses
	// the evidence boundary, unchanged, before the consumer may observe success.
	return emit(ctx, execution.Event{RequestID: req.RequestID, AttemptID: req.AttemptID, UpstreamStatus: 200, Completed: true, Payload: payload, Usage: &execution.Usage{InputTokens: int(counters.Input), OutputTokens: 0, TotalTokens: int(counters.Total), Evidence: append(json.RawMessage(nil), fields["usage"]...)}})
}
