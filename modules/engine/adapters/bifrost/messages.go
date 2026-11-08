package bifrost

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/jhzhfree/ai-governance-gateway/internal/completion"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/modules/engine/execution"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
)

// Messages uses public SDK converters within the same guarded, bounded receiver.
// Conversion does not grant completion or manufacture original usage evidence.
func (e *Executor) messagesExecute(ctx context.Context, req execution.Request, emit execution.Emit) error {
	maxOutput, valid := validateRequest(req.Body, e.model, e.maxOutputTokens)
	v, ok := object(req.Body, "model", "messages", "max_tokens", "stream")
	if !valid || !ok {
		return execution.ErrInvalidRequest
	}
	var items []json.RawMessage
	if json.Unmarshal(v["messages"], &items) != nil {
		return execution.ErrInvalidRequest
	}
	for _, item := range items {
		if _, ok := object(item, "role", "content"); !ok {
			return execution.ErrInvalidRequest
		}
	}
	var governed provider.Request
	if json.Unmarshal(req.Body, &governed) != nil || provider.ValidateEmbeddedMessages(governed) != nil {
		return execution.ErrInvalidRequest
	}
	bctx, cancel := schemas.NewBifrostContextWithCancel(ctx)
	defer cancel()
	var source openai.OpenAIChatRequest
	if json.Unmarshal(req.Body, &source) != nil {
		return execution.ErrInvalidRequest
	}
	neutral := source.ToBifrostChatRequest(bctx)
	neutral.Provider, neutral.Model = schemas.Anthropic, e.model
	wire, err := anthropic.ToAnthropicChatRequest(bctx, neutral)
	if err != nil || wire == nil {
		return execution.ErrInvalidRequest
	}
	wire.Model, wire.MaxTokens = e.model, maxOutput
	wire.Stream = schemas.Ptr(governed.Stream)
	wire.Thinking = &anthropic.AnthropicThinking{Type: "disabled"}
	payload, err := json.Marshal(wire)
	if err != nil {
		return execution.ErrInvalidRequest
	}
	if _, ok := object(payload, "model", "messages", "max_tokens", "system", "stream", "thinking"); !ok {
		return execution.ErrInvalidRequest
	}
	// Prove SDK conversion preserved the reviewed conversation order and text.
	var roundtrip provider.MessagesRequest
	if json.Unmarshal(payload, &roundtrip) != nil {
		return execution.ErrInvalidRequest
	}
	canonical, err := roundtrip.Request()
	if err != nil || len(canonical.Messages) != len(governed.Messages) {
		return execution.ErrInvalidRequest
	}
	for i, m := range canonical.Messages {
		if m.Role != governed.Messages[i].Role || m.Content != governed.Messages[i].Content {
			return execution.ErrInvalidRequest
		}
	}
	ctx, release, err := e.lifecycle.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	if governed.Stream {
		return e.receiveSSE(ctx, payload, func(ctx context.Context, body io.Reader) error {
			return e.receiveMessages(ctx, bctx, body, req, maxOutput, emit)
		})
	}
	raw, err := e.receiveJSON(ctx, payload)
	if err != nil {
		return err
	}
	fields, ok := object(raw, "id", "type", "role", "model", "content", "stop_reason", "stop_sequence", "usage")
	if !ok {
		return execution.ErrUpstream
	}
	id, idOK := text(fields["id"])
	typ, _ := text(fields["type"])
	role, _ := text(fields["role"])
	model, _ := text(fields["model"])
	stop, _ := text(fields["stop_reason"])
	if !idOK || id == "" || len(id) > 256 || typ != "message" || role != "assistant" || model != e.model || (stop != "end_turn" && stop != "max_tokens" && stop != "refusal") {
		return execution.ErrUpstream
	}
	if value := fields["stop_sequence"]; value != nil && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return execution.ErrUpstream
	}
	var content []json.RawMessage
	if json.Unmarshal(fields["content"], &content) != nil || len(content) < 1 || len(content) > 64 {
		return execution.ErrUpstream
	}
	for _, block := range content {
		part, ok := object(block, "type", "text")
		kind, _ := text(part["type"])
		_, plain := text(part["text"])
		if !ok || kind != "text" || !plain {
			return execution.ErrUpstream
		}
	}
	counters, err := completion.MessagesUsage(fields["usage"])
	if err != nil || counters.Output > int64(maxOutput) {
		return execution.ErrUpstream
	}
	var native anthropic.AnthropicMessageResponse
	if json.Unmarshal(raw, &native) != nil {
		return execution.ErrUpstream
	}
	normalized := native.ToBifrostChatResponse(bctx)
	if normalized == nil || normalized.Usage == nil || int64(normalized.Usage.PromptTokens) != counters.Input || int64(normalized.Usage.CompletionTokens) != counters.Output || int64(normalized.Usage.TotalTokens) != counters.Total {
		return execution.ErrUpstream
	}
	normalized.ExtraFields = schemas.BifrostResponseExtraFields{}
	normalized.BackfillParams(neutral)
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return execution.ErrUpstream
	}
	usage := &execution.Usage{InputTokens: int(counters.Input), OutputTokens: int(counters.Output), TotalTokens: int(counters.Total), Evidence: append(json.RawMessage(nil), fields["usage"]...)}
	return emit(ctx, execution.Event{Completed: true, UpstreamStatus: 200, RequestID: req.RequestID, AttemptID: req.AttemptID, Payload: encoded, Usage: usage})
}
