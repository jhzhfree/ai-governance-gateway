package bifrost

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/modules/engine/execution"
)

const embeddingResult = `{"object":"list","model":"fixture","data":[{"object":"embedding","index":0,"embedding":[0.1,-0.2]}],"usage":{"prompt_tokens":3,"total_tokens":3}}`

func TestEmbeddingBoundedTransportContracts(t *testing.T) {
	for _, supplier := range []string{"aliyun", "volcengine", "zhipu"} {
		t.Run(supplier, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				profile, _ := supplierProfileForEmbedding(supplier)
				if r.URL.Path != profile.path || r.Header.Get("Authorization") != "Bearer synthetic-key" {
					t.Error("wire route or credentials differ")
				}
				body, _ := io.ReadAll(r.Body)
				fields, ok := object(body, "model", "input", "encoding_format", "dimensions")
				if !ok || string(fields["input"]) != `["synthetic"]` || string(fields["encoding_format"]) != `"float"` {
					t.Error("SDK altered reviewed embedding body")
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, embeddingResult)
			}))
			defer server.Close()
			engine, err := New(context.Background(), Config{API: "embeddings", Supplier: supplier, BaseURL: server.URL, APIKey: "synthetic-key", Model: "fixture", TestLoopback: true})
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			var events int
			err = engine.Execute(context.Background(), execution.Request{RequestID: "request", AttemptID: "attempt", Body: json.RawMessage(`{"model":"fixture","input":["synthetic"],"encoding_format":"float"}`)}, func(ctx context.Context, event execution.Event) error {
				events++
				if !event.Completed || event.Usage == nil || event.Usage.InputTokens != 3 || event.Usage.OutputTokens != 0 || event.Usage.TotalTokens != 3 || string(event.Usage.Evidence) != `{"prompt_tokens":3,"total_tokens":3}` {
					t.Error("original usage lost or synthesized")
				}
				if strings.Contains(string(event.Payload), "extra_fields") || strings.Contains(string(event.Payload), "raw_response") {
					t.Error("SDK internals leaked")
				}
				return nil
			})
			if err != nil || events != 1 || calls.Load() != 1 {
				t.Fatal("embedding contract", err, events, calls.Load())
			}
		})
	}
}
func supplierProfileForEmbedding(id string) (supplierProfile, error) {
	_, p, e := resolveSupplier(Config{API: "embeddings", Supplier: id})
	return p, e
}
func TestEmbeddingRejectsInvalidSourceAndNeverRetries(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"missing_usage", strings.Replace(embeddingResult, `,"usage":{"prompt_tokens":3,"total_tokens":3}`, "", 1), 200},
		{"missing_model", strings.Replace(embeddingResult, `"model":"fixture",`, "", 1), 200},
		{"duplicate_usage", strings.Replace(embeddingResult, `"usage":`, `"usage":{"prompt_tokens":3,"total_tokens":3},"usage":`, 1), 200},
		{"duplicate_counter", strings.Replace(embeddingResult, `"prompt_tokens":3`, `"prompt_tokens":3,"prompt_tokens":3`, 1), 200},
		{"wrong_total", strings.Replace(embeddingResult, `"total_tokens":3`, `"total_tokens":4`, 1), 200},
		{"nonzero_output", strings.Replace(embeddingResult, `"total_tokens":3`, `"completion_tokens":1,"total_tokens":3`, 1), 200},
		{"null_vector", strings.Replace(embeddingResult, `[0.1,-0.2]`, `[null,0]`, 1), 200},
		{"duplicate_index", strings.Replace(embeddingResult, `"index":0`, `"index":0,"index":0`, 1), 200},
		{"wrong_index", strings.Replace(embeddingResult, `"index":0`, `"index":1`, 1), 200},
		{"empty_vector", strings.Replace(embeddingResult, `[0.1,-0.2]`, `[]`, 1), 200},
		{"base64_vector", strings.Replace(embeddingResult, `[0.1,-0.2]`, `"abcd"`, 1), 200},
		{"overlong", strings.Repeat(" ", 1<<20) + embeddingResult, 200},
		{"upstream_unavailable", `{"error":"synthetic"}`, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer server.Close()
			engine, err := New(context.Background(), Config{API: "embeddings", Supplier: "aliyun", BaseURL: server.URL, APIKey: "synthetic", Model: "fixture", TestLoopback: true})
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			events := 0
			err = engine.Execute(context.Background(), execution.Request{RequestID: "request", AttemptID: "attempt", Body: json.RawMessage(`{"model":"fixture","input":"synthetic"}`)}, func(context.Context, execution.Event) error { events++; return nil })
			if !errors.Is(err, execution.ErrUpstream) || events != 0 || calls.Load() != 1 {
				t.Fatal("invalid completion escaped/no single attempt", err, events, calls.Load())
			}
		})
	}
}
func TestEmbeddingRejectsClientOperationOverrides(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	engine, err := New(context.Background(), Config{API: "embeddings", Supplier: "volcengine", BaseURL: server.URL, APIKey: "synthetic", Model: "fixture", TestLoopback: true})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	for _, raw := range []string{`{"model":"fixture","input":"x","stream":true}`, `{"model":"other","input":"x"}`, `{"model":"fixture","input":[1,2]}`, `{"model":"fixture","input":"x","encoding_format":"base64"}`, `{"model":"fixture","input":"x","dimensions":256}`, `{"model":"fixture","messages":[{"role":"user","content":"x"}],"max_tokens":16}`, `{"model":"fixture","input":"x","fallbacks":["other"]}`} {
		if err := engine.Execute(context.Background(), execution.Request{RequestID: "request", AttemptID: "attempt", Body: json.RawMessage(raw)}, func(context.Context, execution.Event) error { return nil }); !errors.Is(err, execution.ErrInvalidRequest) {
			t.Fatal("invalid embedding accepted", err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid input dispatched")
	}
	if _, err := New(context.Background(), Config{API: "embeddings", Supplier: "deepseek", APIKey: "synthetic", Model: "fixture"}); err == nil {
		t.Fatal("unreviewed DeepSeek embeddings")
	}
}

func TestEmbeddingCancellationReclaimsTransport(t *testing.T) {
	started := make(chan struct{})
	closed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list",`)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(closed)
	}))
	defer server.Close()
	engine, err := New(context.Background(), Config{API: "embeddings", Supplier: "aliyun", BaseURL: server.URL, APIKey: "synthetic", Model: "fixture", TestLoopback: true, MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- engine.Execute(ctx, execution.Request{RequestID: "request", AttemptID: "attempt", Body: json.RawMessage(`{"model":"fixture","input":"synthetic"}`)}, func(context.Context, execution.Event) error { t.Error("partial vector response emitted"); return nil })
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("not started")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("cancel lost", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("executor not reclaimed")
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream connection not canceled")
	}
}
