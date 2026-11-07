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
	valid.Stream = true
	streamed, err := valid.Request()
	if err != nil || !streamed.Stream || !streamed.MessagesStream {
		t.Fatal("reviewed streaming marker lost")
	}
	for _, change := range []func(*MessagesRequest){func(r *MessagesRequest) { r.MaxTokens = 0 }, func(r *MessagesRequest) { r.Messages = nil }, func(r *MessagesRequest) { r.Messages = []MessagesInput{{Role: "system", Content: "bypass"}} }, func(r *MessagesRequest) { r.Messages = []MessagesInput{{Role: "assistant", Content: "prefill"}} }} {
		r := valid
		change(&r)
		if _, err := r.Request(); err == nil {
			t.Fatal("unsupported request accepted")
		}
	}
}

func TestMessagesInitialUsageCannotGuessOrOverflow(t *testing.T) {
	for _, raw := range []string{"", `{}`, `{"input_tokens":2,"output_tokens":0}`, `{"input_tokens":-1,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}`, `{"input_tokens":2,"output_tokens":9,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}`, `{"input_tokens":9223372036854775807,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}`, `{"input_tokens":2,"output_tokens":0,"cache_read_input_tokens":127,"cache_creation_input_tokens":0}`, `{"input_tokens":2,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"unknown":1}`} {
		if _, err := DecodeMessagesInitialUsage(raw, 128, 8); err == nil {
			t.Fatal("invalid initial counters accepted")
		}
	}
	initial, err := DecodeMessagesInitialUsage(`{"input_tokens":2,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}`, 128, 8)
	if err != nil || initial.InputTokens != 2 || initial.OutputTokens != 0 {
		t.Fatal("explicit original zero lost")
	}
	if initial.MatchesFinal(nil) {
		t.Fatal("initial counters became final proof")
	}
}
