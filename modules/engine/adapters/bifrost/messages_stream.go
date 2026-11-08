package bifrost

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/jhzhfree/ai-governance-gateway/internal/completion"
	"github.com/jhzhfree/ai-governance-gateway/modules/engine/execution"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/schemas"
)

// receiveMessages checks the reviewed native sequence before public SDK
// conversion. Only message_stop promotes final cumulative usage to completion.
func (e *Executor) receiveMessages(ctx context.Context, sdkContext *schemas.BifrostContext, body io.Reader, req execution.Request, maxOutput int, emit execution.Emit) error {
	state := anthropic.NewAnthropicStreamState()
	started, blockOpen := false, false
	blocks := 0
	id, stop := "", ""
	var initial, final completion.Counters
	var finalRaw json.RawMessage
	indexMatches := func(raw json.RawMessage, expected int) bool {
		var index *int
		return json.Unmarshal(raw, &index) == nil && index != nil && *index == expected
	}
	emitConverted := func(raw, early json.RawMessage) error {
		var native anthropic.AnthropicStreamEvent
		if json.Unmarshal(raw, &native) != nil {
			return execution.ErrUpstream
		}
		normalized, err, done := native.ToBifrostChatCompletionStream(sdkContext, "", state)
		if err != nil || done {
			return execution.ErrUpstream
		}
		if normalized == nil {
			if len(early) != 0 {
				return execution.ErrUpstream
			}
			return nil
		}
		normalized.ExtraFields = schemas.BifrostResponseExtraFields{}
		normalized.Usage = nil
		normalized.ID = id
		normalized.BackfillParams(&schemas.BifrostChatRequest{Model: e.model})
		payload, errJSON := json.Marshal(normalized)
		if errJSON != nil {
			return execution.ErrUpstream
		}
		return emit(ctx, execution.Event{UpstreamStatus: 200, RequestID: req.RequestID, AttemptID: req.AttemptID, Payload: payload, InitialUsage: append(json.RawMessage(nil), early...)})
	}
	return readSSEFrames(ctx, body, func(name string, raw json.RawMessage) (bool, error) {
		var envelope map[string]json.RawMessage
		if json.Unmarshal(raw, &envelope) != nil {
			return false, execution.ErrUpstream
		}
		kind, ok := text(envelope["type"])
		if !ok || name != kind {
			return false, execution.ErrUpstream
		}
		if kind == "ping" {
			if _, valid := object(raw, "type"); !valid {
				return false, execution.ErrUpstream
			}
			return false, nil
		}
		switch kind {
		case "message_start":
			v, ok := object(raw, "type", "message")
			if !ok || started {
				return false, execution.ErrUpstream
			}
			message, ok := object(v["message"], "id", "type", "role", "model", "content", "stop_reason", "stop_sequence", "usage")
			if !ok {
				return false, execution.ErrUpstream
			}
			id, ok = text(message["id"])
			model, _ := text(message["model"])
			role, _ := text(message["role"])
			typ, _ := text(message["type"])
			var content []json.RawMessage
			if !ok || id == "" || len(id) > 256 || model != e.model || role != "assistant" || typ != "message" || json.Unmarshal(message["content"], &content) != nil || content == nil || len(content) != 0 || !bytes.Equal(bytes.TrimSpace(message["stop_reason"]), []byte("null")) || !bytes.Equal(bytes.TrimSpace(message["stop_sequence"]), []byte("null")) {
				return false, execution.ErrUpstream
			}
			var err error
			initial, err = completion.MessagesUsage(message["usage"])
			if err != nil || initial.Output > int64(maxOutput) {
				return false, execution.ErrUpstream
			}
			started = true
			return false, emitConverted(raw, message["usage"])
		case "content_block_start":
			v, ok := object(raw, "type", "index", "content_block")
			if !ok || !started || blockOpen || finalRaw != nil || blocks >= 64 || !indexMatches(v["index"], blocks) {
				return false, execution.ErrUpstream
			}
			block, ok := object(v["content_block"], "type", "text")
			typ, _ := text(block["type"])
			value, plain := text(block["text"])
			// The SDK emits text through deltas. A nonempty start must never be
			// silently dropped, so it is outside this reviewed wire subset.
			if !ok || typ != "text" || !plain || value != "" {
				return false, execution.ErrUpstream
			}
			blockOpen = true
			return false, emitConverted(raw, nil)
		case "content_block_delta":
			v, ok := object(raw, "type", "index", "delta")
			if !ok || !blockOpen || finalRaw != nil || !indexMatches(v["index"], blocks) {
				return false, execution.ErrUpstream
			}
			delta, ok := object(v["delta"], "type", "text")
			typ, _ := text(delta["type"])
			_, plain := text(delta["text"])
			if !ok || typ != "text_delta" || !plain {
				return false, execution.ErrUpstream
			}
			return false, emitConverted(raw, nil)
		case "content_block_stop":
			v, ok := object(raw, "type", "index")
			if !ok || !blockOpen || !indexMatches(v["index"], blocks) {
				return false, execution.ErrUpstream
			}
			blockOpen = false
			blocks++
			return false, emitConverted(raw, nil)
		case "message_delta":
			v, ok := object(raw, "type", "delta", "usage")
			if !ok || !started || blockOpen || blocks == 0 || finalRaw != nil {
				return false, execution.ErrUpstream
			}
			delta, ok := object(v["delta"], "stop_reason", "stop_sequence")
			stop, _ = text(delta["stop_reason"])
			if !ok || (stop != "end_turn" && stop != "max_tokens" && stop != "refusal") || !bytes.Equal(bytes.TrimSpace(delta["stop_sequence"]), []byte("null")) {
				return false, execution.ErrUpstream
			}
			var err error
			final, err = completion.MessagesUsage(v["usage"])
			if err != nil || final.Input != initial.Input || final.Cache.Read != initial.Cache.Read || final.Cache.Uncached != initial.Cache.Uncached || final.Output < initial.Output || final.Output > int64(maxOutput) {
				return false, execution.ErrUpstream
			}
			finalRaw = append(json.RawMessage(nil), v["usage"]...)
			return false, nil
		case "message_stop":
			if _, ok := object(raw, "type"); !ok || !started || blockOpen || finalRaw == nil {
				return false, execution.ErrUpstream
			}
			// Reuse the SDK's public finish-reason mapping; its stream converter
			// intentionally does not emit message_delta, so evidence stays here.
			reason := anthropic.ConvertAnthropicFinishReasonToBifrost(anthropic.AnthropicStopReason(stop))
			payload, err := json.Marshal(&schemas.BifrostChatResponse{ID: id, Object: "chat.completion.chunk", Model: e.model, Choices: []schemas.BifrostResponseChoice{{Index: 0, FinishReason: &reason, ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{Delta: &schemas.ChatStreamResponseChoiceDelta{}}}}})
			if err != nil {
				return false, execution.ErrUpstream
			}
			usage := &execution.Usage{InputTokens: int(final.Input), OutputTokens: int(final.Output), TotalTokens: int(final.Total), Evidence: finalRaw}
			return true, emit(ctx, execution.Event{Completed: true, UpstreamStatus: 200, RequestID: req.RequestID, AttemptID: req.AttemptID, Payload: payload, Usage: usage})
		default:
			return false, execution.ErrUpstream
		}
	})
}
