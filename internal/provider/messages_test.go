package provider

import "testing"

func TestMessagesEvidenceAndDisjointCounts(t *testing.T) {
	value := Response{ID: "chatcmpl-test", Model: "reviewed", Choices: []Choice{{Message: &Delta{Content: new("synthetic")}, FinishReason: new("stop")}}, Usage: &Usage{PromptTokens: new(int64(11)), CompletionTokens: new(int64(3)), TotalTokens: new(int64(14)), PromptTokensDetails: &InputTokenDetails{CachedTokens: new(int64(4)), CachedCreationTokens: new(int64(5))}}}
	result, err := PublicMessages(value)
	if err != nil || result.Usage.InputTokens != 2 || result.Usage.CacheReadInputTokens != 4 || result.Usage.CacheCreationInputTokens != 5 || result.StopReason != "end_turn" {
		t.Fatal("cache counts double-counted", err)
	}
	value.Usage.PromptTokensDetails.CachedCreationTokens = new(int64(8))
	if _, err := PublicMessages(value); err == nil {
		t.Fatal("overlapping counters accepted")
	}
	value.Usage.PromptTokensDetails = nil
	if _, err := PublicMessages(value); err == nil {
		t.Fatal("missing cache facts guessed zero")
	}
}
func TestMessagesRequestBoundaries(t *testing.T) {
	valid := MessagesRequest{Model: "reviewed", MaxTokens: 8, System: "instructions", Messages: []MessagesInput{{Role: "user", Content: "question"}, {Role: "assistant", Content: "answer"}, {Role: "user", Content: "next"}}}
	req, err := valid.Request()
	if err != nil || len(req.Messages) != 4 || req.Messages[0].Role != "system" {
		t.Fatal("conversation mapping failed", err)
	}
	for _, change := range []func(*MessagesRequest){func(r *MessagesRequest) { r.Stream = true }, func(r *MessagesRequest) { r.MaxTokens = 0 }, func(r *MessagesRequest) { r.Messages = nil }, func(r *MessagesRequest) { r.Messages = []MessagesInput{{Role: "system", Content: "bypass"}} }, func(r *MessagesRequest) { r.Messages = []MessagesInput{{Role: "assistant", Content: "prefill"}} }} {
		r := valid
		change(&r)
		if _, err := r.Request(); err == nil {
			t.Fatal("unsupported request accepted")
		}
	}
}
