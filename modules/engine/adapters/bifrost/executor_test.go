package bifrost_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	adapter "github.com/jhzhfree/ai-governance-gateway/modules/engine/adapters/bifrost"
	"github.com/jhzhfree/ai-governance-gateway/modules/engine/internal/execution"
)

const model = "gpt-4o-mini"
const body = `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"synthetic fixture"}],"max_tokens":32}`

func fixture(t *testing.T, handler http.HandlerFunc) *adapter.Executor {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	e, err := adapter.New(context.Background(), adapter.Config{BaseURL: server.URL, APIKey: "synthetic-not-a-real-key", Model: model, TestLoopback: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e
}

func request(payload string) execution.Request {
	return execution.Request{RequestID: "request-1", AttemptID: "attempt-1", Body: json.RawMessage(payload)}
}
func discard(context.Context, execution.Event) error { return nil }

func respond(w http.ResponseWriter, usage bool) {
	w.Header().Set("Content-Type", "application/json")
	suffix := ""
	if usage {
		suffix = `,"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}`
	}
	fmt.Fprintf(w, `{"id":"fixture","object":"chat.completion","model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]%s}`, suffix)
}

func TestUsageEvidenceAndAttemptIdentity(t *testing.T) {
	var calls atomic.Int32
	e := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		respond(w, true)
	})
	var got execution.Event
	if err := e.Execute(context.Background(), request(body), func(_ context.Context, event execution.Event) error { got = event; return nil }); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("physical attempts = %d", calls.Load())
	}
	if got.RequestID != "request-1" || got.AttemptID != "attempt-1" {
		t.Fatal("lost correlation")
	}
	if got.Usage == nil || got.Usage.TotalTokens != 10 || got.Usage.InputTokens != 7 || got.Usage.OutputTokens != 3 || !json.Valid(got.Usage.Evidence) {
		t.Fatalf("incorrect usage: %+v", got.Usage)
	}
}

func TestMissingUsageRemainsUnknown(t *testing.T) {
	e := fixture(t, func(w http.ResponseWriter, _ *http.Request) { respond(w, false) })
	var got *execution.Usage
	if err := e.Execute(context.Background(), request(body), func(_ context.Context, event execution.Event) error { got = event.Usage; return nil }); err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("missing usage must not become zero-cost evidence: %+v", got)
	}
}

func TestProviderFailureDoesNotRetryOrLeak(t *testing.T) {
	var calls atomic.Int32
	e := fixture(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		fmt.Fprint(w, `{"error":{"message":"synthetic-secret-echo","type":"server_error"}}`)
	})
	err := e.Execute(context.Background(), request(body), discard)
	if !errors.Is(err, execution.ErrUpstream) {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(err.Error(), "synthetic-secret-echo") {
		t.Fatal("raw provider error leaked")
	}
	if calls.Load() != 1 {
		t.Fatalf("unexpected automatic retries: %d", calls.Load())
	}
}

func TestRejectsRoutingOverridesBeforeDispatch(t *testing.T) {
	var calls atomic.Int32
	e := fixture(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); respond(w, true) })
	for _, payload := range []string{
		`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"x"}],"fallbacks":["openai/other"]}`,
		`{"model":"unauthorized","messages":[{"role":"user","content":"x"}]}`,
		`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"x"}],"provider":"other"}`,
		`null`, `[]`, `{`,
	} {
		if err := e.Execute(context.Background(), request(payload), discard); !errors.Is(err, execution.ErrInvalidRequest) {
			t.Fatalf("override accepted: %v", err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid request dispatched")
	}
}

func TestToolDefinitionReachesProvider(t *testing.T) {
	var observed atomic.Bool
	e := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		var wire map[string]json.RawMessage
		if json.Unmarshal(data, &wire) != nil || !strings.Contains(string(wire["tools"]), "lookup") {
			t.Error("tool definition lost")
		} else {
			observed.Store(true)
		}
		respond(w, true)
	})
	payload := `{"model":"gpt-4o-mini","max_tokens":32,"messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}}}],"tool_choice":"auto"}`
	if err := e.Execute(context.Background(), request(payload), discard); err != nil {
		t.Fatal(err)
	}
	if !observed.Load() {
		t.Fatal("no tool request observed")
	}
}

