package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

type Message struct {
	Parts      []ContentPart `json:"-"`
	Role       string        `json:"role"`
	Content    string        `json:"content"`
	ToolCalls  []ToolCall    `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}
type Request struct {
	MessagesStream bool      `json:"-"`
	API            string    `json:"-"`
	Input          []string  `json:"input,omitempty"`
	EncodingFormat string    `json:"encoding_format,omitempty"`
	Dimensions     *int      `json:"dimensions,omitempty"`
	Tools          []Tool    `json:"tools,omitempty"`
	ToolChoice     string    `json:"tool_choice,omitempty"`
	Model          string    `json:"model"`
	Stream         bool      `json:"stream"`
	MaxTokens      int       `json:"max_tokens"`
	Messages       []Message `json:"messages"`
}
type InputTokenDetails struct {
	CachedCreationTokensDetails *CacheWriteUsage `json:"cached_creation_tokens_details,omitempty"`
	CachedTokens                *int64           `json:"cached_tokens"`
	CachedCreationTokens        *int64           `json:"cached_creation_tokens,omitempty"`
}
type Usage struct {
	PromptTokensDetails *InputTokenDetails `json:"prompt_tokens_details,omitempty"`
	PromptTokens        *int64             `json:"prompt_tokens"`
	CompletionTokens    *int64             `json:"completion_tokens"`
	TotalTokens         *int64             `json:"total_tokens"`
}
type Delta struct {
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	Role             string     `json:"role,omitempty"`
	Content          *string    `json:"content,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
}
type Choice struct {
	Index        int     `json:"index"`
	Delta        *Delta  `json:"delta,omitempty"`
	Message      *Delta  `json:"message,omitempty"`
	FinishReason *string `json:"finish_reason"`
}
type Response struct {
	ID      string          `json:"id"`
	Object  string          `json:"object"`
	Created int64           `json:"created,omitempty"`
	Model   string          `json:"model"`
	Choices []Choice        `json:"choices"`
	Usage   *Usage          `json:"usage,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

func ValidateRequest(req Request) bool {
	if req.MessagesStream && (!req.Stream || req.HasTools() || req.HasParts() || req.IsEmbedding()) {
		return false
	}
	if req.IsEmbedding() {
		return validEmbeddingRequest(req)
	}
	if req.API != "" || req.Input != nil || req.EncodingFormat != "" || req.Dimensions != nil {
		return false
	}
	if len(req.Messages) < 1 || len(req.Messages) > 64 || !validateTools(req) {
		return false
	}
	for _, m := range req.Messages {
		if (m.Role != "user" && m.Role != "system" && m.Role != "assistant" && m.Role != "tool") || (len(m.Content) == 0 && len(m.ToolCalls) == 0 && len(m.Parts) == 0) || len(m.Content) > 16<<10 {
			return false
		}
	}
	if _, err := req.ImageMetadata(); err != nil {
		return false
	}
	return true
}
func (r *Route) Open(ctx context.Context, req Request) (*http.Response, error) {
	if r.UsesNativeMessages() || req.MessagesStream || req.IsEmbedding() || !r.Ready() || r.key == "" || req.HasTools() || req.HasParts() {
		return nil, errors.New("mainland residency review expired")
	}
	payload := map[string]any{"model": r.model.UpstreamModel, "max_tokens": req.MaxTokens, "stream": req.Stream, "messages": req.Messages}
	if req.Stream && r.profile.IncludeUsage {
		payload["stream_options"] = map[string]bool{"include_usage": true}
	}
	if r.profile.ID == "aliyun" {
		payload["enable_thinking"] = false
	} else {
		payload["thinking"] = map[string]string{"type": "disabled"}
	}
	body, _ := json.Marshal(payload)
	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, r.profile.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("cannot construct supplier request")
	}
	upstream.Header.Set("Authorization", "Bearer "+r.key)
	upstream.Header.Set("Content-Type", "application/json")
	upstream.Header.Set("Accept", "application/json")
	if req.Stream {
		upstream.Header.Set("Accept", "text/event-stream")
	}
	response, err := r.client.Do(upstream)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("supplier connection failed")
	}
	return response, nil
}

// Normalize preserves the text-only contract for existing executors.
func Normalize(data []byte, alias, id string, stream bool) (Response, error) {
	return normalize(data, alias, id, stream, false)
}

func NormalizeRequest(data []byte, req Request, id string) (Response, error) {
	return normalize(data, req.Model, id, req.Stream, len(req.Tools) > 0)
}
func normalize(data []byte, alias, id string, stream, tools bool) (Response, error) {
	var v Response
	if json.Unmarshal(data, &v) != nil || len(v.Error) > 0 || len(v.Choices) > 1 || (!stream && len(v.Choices) != 1) || (v.Choices == nil && v.Usage == nil) {
		return v, errors.New("invalid supplier response")
	}
	for i := range v.Choices {
		c := &v.Choices[i]
		if c.Index != 0 {
			return v, errors.New("unsupported supplier choice")
		}
		delta := c.Delta
		if stream && c.Message != nil {
			return v, errors.New("invalid stream choice")
		}
		if !stream && c.Delta != nil {
			return v, errors.New("invalid completion choice")
		}
		if !stream {
			delta = c.Message
			if c.FinishReason == nil || *c.FinishReason == "" || delta == nil || delta.Role != "assistant" || (delta.Content == nil && len(delta.ToolCalls) == 0) {
				return v, errors.New("incomplete supplier response")
			}
		}
		if delta != nil && ((!tools && len(delta.ToolCalls) > 0) || delta.ReasoningContent != "" || (delta.Role != "" && delta.Role != "assistant")) {
			return v, errors.New("unsupported reasoning response")
		}
		if c.FinishReason != nil && *c.FinishReason != "stop" && *c.FinishReason != "length" && *c.FinishReason != "content_filter" && !(tools && *c.FinishReason == "tool_calls") {
			return v, errors.New("unsupported supplier finish reason")
		}
	}
	// Use one gateway identity even when usage-only supplier chunks omit/change ID.
	v.ID = "chatcmpl-" + id
	v.Model = alias
	v.Error = nil
	v.Object = "chat.completion"
	if stream {
		v.Object = "chat.completion.chunk"
	}
	return v, nil
}
