package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

// ResponsesRequest is the reviewed stateless text subset. Unknown fields are
// rejected by the public decoder, including tools, previous_response_id and files.
type ResponsesRequest struct {
	Model           string          `json:"model"`
	Input           json.RawMessage `json:"input"`
	Instructions    string          `json:"instructions,omitempty"`
	MaxOutputTokens int             `json:"max_output_tokens"`
	Store           *bool           `json:"store"`
	Stream          bool            `json:"stream,omitempty"`
	Background      bool            `json:"background,omitempty"`
}

func (r ResponsesRequest) Request() (Request, error) {
	if r.Store == nil || *r.Store || r.Background || r.MaxOutputTokens < 16 || len(r.Instructions) > 16<<10 {
		return Request{}, errors.New("unsupported responses request")
	}
	req := Request{Model: r.Model, MaxTokens: r.MaxOutputTokens, Stream: r.Stream}
	if r.Instructions != "" {
		req.Messages = append(req.Messages, Message{Role: "system", Content: r.Instructions})
	}
	input, err := responsesTextInput(r.Input)
	if err != nil {
		return Request{}, err
	}
	req.Messages = append(req.Messages, input...)
	if !ValidateRequest(req) {
		return Request{}, errors.New("invalid responses request")
	}
	return req, nil
}

// The union is decoded separately so unknown nested fields cannot bypass the
// public decoder. Role order is preserved; developer/tool/state semantics are
// deliberately not approximated through Chat's system messages.
func responsesTextInput(raw json.RawMessage) ([]Message, error) {
	invalid := errors.New("unsupported responses text input")
	var text string
	if json.Unmarshal(raw, &text) == nil && len(text) > 0 && len(text) <= 16<<10 {
		return []Message{{Role: "user", Content: text}}, nil
	}
	var items []struct {
		Type    string          `json:"type,omitempty"`
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	decode := func(raw []byte, value any) bool {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		return d.Decode(value) == nil && d.Decode(&struct{}{}) == io.EOF
	}
	if len(raw) > 64<<10 || !decode(raw, &items) || len(items) == 0 || len(items) > 63 || items[len(items)-1].Role != "user" {
		return nil, invalid
	}
	result := make([]Message, 0, len(items))
	for _, item := range items {
		if (item.Type != "" && item.Type != "message") || (item.Role != "user" && item.Role != "assistant" && item.Role != "system") {
			return nil, invalid
		}
		var content string
		if json.Unmarshal(item.Content, &content) != nil {
			var parts []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if item.Role == "assistant" || !decode(item.Content, &parts) || len(parts) == 0 || len(parts) > 16 {
				return nil, invalid
			}
			for _, part := range parts {
				if part.Type != "input_text" || len(part.Text) == 0 || len(part.Text) > (16<<10)-len(content) {
					return nil, invalid
				}
				content += part.Text
			}
		}
		if len(content) == 0 || len(content) > 16<<10 {
			return nil, invalid
		}
		result = append(result, Message{Role: item.Role, Content: content})
	}
	return result, nil
}

type ResponsesText struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Annotations []any  `json:"annotations"`
}
type ResponsesOutput struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Status  string          `json:"status"`
	Role    string          `json:"role"`
	Content []ResponsesText `json:"content"`
}
type ResponsesInputDetails struct {
	CachedTokens     *int64 `json:"cached_tokens"`
	CacheWriteTokens *int64 `json:"cache_write_tokens,omitempty"`
}

type ResponsesUsage struct {
	InputTokens        int64                  `json:"input_tokens"`
	InputTokensDetails *ResponsesInputDetails `json:"input_tokens_details,omitempty"`
	OutputTokens       int64                  `json:"output_tokens"`
	TotalTokens        int64                  `json:"total_tokens"`
}
type ResponsesResult struct {
	ID                string            `json:"id"`
	Object            string            `json:"object"`
	CreatedAt         int64             `json:"created_at"`
	Status            string            `json:"status"`
	Error             any               `json:"error"`
	IncompleteDetails map[string]string `json:"incomplete_details"`
	Model             string            `json:"model"`
	Store             bool              `json:"store"`
	Background        bool              `json:"background"`
	MaxOutputTokens   int               `json:"max_output_tokens"`
	Output            []ResponsesOutput `json:"output"`
	Usage             ResponsesUsage    `json:"usage"`
}

// PublicResponses formats already normalized and validated Chat execution. It
// never estimates missing usage details or implies a retrievable stored response.
func PublicResponses(value Response, req Request) (ResponsesResult, error) {
	if len(value.Choices) != 1 || value.Choices[0].Message == nil || value.Choices[0].Message.Content == nil || value.Choices[0].FinishReason == nil || value.Usage == nil || value.Usage.PromptTokens == nil || value.Usage.CompletionTokens == nil || value.Usage.TotalTokens == nil {
		return ResponsesResult{}, errors.New("incomplete responses evidence")
	}
	reason := *value.Choices[0].FinishReason
	status := "completed"
	var details map[string]string
	switch reason {
	case "stop":
	case "length":
		status = "incomplete"
		details = map[string]string{"reason": "max_output_tokens"}
	case "content_filter":
		status = "incomplete"
		details = map[string]string{"reason": "content_filter"}
	default:
		return ResponsesResult{}, errors.New("unsupported responses termination")
	}
	id := strings.TrimPrefix(value.ID, "chatcmpl-")
	var inputDetails *ResponsesInputDetails
	if d := value.Usage.PromptTokensDetails; d != nil && d.CachedTokens != nil {
		inputDetails = &ResponsesInputDetails{CachedTokens: d.CachedTokens, CacheWriteTokens: d.CachedCreationTokens}
	}
	return ResponsesResult{ID: "resp_" + id, Object: "response", CreatedAt: time.Now().Unix(), Status: status, IncompleteDetails: details, Model: value.Model, MaxOutputTokens: req.MaxTokens,
		Output: []ResponsesOutput{{ID: "msg_" + id, Type: "message", Status: status, Role: "assistant", Content: []ResponsesText{{Type: "output_text", Text: *value.Choices[0].Message.Content, Annotations: []any{}}}}},
		Usage:  ResponsesUsage{InputTokens: *value.Usage.PromptTokens, InputTokensDetails: inputDetails, OutputTokens: *value.Usage.CompletionTokens, TotalTokens: *value.Usage.TotalTokens}}, nil
}
