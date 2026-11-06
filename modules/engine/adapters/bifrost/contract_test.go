package bifrost_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	adapter "github.com/jhzhfree/ai-governance-gateway/modules/engine/adapters/bifrost"
	"github.com/jhzhfree/ai-governance-gateway/modules/engine/internal/execution"
)

func TestUnsupportedRequestsNeverDispatch(t *testing.T) {
	var calls atomic.Int32
	e := fixture(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); respond(w, true) })
	add := func(extra string) string { return strings.TrimSuffix(body, "}") + "," + extra + "}" }
	cases := map[string]string{
		"SDK limit expansion":      strings.Replace(body, "32", "8", 1),
		"negative limit":           strings.Replace(body, "32", "-1", 1),
		"missing limit":            strings.Replace(body, `,"max_tokens":32`, "", 1),
		"over server ceiling":      strings.Replace(body, "32", "4097", 1),
		"multiple completions":     add(`"n":2`),
		"unknown option":           add(`"extra_params":{"base_url":"https://example.invalid"}`),
		"duplicate model":          add(`"model":"gpt-4o-mini"`),
		"alternate field case":     add(`"Model":"gpt-4o-mini"`),
		"unknown storage behavior": add(`"store":true`),
		"null stream":              add(`"stream":null`),
		"multimodal file":          `{"model":"gpt-4o-mini","max_tokens":32,"messages":[{"role":"user","content":[{"type":"file","file":{"file_url":"https://example.invalid/private"}}]}]}`,
		"unknown role":             strings.Replace(body, `"user"`, `"developer"`, 1),
		"hidden message parameter": strings.Replace(body, `"role":"user"`, `"role":"user","metadata":{"hidden":true}`, 1),
		"duplicate message role":   strings.Replace(body, `"role":"user"`, `"role":"user","role":"user"`, 1),
		"unverified builtin tool":  add(`"tools":[{"type":"web_search"}]`),
		"unknown tool choice":      add(`"tool_choice":"anything"`),
		"unregistered forced tool": add(`"tool_choice":{"type":"function","function":{"name":"unknown"}}`),
		"invalid tool history":     `{"model":"gpt-4o-mini","max_tokens":32,"messages":[{"role":"assistant","tool_calls":[{"id":"call-1","type":"web_search"}]}]}`,
		"trailing document":        body + `{}`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if err := e.Execute(context.Background(), request(payload), discard); !errors.Is(err, execution.ErrInvalidRequest) {
				t.Fatalf("expected refusal, got %v", err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("unsupported request reached provider: %d", calls.Load())
	}
}

func TestOutputLimitIsNotRaisedAtDispatch(t *testing.T) {
	var observed atomic.Bool
	e := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		var wire struct {
			MaxTokens           *int `json:"max_tokens"`
			MaxCompletionTokens *int `json:"max_completion_tokens"`
		}
		if json.NewDecoder(r.Body).Decode(&wire) != nil {
			t.Error("invalid outgoing request")
		}
		limit := wire.MaxCompletionTokens
		if limit == nil {
			limit = wire.MaxTokens
		}
		if limit == nil || *limit != 16 {
			t.Errorf("authorized output limit changed: %+v", wire)
		} else {
			observed.Store(true)
		}
		respond(w, true)
	})
	if err := e.Execute(context.Background(), request(strings.Replace(body, "32", "16", 1)), discard); err != nil {
		t.Fatal(err)
	}
	if !observed.Load() {
		t.Fatal("output bound was not observed")
	}
}

func TestInvalidUsageNeverEmitsEvidence(t *testing.T) {
	for name, usage := range map[string]string{
		"negative":                     `{"prompt_tokens":-1,"completion_tokens":3,"total_tokens":2}`,
		"inconsistent total":           `{"prompt_tokens":7,"completion_tokens":3,"total_tokens":999}`,
		"output exceeds authorization": `{"prompt_tokens":7,"completion_tokens":33,"total_tokens":40}`,
		"input exceeds ceiling":        `{"prompt_tokens":2000001,"completion_tokens":3,"total_tokens":2000004}`,
		"missing prompt":               `{"completion_tokens":3,"total_tokens":3}`,
	} {
		t.Run(name, func(t *testing.T) {
			e := fixture(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"id":"fixture","object":"chat.completion","model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":%s}`, usage)
			})
			emitted := false
			err := e.Execute(context.Background(), request(body), func(context.Context, execution.Event) error { emitted = true; return nil })
			if !errors.Is(err, execution.ErrUpstream) || emitted {
				t.Fatalf("invalid usage accepted: error=%v emitted=%v", err, emitted)
			}
		})
	}
}

func TestInvalidServerTokenCeilingsRejected(t *testing.T) {
	for _, limit := range []int{-1, 15, 1000001} {
		e, err := adapter.New(context.Background(), adapter.Config{BaseURL: "https://example.invalid", APIKey: "synthetic", Model: model, MaxOutputTokens: limit})
		if err == nil {
			e.Close()
			t.Fatalf("unsafe ceiling accepted: %d", limit)
		}
	}
}

func TestToolResultHistoryReachesProvider(t *testing.T) {
	var observed atomic.Bool
	e := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		var wire struct {
			Messages []struct {
				Role       string            `json:"role"`
				ToolCallID string            `json:"tool_call_id"`
				ToolCalls  []json.RawMessage `json:"tool_calls"`
			} `json:"messages"`
		}
		if json.Unmarshal(data, &wire) != nil || len(wire.Messages) != 3 || len(wire.Messages[1].ToolCalls) != 1 || wire.Messages[2].Role != "tool" || wire.Messages[2].ToolCallID != "call-1" {
			t.Errorf("tool history changed: %s", data)
		} else {
			observed.Store(true)
		}
		respond(w, true)
	})
	payload := `{"model":"gpt-4o-mini","max_tokens":32,"messages":[{"role":"user","content":"lookup"},{"role":"assistant","content":null,"tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"fixture\"}"}}]},{"role":"tool","tool_call_id":"call-1","content":"synthetic result"}]}`
	if err := e.Execute(context.Background(), request(payload), discard); err != nil {
		t.Fatal(err)
	}
	if !observed.Load() {
		t.Fatal("tool history not observed")
	}
}
