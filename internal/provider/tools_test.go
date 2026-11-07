package provider_test

import (
	"encoding/json"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
	"strings"
	"testing"
)

func toolRequest() provider.Request {
	return provider.Request{Model: "model", Messages: []provider.Message{{Role: "user", Content: "synthetic"}}, Tools: []provider.Tool{{Type: "function", Function: provider.FunctionDefinition{Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)}}}}
}
func TestToolTranscriptRequiresDeclaredUniqueCallsAndResults(t *testing.T) {
	for _, mode := range []string{"valid", "orphan", "unanswered", "duplicate", "undeclared", "hosted", "empty-tools", "choice", "schema", "unknown-field"} {
		t.Run(mode, func(t *testing.T) {
			req := toolRequest()
			req.Messages = append(req.Messages, provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "call_1", Type: "function", Function: provider.FunctionCall{Name: "lookup", Arguments: `{"q":"synthetic"}`}}}}, provider.Message{Role: "tool", ToolCallID: "call_1", Content: "result"})
			switch mode {
			case "orphan":
				req.Messages[2].ToolCallID = "call_other"
			case "unanswered":
				req.Messages = req.Messages[:2]
			case "duplicate":
				req.Messages = append(req.Messages, req.Messages[2])
			case "undeclared":
				req.Messages[1].ToolCalls[0].Function.Name = "exec"
			case "empty-tools":
				req.Tools = []provider.Tool{}
			case "hosted":
				req.Tools[0].Type = "web_search"
			case "choice":
				req.ToolChoice = "provider_specific"
			case "schema":
				req.Tools[0].Function.Parameters = json.RawMessage(`null`)
			case "unknown-field":
				decoder := json.NewDecoder(strings.NewReader(`{"model":"model","messages":[{"role":"user","content":"x","audio":{}}]}`))
				decoder.DisallowUnknownFields()
				if decoder.Decode(&req) == nil {
					t.Fatal("unknown message extension accepted")
				}
				return
			}
			if provider.ValidateRequest(req) != (mode == "valid") {
				t.Fatal("invalid tool transcript accepted or valid round trip rejected")
			}
		})
	}
}
func TestToolStreamBuffersAndValidatesBeforeRelease(t *testing.T) {
	for _, mode := range []string{"valid", "undeclared", "repeated-id", "broken-json", "duplicate-id", "oversize", "wrong-finish", "none", "missing-index"} {
		t.Run(mode, func(t *testing.T) {
			req := toolRequest()
			req.Stream = true
			if mode == "none" {
				req.ToolChoice = "none"
			}
			state := provider.NewToolResponseState(req)
			index := 0
			first := provider.ToolCall{Index: &index, ID: "call_1", Type: "function", Function: provider.FunctionCall{Name: "lookup", Arguments: `{"q":`}}
			if mode == "undeclared" {
				first.Function.Name = "exec"
			}
			if mode == "missing-index" {
				first.Index = nil
			}
			v := provider.Response{Choices: []provider.Choice{{Delta: &provider.Delta{ToolCalls: []provider.ToolCall{first}}}}}
			err := state.Apply(&v, true)
			if mode == "undeclared" || mode == "none" || mode == "missing-index" {
				if err == nil {
					t.Fatal("unapproved partial call released")
				}
				return
			}
			if err != nil || len(v.Choices[0].Delta.ToolCalls) != 0 {
				t.Fatal("partial call released")
			}
			part := provider.ToolCall{Index: &index, Function: provider.FunctionCall{Arguments: `"synthetic"}`}}
			if mode == "repeated-id" {
				part.ID = "call_2"
			}
			if mode == "broken-json" {
				part.Function.Arguments = `unquoted}`
			}
			if mode == "oversize" {
				part.Function.Arguments = strings.Repeat("x", 32769)
			}
			finish := "tool_calls"
			if mode == "wrong-finish" {
				finish = "stop"
			}
			parts := []provider.ToolCall{part}
			if mode == "duplicate-id" {
				next := 1
				duplicate := first
				duplicate.Index = &next
				duplicate.Function.Arguments = `{}`
				parts = append(parts, duplicate)
			}
			v = provider.Response{Choices: []provider.Choice{{Delta: &provider.Delta{ToolCalls: parts}, FinishReason: &finish}}}
			err = state.Apply(&v, true)
			if mode != "valid" {
				if err == nil {
					t.Fatal("invalid complete tool call accepted")
				}
				return
			}
			if err != nil || len(v.Choices[0].Delta.ToolCalls) != 1 || v.Choices[0].Delta.ToolCalls[0].Function.Arguments != `{"q":"synthetic"}` {
				t.Fatal("valid tool arguments not preserved")
			}
		})
	}
}
func TestUnsolicitedToolCallsAreNotSilentlyDiscarded(t *testing.T) {
	raw := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"text","tool_calls":[{"id":"call_1","type":"function","function":{"name":"exec","arguments":"{}"}}]},"finish_reason":"stop"}]}`)
	if _, err := provider.Normalize(raw, "model", "request", false); err == nil {
		t.Fatal("text normalization accepted unsolicited tool payload")
	}
	req := toolRequest()
	v, err := provider.NormalizeRequest(raw, req, "request")
	if err != nil {
		t.Fatal(err)
	}
	if provider.NewToolResponseState(req).Apply(&v, false) == nil {
		t.Fatal("undeclared/wrong-finish tool accepted")
	}
}

func TestReviewedToolPolicyIsImmutableAndVersioned(t *testing.T) {
	cfg := testutil.MainlandConfig()
	for i := range cfg.Models {
		cfg.Models[i].NewAPIChannelID = 1
	}
	empty, err := provider.NewGoverned(cfg)
	if err != nil {
		t.Fatal(err)
	}
	denied, _ := empty.Find(cfg.Models[0].ID)
	if denied.AllowsTools(toolRequest()) {
		t.Fatal("unreviewed tools accepted")
	}
	cfg.Models[0].AllowedTools = []string{"lookup", "describe"}
	approved, err := provider.NewGoverned(cfg)
	if err != nil {
		t.Fatal(err)
	}
	route, _ := approved.Find(cfg.Models[0].ID)
	if !route.AllowsTools(toolRequest()) || route.PriceVersion() == denied.PriceVersion() {
		t.Fatal("tool policy missing from route identity")
	}
	cfg.Models[0].AllowedTools[0] = "execute"
	if !route.AllowsTools(toolRequest()) {
		t.Fatal("caller mutated published tool policy")
	}
	cfg.Models[0].AllowedTools = []string{"describe", "lookup"}
	reordered, err := provider.NewGoverned(cfg)
	if err != nil {
		t.Fatal(err)
	}
	same, _ := reordered.Find(cfg.Models[0].ID)
	if same.PriceVersion() != route.PriceVersion() {
		t.Fatal("policy order changed identity")
	}
	for _, invalid := range [][]string{{"lookup", "lookup"}, {"*"}, {""}, {"has space"}} {
		cfg.Models[0].AllowedTools = invalid
		if _, err := provider.NewGoverned(cfg); err == nil {
			t.Fatal("invalid tool policy accepted")
		}
	}
	req := toolRequest()
	req.Tools[0].Function.Name = "execute"
	if route.AllowsTools(req) {
		t.Fatal("client declaration bypassed enterprise approval")
	}
}
