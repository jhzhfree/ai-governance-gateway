package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResponsesStatelessTextAndEvidence(t *testing.T) {
	no := false
	request := ResponsesRequest{Model: "reviewed", Input: "synthetic input", Instructions: "synthetic instructions", MaxOutputTokens: 16, Store: &no}
	normalized, err := request.Request()
	if err != nil || len(normalized.Messages) != 2 || normalized.Messages[0].Role != "system" || normalized.Stream || normalized.MaxTokens != 16 {
		t.Fatal("incorrect normalized request")
	}
	for name, mutate := range map[string]func(*ResponsesRequest){
		"missing store": func(r *ResponsesRequest) { r.Store = nil },
		"stored":        func(r *ResponsesRequest) { yes := true; r.Store = &yes },
		"stream":        func(r *ResponsesRequest) { r.Stream = true },
		"background":    func(r *ResponsesRequest) { r.Background = true },
		"empty":         func(r *ResponsesRequest) { r.Input = "" },
		"large":         func(r *ResponsesRequest) { r.Input = strings.Repeat("x", (16<<10)+1) },
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
