package provider

import (
	"errors"
	"strings"
)

// MessagesRequest is the explicitly reviewed text-only, non-streaming subset.
// Separate DTOs prevent tools or media from silently entering the Chat bridge.
type MessagesRequest struct {
	Model     string          `json:"model"`
	Messages  []MessagesInput `json:"messages"`
	System    string          `json:"system,omitempty"`
	MaxTokens int             `json:"max_tokens"`
	Stream    bool            `json:"stream,omitempty"`
}
type MessagesInput struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (r MessagesRequest) Request() (Request, error) {
	if r.Stream || r.MaxTokens < 1 || len(r.Messages) == 0 || len(r.Messages) > 63 || len(r.System) > 16<<10 || r.Messages[len(r.Messages)-1].Role != "user" {
		return Request{}, errors.New("unsupported messages request")
	}
	req := Request{Model: r.Model, MaxTokens: r.MaxTokens}
	if r.System != "" {
		req.Messages = append(req.Messages, Message{Role: "system", Content: r.System})
	}
	for _, m := range r.Messages {
		if (m.Role != "user" && m.Role != "assistant") || len(m.Content) == 0 || len(m.Content) > 16<<10 {
			return Request{}, errors.New("unsupported messages content")
		}
		req.Messages = append(req.Messages, Message{Role: m.Role, Content: m.Content})
	}
	if !ValidateRequest(req) {
		return Request{}, errors.New("invalid messages request")
	}
	return req, nil
}

type MessagesText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
type MessagesUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}
type MessagesResult struct {
	ID           string         `json:"id"`
	Type         string         `json:"type"`
	Role         string         `json:"role"`
	Model        string         `json:"model"`
	Content      []MessagesText `json:"content"`
	StopReason   string         `json:"stop_reason"`
	StopSequence *string        `json:"stop_sequence"`
	Usage        MessagesUsage  `json:"usage"`
}

func PublicMessages(v Response) (MessagesResult, error) {
	if len(v.Choices) != 1 || v.Choices[0].Message == nil || v.Choices[0].Message.Content == nil || v.Choices[0].FinishReason == nil || v.Usage == nil || v.Usage.PromptTokens == nil || v.Usage.CompletionTokens == nil || v.Usage.PromptTokensDetails == nil || v.Usage.PromptTokensDetails.CachedTokens == nil || v.Usage.PromptTokensDetails.CachedCreationTokens == nil {
		return MessagesResult{}, errors.New("missing messages usage evidence")
	}
	input, output, read, write := *v.Usage.PromptTokens, *v.Usage.CompletionTokens, *v.Usage.PromptTokensDetails.CachedTokens, *v.Usage.PromptTokensDetails.CachedCreationTokens
	if input < 0 || output < 0 || read < 0 || write < 0 || read > input || write > input-read {
		return MessagesResult{}, errors.New("invalid messages usage evidence")
	}
	reason := ""
	switch *v.Choices[0].FinishReason {
	case "stop":
		reason = "end_turn"
	case "length":
		reason = "max_tokens"
	case "content_filter":
		reason = "refusal"
	default:
		return MessagesResult{}, errors.New("unsupported messages termination")
	}
	return MessagesResult{ID: "msg_" + strings.TrimPrefix(v.ID, "chatcmpl-"), Type: "message", Role: "assistant", Model: v.Model, Content: []MessagesText{{Type: "text", Text: *v.Choices[0].Message.Content}}, StopReason: reason, Usage: MessagesUsage{InputTokens: input - read - write, OutputTokens: output, CacheReadInputTokens: read, CacheCreationInputTokens: write}}, nil
}