func TestStreamingTerminalUsage(t *testing.T) {
	e := fixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"s\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n")
		w.(http.Flusher).Flush()
		fmt.Fprint(w, "data: {\"id\":\"s\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o-mini\",\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3,\"total_tokens\":10}}\n\ndata: [DONE]\n\n")
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var observed bool
	var last *execution.Usage
	err := e.Execute(ctx, request(strings.TrimSuffix(body, "}")+`,"stream":true}`), func(_ context.Context, event execution.Event) error {
		observed = observed || strings.Contains(string(event.Payload), "hello")
		if event.Usage != nil {
			last = event.Usage
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !observed || last == nil || last.TotalTokens != 10 {
		t.Fatalf("stream evidence lost: content=%v usage=%+v", observed, last)
	}
}

func TestCanceledBeforeDispatch(t *testing.T) {
	var calls atomic.Int32
	e := fixture(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); respond(w, true) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.Execute(ctx, request(body), discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel lost: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("canceled request dispatched")
	}
}

func TestInFlightStreamStopsUpstream(t *testing.T) {
	for _, consumerFailure := range []bool{false, true} {
		t.Run(fmt.Sprintf("consumer_failure=%v", consumerFailure), func(t *testing.T) {
			closed := make(chan struct{})
			e := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"id\":\"s\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n")
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
					close(closed)
				case <-time.After(6 * time.Second):
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			want := errors.New("stream sink unavailable")
			observed := false
			err := e.Execute(ctx, request(strings.TrimSuffix(body, "}")+`,"stream":true}`), func(context.Context, execution.Event) error {
				observed = true
				if consumerFailure {
					return want
				}
				cancel()
				return nil
			})
			if !consumerFailure {
				want = context.Canceled
			}
			if !observed || !errors.Is(err, want) {
				t.Fatalf("stream outcome: observed=%v error=%v", observed, err)
			}
			select {
			case <-closed:
			case <-time.After(2 * time.Second):
				t.Fatal("upstream connection remained open after cancellation")
			}
		})
	}
}

func TestTruncatedStreamIsNotSuccess(t *testing.T) {
	e := fixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"s\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n")
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := e.Execute(ctx, request(strings.TrimSuffix(body, "}")+`,"stream":true}`), discard)
	if !errors.Is(err, execution.ErrUpstream) {
		t.Fatalf("truncated stream incorrectly classified: %v", err)
	}
}

func TestStreamingMissingUsageRemainsUnknown(t *testing.T) {
	e := fixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"s\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := e.Execute(ctx, request(strings.TrimSuffix(body, "}")+`,"stream":true}`), func(_ context.Context, event execution.Event) error {
		if event.Usage != nil {
			t.Errorf("missing streaming usage became billing evidence: %+v", event.Usage)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestConsumerFailureIsPropagated(t *testing.T) {
	e := fixture(t, func(w http.ResponseWriter, _ *http.Request) { respond(w, true) })
	want := errors.New("audit sink unavailable")
	err := e.Execute(context.Background(), request(body), func(context.Context, execution.Event) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("sink failure swallowed: %v", err)
	}
}

func TestUnsafeEndpointRejected(t *testing.T) {
	for _, endpoint := range []string{"http://example.com/v1", "http://localhost/v1", "https://user:pass@example.com/v1", "https://example.com/v1?api_key=x"} {
		if e, err := adapter.New(context.Background(), adapter.Config{BaseURL: endpoint, APIKey: "synthetic", Model: model, TestLoopback: true}); err == nil {
			e.Close()
			t.Fatalf("unsafe endpoint accepted: %s", endpoint)
		}
	}
}
