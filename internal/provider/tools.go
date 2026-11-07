package provider

import (
	"encoding/json"
	"errors"
	"regexp"
)

var functionName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var callID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// Tools are client-side functions. The gateway never runs a tool or accepts
// provider-hosted tools with additional, unbounded billing dimensions.
type Tool struct {
	Type     string             `json:"type"`
	Function FunctionDefinition `json:"function"`
}
type FunctionDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}
type FunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}
type ToolCall struct {
	Index    *int         `json:"index,omitempty"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function FunctionCall `json:"function"`
}

func objectJSON(raw []byte) bool {
	var obj map[string]json.RawMessage
	return len(raw) <= 16<<10 && json.Unmarshal(raw, &obj) == nil && obj != nil
}
func (r Request) HasTools() bool {
	return r.Tools != nil || r.ToolChoice != "" || hasToolHistory(r.Messages)
}
func hasToolHistory(messages []Message) bool {
	for _, m := range messages {
		if m.ToolCalls != nil || m.ToolCallID != "" || m.Role == "tool" {
			return true
		}
	}
	return false
}
func validateTools(r Request) bool {
	if len(r.Tools) > 8 || (r.ToolChoice != "" && r.ToolChoice != "auto" && r.ToolChoice != "none" && r.ToolChoice != "required") {
		return false
	}
	names := map[string]bool{}
	for _, tool := range r.Tools {
		if tool.Type != "function" || !functionName.MatchString(tool.Function.Name) || names[tool.Function.Name] || len(tool.Function.Description) > 4096 || !objectJSON(tool.Function.Parameters) {
			return false
		}
		names[tool.Function.Name] = true
	}
	if len(names) == 0 && r.HasTools() {
		return false
	}
	pending := map[string]bool{}
	used := map[string]bool{}
	for _, m := range r.Messages {
		if m.Role == "tool" {
			if !pending[m.ToolCallID] || len(m.ToolCalls) > 0 || len(m.Content) == 0 {
				return false
			}
			delete(pending, m.ToolCallID)
			continue
		}
		if len(pending) > 0 || m.ToolCallID != "" {
			return false
		}
		if len(m.ToolCalls) > 0 {
			if m.Role != "assistant" || len(m.ToolCalls) > 8 {
				return false
			}
			for _, call := range m.ToolCalls {
				if call.Index != nil || !validCall(call, names) || used[call.ID] {
					return false
				}
				pending[call.ID] = true
				used[call.ID] = true
			}
		}
	}
	return len(pending) == 0
}
func validCall(c ToolCall, names map[string]bool) bool {
	return callID.MatchString(c.ID) && c.Type == "function" && names[c.Function.Name] && objectJSON([]byte(c.Function.Arguments))
}

// ToolResponseState buffers argument deltas until a complete, bounded call is
// validated. No partial function call is handed to a client for execution.
type ToolResponseState struct {
	request Request
	calls   []ToolCall
}

func NewToolResponseState(req Request) *ToolResponseState { return &ToolResponseState{request: req} }
func (s *ToolResponseState) Apply(v *Response, stream bool) error {
	invalid := errors.New("invalid or unauthorized tool response")
	names := map[string]bool{}
	if s.request.ToolChoice != "none" {
		for _, t := range s.request.Tools {
			names[t.Function.Name] = true
		}
	}
	for i := range v.Choices {
		c := &v.Choices[i]
		d := c.Message
		if stream {
			d = c.Delta
		}
		if d != nil {
			if !stream {
				s.calls = d.ToolCalls
			} else {
				for _, part := range d.ToolCalls {
					if part.Index == nil || *part.Index < 0 || *part.Index > 7 || *part.Index > len(s.calls) {
						return invalid
					}
					n := *part.Index
					if n == len(s.calls) {
						if !callID.MatchString(part.ID) || part.Type != "function" || !names[part.Function.Name] {
							return invalid
						}
						s.calls = append(s.calls, part)
					} else {
						if (part.ID != "" && part.ID != s.calls[n].ID) || (part.Type != "" && part.Type != "function") || (part.Function.Name != "" && part.Function.Name != s.calls[n].Function.Name) {
							return invalid
						}
						s.calls[n].Function.Arguments += part.Function.Arguments
					}
				}
				d.ToolCalls = nil
			}
		}
		size := 0
		for _, call := range s.calls {
			size += len(call.Function.Arguments)
			if len(call.Function.Arguments) > 16<<10 {
				return invalid
			}
		}
		if size > 32<<10 || len(s.calls) > 8 {
			return invalid
		}
		if c.FinishReason != nil {
			hasCalls := len(s.calls) > 0
			if (*c.FinishReason == "tool_calls") != hasCalls || (!hasCalls && s.request.ToolChoice == "required") {
				return invalid
			}
			seen := map[string]bool{}
			for _, call := range s.calls {
				if !validCall(call, names) || seen[call.ID] || (!stream && call.Index != nil) {
					return invalid
				}
				seen[call.ID] = true
			}
			if stream && hasCalls {
				if d == nil {
					d = &Delta{}
					c.Delta = d
				}
				d.ToolCalls = s.calls
			}
		}
	}
	return nil
}

// AllowsTools checks the reviewed model policy, not client declarations alone.
// It approves names only; client business authorization and execution remain external.
func (r *Route) AllowsTools(req Request) bool {
	if !req.HasTools() {
		return true
	}
	if r == nil || len(r.model.AllowedTools) == 0 {
		return false
	}
	allowed := make(map[string]bool, len(r.model.AllowedTools))
	for _, name := range r.model.AllowedTools {
		allowed[name] = true
	}
	for _, tool := range req.Tools {
		if !allowed[tool.Function.Name] {
			return false
		}
	}
	for _, message := range req.Messages {
		for _, call := range message.ToolCalls {
			if !allowed[call.Function.Name] {
				return false
			}
		}
	}
	return true
}
