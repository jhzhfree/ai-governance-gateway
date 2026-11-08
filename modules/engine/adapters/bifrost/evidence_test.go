package bifrost_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jhzhfree/ai-governance-gateway/modules/engine/execution"
)

func TestStreamRawUsageCannotBeRepairedOrCombinedBySDK(t *testing.T) {
	valid := `{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}`
	finish := `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
	usageFrame := func(u string) string { return `{"choices":[],"usage":` + u + `}` }
	for name, frames := range map[string][]string{
		"duplicate":         {finish, usageFrame(valid), usageFrame(valid)},
		"conflicting":       {finish, usageFrame(valid), usageFrame(`{"prompt_tokens":8,"completion_tokens":3,"total_tokens":11}`)},
		"repaired total":    {finish, usageFrame(`{"prompt_tokens":7,"completion_tokens":3,"total_tokens":1}`)},
		"premature":         {usageFrame(valid), finish},
		"duplicate counter": {finish, usageFrame(`{"prompt_tokens":5,"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}`)},
		"cache mismatch":    {finish, usageFrame(`{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10,"prompt_cache_hit_tokens":3,"prompt_cache_miss_tokens":5}`)},
		"cache partial":     {finish, usageFrame(`{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10,"prompt_cache_hit_tokens":3}`)},
		"cache negative":    {finish, usageFrame(`{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10,"prompt_cache_hit_tokens":-1,"prompt_cache_miss_tokens":8}`)},
		"no finish":         {usageFrame(valid)},
	} {
		t.Run(name, func(t *testing.T) {
			e := fixture(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				for _, frame := range frames {
					_, _ = fmt.Fprintf(w, "data: %s\n\n", frame)
				}
				_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			})
			evidence := 0
			err := e.Execute(context.Background(), request(strings.TrimSuffix(body, "}")+`,"stream":true}`), func(_ context.Context, event execution.Event) error {
				if event.Usage != nil {
					evidence++
				}
				return nil
			})
			if !errors.Is(err, execution.ErrUpstream) || evidence != 0 {
				t.Fatalf("ambiguous raw usage accepted: error=%v evidence=%d", err, evidence)
			}
		})
	}
}
func TestRawCaptureNeverLeavesAdapter(t *testing.T) {
	e := fixture(t, func(w http.ResponseWriter, _ *http.Request) { respond(w, true) })
	if err := e.Execute(context.Background(), request(body), func(_ context.Context, event execution.Event) error {
		if strings.Contains(string(event.Payload), "raw_response") || strings.Contains(string(event.Payload), "raw_request") || event.UpstreamStatus != 200 {
			t.Error("raw capture leaked or success evidence missing")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestJSONUsageWithoutOriginalFinishIsNotCompletion(t *testing.T) {
	for _, reason := range []string{"null", `""`, `"unexpected"`} {
		t.Run(reason, func(t *testing.T) {
			e := fixture(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"synthetic"},"finish_reason":%s}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`, reason)
			})
			emitted := false
			err := e.Execute(context.Background(), request(body), func(context.Context, execution.Event) error { emitted = true; return nil })
			if !errors.Is(err, execution.ErrUpstream) || emitted {
				t.Fatal("unfinished JSON became durable completion", err)
			}
		})
	}
}
