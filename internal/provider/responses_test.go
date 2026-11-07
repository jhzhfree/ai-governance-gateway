package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResponsesStatelessTextAndEvidence(t *testing.T) {
	no := false
	request := ResponsesRequest{Model: "reviewed", Input: json.RawMessage(`"synthetic input"`), Instructions: "synthetic instructions", MaxOutputTokens: 16, Store: &no}
	normalized, err := request.Request()
	if err != nil || len(normalized.Messages) != 2 || normalized.Messages[0].Role != "system" || normalized.Stream || normalized.MaxTokens != 16 {
		t.Fatal("incorrect normalized request")
	}
	for name, mutate := range map[string]func(*ResponsesRequest){
		"missing store": func(r *ResponsesRequest) { r.Store = nil },
		"stored":        func(r *ResponsesRequest) { yes := true; r.Store = &yes },
		"background":    func(r *ResponsesRequest) { r.Background = true },
		"empty":         func(r *ResponsesRequest) { r.Input = json.RawMessage(`""`) },
		"large":         func(r *ResponsesRequest) { r.Input, _ = json.Marshal(strings.Repeat("x", (16<<10)+1)) },
		"small output":  func(r *ResponsesRequest) { r.MaxOutputTokens = 15 },
	} {
		t.Run(name, func(t *testing.T) {
			copy := request
			mutate(&copy)
			if _, err := copy.Request(); err == nil {
				t.Fatal("unsupported request accepted")
			}
		})
	}
	input, output, total, read, write := int64(11), int64(3), int64(14), int64(4), int64(5)
	text := "synthetic output"
	for _, reason := range []string{"stop", "length", "content_filter"} {
		value := Response{ID: "chatcmpl-synthetic", Model: "reviewed", Choices: []Choice{{Message: &Delta{Role: "assistant", Content: &text}, FinishReason: &reason}}, Usage: &Usage{PromptTokens: &input, CompletionTokens: &output, TotalTokens: &total, PromptTokensDetails: &InputTokenDetails{CachedTokens: &read, CachedCreationTokens: &write}}}
		result, err := PublicResponses(value, normalized)
		if err != nil || result.ID != "resp_synthetic" || result.Store || result.Usage.InputTokens != 11 || *result.Usage.InputTokensDetails.CacheWriteTokens != 5 {
			t.Fatal("responses evidence lost")
		}
		if (reason == "stop") != (result.Status == "completed") {
			t.Fatal("termination reason lost")
		}
		encoded, _ := json.Marshal(result)
		if !strings.Contains(string(encoded), `"cache_write_tokens":5`) || strings.Contains(string(encoded), "reasoning_tokens") {
			t.Fatal("usage detail invented or misnamed")
		}
		value.Usage.PromptTokensDetails = nil
		result, err = PublicResponses(value, normalized)
		if err != nil || result.Usage.InputTokensDetails != nil {
			t.Fatal("unknown cache detail fabricated")
		}
	}
	if _, err := PublicResponses(Response{}, normalized); err == nil {
		t.Fatal("missing usage accepted")
	}
}

func TestResponsesStructuredTextBoundary(t *testing.T) {
	for name, raw := range map[string]string{
		"messages": `[{"role":"system","content":"policy"},{"type":"message","role":"assistant","content":"history"},{"role":"user","content":[{"type":"input_text","text":"first"},{"type":"input_text","text":" second"}]}]`,
		"string":   `"plain"`,
	} {
		t.Run(name, func(t *testing.T) {
			no := false
			r, err := (ResponsesRequest{Model: "reviewed", Input: json.RawMessage(raw), Store: &no, MaxOutputTokens: 16}).Request()
			if err != nil || r.Messages[len(r.Messages)-1].Role != "user" {
				t.Fatal("valid text rejected")
			}
			if name == "messages" && (len(r.Messages) != 3 || r.Messages[0].Role != "system" || r.Messages[1].Content != "history" || r.Messages[2].Content != "first second") {
				t.Fatal("role/content semantics lost")
			}
		})
	}
	for _, raw := range []string{
		`null`, `[]`, `1`, `["text"]`, `[{"role":"developer","content":"x"}]`, `[{"role":"assistant","content":"x"}]`,
		`[{"role":"user","content":"x","phase":"final_answer"}]`, `[{"type":"item_reference","role":"user","content":"x"}]`,
		`[{"role":"user","content":null}]`, `[{"role":"user","content":[]}]`, `[{"role":"user","content":[{"type":"input_image","text":"x"}]}]`,
		`[{"role":"user","content":[{"type":"input_text","text":"x","prompt_cache_breakpoint":{}}]}]`,
		`[{"role":"assistant","content":[{"type":"input_text","text":"x"}]},{"role":"user","content":"y"}]`,
		`[{"role":"user","content":"x"}] {}`,
		`[{"role":"user","content":[` + strings.TrimSuffix(strings.Repeat(`{"type":"input_text","text":"x"},`, 17), ",") + `]}]`,
		`[{"role":"user","content":"` + strings.Repeat("x", 16385) + `"}]`,
	} {
		if _, err := responsesTextInput(json.RawMessage(raw)); err == nil {
			t.Fatal("unsupported nested input accepted")
		}
	}
	for _, size := range []int{8192, 8193} {
		parts := []map[string]string{{"type": "input_text", "text": strings.Repeat("a", size)}, {"type": "input_text", "text": strings.Repeat("b", size)}}
		raw, _ := json.Marshal([]map[string]any{{"role": "user", "content": parts}})
		messages, err := responsesTextInput(raw)
		if (err == nil) != (size == 8192) {
			t.Fatal("joined text bound not enforced")
		}
		if err == nil && len(messages[0].Content) != 16384 {
			t.Fatal("text truncated")
		}
	}
	many := make([]map[string]string, 64)
	for i := range many {
		many[i] = map[string]string{"role": "user", "content": "x"}
	}
	raw, _ := json.Marshal(many)
	if _, err := responsesTextInput(raw); err == nil {
		t.Fatal("unbounded message count")
	}
}
