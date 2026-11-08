package bifrost

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jhzhfree/ai-governance-gateway/modules/engine/execution"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
)

const maxJSONResponseBytes = 1 << 20

// receiveJSON bounds original bytes before any SDK decoding or compaction. It
// shares the SDK's guarded network client with SSE; it never follows an SDK
// attempt or performs a fallback. Lifecycle ownership stays with the caller.
func (e *Executor) receiveJSON(ctx context.Context, payload []byte) ([]byte, error) {
	parent := ctx
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, e.streamURL, bytes.NewReader(payload))
	if err != nil {
		return nil, execution.ErrInvalidRequest
	}
	request.Header.Set("Authorization", "Bearer "+e.streamKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", "identity")
	timer := time.AfterFunc(10*time.Second, func() { cancel(context.DeadlineExceeded) })
	response, err := e.streamHTTP.Do(request)
	timer.Stop()
	if err != nil {
		return nil, streamError(parent, ctx, execution.ErrUpstream)
	}
	defer response.Body.Close()
	media, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	encoding := response.Header.Get("Content-Encoding")
	if response.StatusCode != http.StatusOK || mediaErr != nil || media != "application/json" || response.ContentLength > maxJSONResponseBytes || (encoding != "" && encoding != "identity") {
		return nil, execution.ErrUpstream
	}
	raw, err := io.ReadAll(io.LimitReader(&idleReader{Reader: response.Body, cancel: cancel}, maxJSONResponseBytes+1))
	if err != nil || len(raw) > maxJSONResponseBytes {
		return nil, streamError(parent, ctx, execution.ErrUpstream)
	}
	if !utf8.Valid(raw) {
		return nil, execution.ErrUpstream
	}
	return raw, nil
}

func (e *Executor) chatJSON(ctx context.Context, req execution.Request, maxOutput int, emit execution.Emit) error {
	payload, err := e.profile.wireBody(req.Body, false)
	if err != nil {
		return execution.ErrInvalidRequest
	}
	raw, err := e.receiveJSON(ctx, payload)
	if err != nil {
		return err
	}
	// Reuse the pinned SDK's response schema, in-band error detection and
	// normalization; never promote upstream-supplied extra_fields to metadata.
	if openai.ErrorInSuccessfulChatBody(raw) != nil {
		return execution.ErrUpstream
	}
	if !unambiguousJSONChoice(raw) {
		return execution.ErrUpstream
	}
	var resp schemas.BifrostChatResponse
	if json.Unmarshal(raw, &resp) != nil {
		return execution.ErrUpstream
	}
	resp.ExtraFields = schemas.BifrostResponseExtraFields{RawResponse: json.RawMessage(raw)}
	resp.Usage.NormalizeReasoningTokens()
	resp.BackfillParams(&schemas.BifrostChatRequest{Model: e.model})
	return deliver(ctx, req, &resp, maxOutput, &usageEvidence{}, emit)
}

// Check the completion boundary before a case-insensitive SDK decoder can
// silently choose one repeated finish/message field. Top-level and usage
// ambiguity is independently rejected by usageEvidence.
func unambiguousJSONChoice(raw []byte) bool {
	var frame map[string]json.RawMessage
	if json.Unmarshal(raw, &frame) != nil {
		return false
	}
	for key := range frame {
		if (strings.EqualFold(key, "choices") && key != "choices") || (strings.EqualFold(key, "usage") && key != "usage") {
			return false
		}
	}
	var choices []json.RawMessage
	if json.Unmarshal(frame["choices"], &choices) != nil || len(choices) != 1 {
		return false
	}
	choice, ok := object(choices[0], "index", "message", "finish_reason", "logprobs")
	if !ok {
		return false
	}
	var index *int
	if json.Unmarshal(choice["index"], &index) != nil || index == nil || *index != 0 {
		return false
	}
	_, ok = object(choice["message"], "role", "content", "tool_calls", "reasoning_content", "refusal")
	return ok
}
