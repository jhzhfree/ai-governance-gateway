package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// MessagesRequest is the explicitly reviewed text-only subset.
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
	if r.MaxTokens < 1 || len(r.Messages) == 0 || len(r.Messages) > 63 || len(r.System) > 16<<10 || r.Messages[len(r.Messages)-1].Role != "user" {
		return Request{}, errors.New("unsupported messages request")
	}
	req := Request{Model: r.Model, MaxTokens: r.MaxTokens, Stream: r.Stream, MessagesStream: r.Stream}
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

// DecodeMessagesInitialUsage accepts explicit original counters only. They are
// provisional for display and must agree with final evidence before settlement.
func DecodeMessagesInitialUsage(raw string, maxInput, maxOutput int64) (*MessagesUsage, error) {
	if maxInput < 1 || maxInput > 1<<31-1 || maxOutput < 1 || maxOutput > 1<<31-1 || len(raw) == 0 || len(raw) > 512 {
		return nil, errors.New("missing original initial usage")
	}
	var v struct {
		Input  *int64 `json:"input_tokens"`
		Output *int64 `json:"output_tokens"`
		Read   *int64 `json:"cache_read_input_tokens"`
		Write  *int64 `json:"cache_creation_input_tokens"`
	}
	d := json.NewDecoder(bytes.NewBufferString(raw))
	d.DisallowUnknownFields()
	if d.Decode(&v) != nil || d.Decode(&struct{}{}) != io.EOF || v.Input == nil || v.Output == nil || v.Read == nil || v.Write == nil {
		return nil, errors.New("incomplete initial usage")
	}
	if *v.Input < 0 || *v.Input > maxInput || *v.Read < 0 || *v.Read > maxInput || *v.Write < 0 || *v.Write > maxInput || *v.Output < 0 || *v.Output > maxOutput {
		return nil, errors.New("invalid initial usage")
	}
	total := *v.Input + *v.Read + *v.Write
	if total < 1 || total > maxInput {
		return nil, errors.New("initial input exceeds reviewed bound")
	}
	return &MessagesUsage{InputTokens: *v.Input, OutputTokens: *v.Output, CacheReadInputTokens: *v.Read, CacheCreationInputTokens: *v.Write}, nil
}
func (u *MessagesUsage) MatchesFinal(v *Usage) bool {
	return u != nil && v != nil && v.PromptTokens != nil && v.CompletionTokens != nil && v.PromptTokensDetails != nil && v.PromptTokensDetails.CachedTokens != nil && v.PromptTokensDetails.CachedCreationTokens != nil && *v.PromptTokens == u.InputTokens+u.CacheReadInputTokens+u.CacheCreationInputTokens && *v.CompletionTokens >= u.OutputTokens && *v.PromptTokensDetails.CachedTokens == u.CacheReadInputTokens && *v.PromptTokensDetails.CachedCreationTokens == u.CacheCreationInputTokens
}
