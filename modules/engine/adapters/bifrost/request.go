package bifrost

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
)

var functionName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// object rejects unknown and repeated keys before the SDK can discard or
// reinterpret them. Provider/model routing must have one unambiguous meaning.
func object(data []byte, allowed ...string) (map[string]json.RawMessage, bool) {
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, false
	}
	values := make(map[string]json.RawMessage)
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, false
		}
		permitted := false
		for _, field := range allowed {
			if key == field {
				permitted = true
				break
			}
		}
		if !permitted {
			return nil, false
		}
		if _, exists := values[key]; exists {
			return nil, false
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, false
		}
		values[key] = value
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		return nil, false
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, false
	}
	return values, true
}

func text(data json.RawMessage) (string, bool) {
	var value string
	if len(data) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null")) || json.Unmarshal(data, &value) != nil {
		return "", false
	}
	return value, true
}

// Deliberately limited to text Chat and function tools until other capabilities
// have their own protocol, accounting and content-audit acceptance tests.
func validateRequest(data []byte, model string, ceiling int) (int, bool) {
	v, ok := object(data, "model", "messages", "max_tokens", "stream", "tools", "tool_choice", "n")
	if !ok {
		return 0, false
	}
	m, ok := text(v["model"])
	if !ok || m != model {
		return 0, false
	}
	var limit int
	// Bifrost raises smaller limits to 16. Reject instead of silently increasing
	// the authorized request's cost bound.
	if json.Unmarshal(v["max_tokens"], &limit) != nil || limit < 16 || limit > ceiling {
		return 0, false
	}
	if raw, exists := v["n"]; exists {
		var n int
		if json.Unmarshal(raw, &n) != nil || n != 1 {
			return 0, false
		}
	}
	if raw, exists := v["stream"]; exists {
		var stream *bool
		if json.Unmarshal(raw, &stream) != nil || stream == nil {
			return 0, false
		}
	}
	var messages []json.RawMessage
	if json.Unmarshal(v["messages"], &messages) != nil || len(messages) == 0 || len(messages) > 64 {
		return 0, false
	}
	for _, raw := range messages {
		message, ok := object(raw, "role", "content", "name", "tool_calls", "tool_call_id")
		if !ok {
			return 0, false
		}
		role, _ := text(message["role"])
		if role != "system" && role != "user" && role != "assistant" && role != "tool" {
			return 0, false
		}
		content, hasText := text(message["content"])
		if !hasText {
			// Assistant tool calls may have omitted/null content, never URL or
			// multimodal content that could trigger an unreviewed SDK fetch.
			if raw, exists := message["content"]; exists && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return 0, false
			}
			var calls []json.RawMessage
			if role != "assistant" || json.Unmarshal(message["tool_calls"], &calls) != nil || len(calls) == 0 {
				return 0, false
			}
		}
		if len(content) > 16<<10 {
			return 0, false
		}
		if raw, exists := message["name"]; exists {
			name, ok := text(raw)
			if !ok || !functionName.MatchString(name) {
				return 0, false
			}
		}
		if _, exists := message["tool_calls"]; exists && role != "assistant" {
			return 0, false
		}
		if raw, exists := message["tool_calls"]; exists && !validToolCalls(raw) {
			return 0, false
		}
		if role == "tool" {
			id, ok := text(message["tool_call_id"])
			if !ok || id == "" {
				return 0, false
			}
		} else if _, exists := message["tool_call_id"]; exists {
			return 0, false
		}
	}
	names := make(map[string]bool)
	if raw, exists := v["tools"]; exists {
		var definitions []json.RawMessage
		if json.Unmarshal(raw, &definitions) != nil || len(definitions) == 0 || len(definitions) > 32 {
			return 0, false
		}
		for _, raw := range definitions {
			tool, ok := object(raw, "type", "function")
			kind, _ := text(tool["type"])
			if !ok || kind != "function" {
				return 0, false
			}
			function, ok := object(tool["function"], "name", "description", "parameters", "strict")
			name, _ := text(function["name"])
			if !ok || !functionName.MatchString(name) || names[name] {
				return 0, false
			}
			names[name] = true
			if raw, exists := function["parameters"]; exists {
				var schema map[string]json.RawMessage
				if json.Unmarshal(raw, &schema) != nil || schema == nil {
					return 0, false
				}
			}
			if raw, exists := function["description"]; exists {
				if _, ok := text(raw); !ok {
					return 0, false
				}
			}
			if raw, exists := function["strict"]; exists {
				var strict *bool
				if json.Unmarshal(raw, &strict) != nil || strict == nil {
					return 0, false
				}
			}
		}
	}
	if raw, exists := v["tool_choice"]; exists {
		if choice, ok := text(raw); ok {
			if choice != "none" && ((choice != "auto" && choice != "required") || len(names) == 0) {
				return 0, false
			}
		} else {
			choice, ok := object(raw, "type", "function")
			kind, _ := text(choice["type"])
			function, valid := object(choice["function"], "name")
			name, _ := text(function["name"])
			if !ok || !valid || kind != "function" || !names[name] {
				return 0, false
			}
		}
	}
	return limit, true
}

func validToolCalls(raw json.RawMessage) bool {
	var calls []json.RawMessage
	if json.Unmarshal(raw, &calls) != nil || len(calls) == 0 || len(calls) > 32 {
		return false
	}
	ids := make(map[string]bool)
	for _, raw := range calls {
		call, ok := object(raw, "id", "type", "function")
		id, _ := text(call["id"])
		kind, _ := text(call["type"])
		function, valid := object(call["function"], "name", "arguments")
		name, _ := text(function["name"])
		args, isText := text(function["arguments"])
		if !ok || !valid || id == "" || len(id) > 256 || ids[id] || kind != "function" || !functionName.MatchString(name) || !isText || len(args) > 64<<10 {
			return false
		}
		ids[id] = true
	}
	return true
}
