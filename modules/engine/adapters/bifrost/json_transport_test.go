package bifrost_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	adapter "github.com/jhzhfree/ai-governance-gateway/modules/engine/adapters/bifrost"
	"github.com/jhzhfree/ai-governance-gateway/modules/engine/execution"
)

const jsonCompletion = `{"id":"fixture","object":"chat.completion","model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`

func TestJSONResponseLimitAcceptsExactBound(t *testing.T) {
	e := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.(http.Flusher).Flush()
		io.WriteString(w, strings.Repeat(" ", (1<<20)-len(jsonCompletion))+jsonCompletion)
	})
	var events int
	if err := e.Execute(context.Background(), request(body), func(_ context.Context, event execution.Event) error { events++; return nil }); err != nil || events != 1 {
		t.Fatal("exact byte limit should remain usable", err, events)
	}
}

func TestJSONTLSAndDestinationPolicy(t *testing.T) {
	for _, testLoopback := range []bool{false, true} {
		var connections, calls atomic.Int32
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
		server.Config.ErrorLog = log.New(io.Discard, "", 0)
		server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
			if state == http.StateNew {
				connections.Add(1)
			}
		}
		server.StartTLS()
		e, err := adapter.New(context.Background(), adapter.Config{BaseURL: server.URL, APIKey: "synthetic", Model: model, TestLoopback: testLoopback})
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		err = e.Execute(context.Background(), request(body), discard)
		e.Close()
		server.Close()
		if !errors.Is(err, execution.ErrUpstream) || calls.Load() != 0 || (!testLoopback && connections.Load() != 0) || (testLoopback && connections.Load() == 0) {
			t.Fatal("JSON destination/TLS policy failed", err, connections.Load(), calls.Load())
		}
	}
}

func TestJSONWireBoundsPrecedeParsing(t *testing.T) {
	var redirects atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirects.Add(1) }))
	defer target.Close()
	for _, mode := range []string{"declared_size", "chunked_size", "media", "encoding", "compressed", "redirect", "trailing", "invalid_utf8", "in_band_error", "duplicate_finish", "duplicate_message", "null_index", "mixed_choice", "choice_case", "usage_case"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			e := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				switch mode {
				case "declared_size":
					w.Header().Set("Content-Length", "1048577")
				case "chunked_size":
					w.(http.Flusher).Flush()
					io.WriteString(w, strings.Repeat(" ", 1<<20))
				case "media":
					w.Header().Set("Content-Type", "text/html")
				case "encoding":
					w.Header().Set("Content-Encoding", "unreviewed")
				case "compressed":
					var compressed bytes.Buffer
					gz := gzip.NewWriter(&compressed)
					io.WriteString(gz, strings.Repeat(" ", 1<<20)+jsonCompletion)
					gz.Close()
					w.Header().Set("Content-Encoding", "gzip")
					w.Write(compressed.Bytes())
					return
				case "redirect":
					w.Header().Set("Location", target.URL)
					w.WriteHeader(307)
				case "trailing":
					io.WriteString(w, jsonCompletion)
				case "invalid_utf8":
					io.WriteString(w, strings.Replace(jsonCompletion, `"ok"`, "\"\xff\"", 1))
					return
				case "in_band_error":
					io.WriteString(w, strings.Replace(jsonCompletion, `"id":"fixture"`, `"error":{"message":"synthetic-private-error"},"id":"fixture"`, 1))
					return
				case "duplicate_finish":
					io.WriteString(w, strings.Replace(jsonCompletion, `"finish_reason":"stop"`, `"finish_reason":null,"finish_reason":"stop"`, 1))
					return
				case "duplicate_message":
					io.WriteString(w, strings.Replace(jsonCompletion, `"message":`, `"message":null,"message":`, 1))
					return
				case "null_index":
					io.WriteString(w, strings.Replace(jsonCompletion, `"index":0`, `"index":null`, 1))
					return
				case "mixed_choice":
					io.WriteString(w, strings.Replace(jsonCompletion, `"index":0`, `"index":0,"delta":{"content":"unreviewed"}`, 1))
					return
				case "choice_case":
					io.WriteString(w, strings.Replace(jsonCompletion, `"choices":[`, `"Choices":[],"choices":[`, 1))
					return
				case "usage_case":
					io.WriteString(w, strings.Replace(jsonCompletion, `"usage":`, `"Usage":null,"usage":`, 1))
					return
				}
				io.WriteString(w, jsonCompletion)
			})
			events := 0
			err := e.Execute(context.Background(), request(body), func(context.Context, execution.Event) error { events++; return nil })
			if !errors.Is(err, execution.ErrUpstream) || events != 0 || calls.Load() != 1 || redirects.Load() != 0 {
				t.Fatalf("invalid wire response accepted: error=%v events=%d calls=%d redirects=%d", err, events, calls.Load(), redirects.Load())
			}
		})
	}
}

func TestJSONOriginalUsageAndInternalMetadataBoundary(t *testing.T) {
	const original = `{ "prompt_tokens" : 7, "completion_tokens": 3, "total_tokens" : 10 }`
	e := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Encoding") != "identity" || r.Header.Get("Accept") != "application/json" {
			t.Error("explicit JSON/identity wire contract missing")
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		raw := strings.Replace(jsonCompletion, `{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}`, original, 1)
		raw = strings.Replace(raw, `"id":"fixture"`, `"extra_fields":{"raw_request":"synthetic-secret","provider_response_headers":{"X-Secret":"synthetic-secret"}},"id":"fixture"`, 1)
		io.WriteString(w, raw)
	})
	var got execution.Event
	err := e.Execute(context.Background(), request(body), func(_ context.Context, event execution.Event) error { got = event; return nil })
	if err != nil || got.Usage == nil || string(got.Usage.Evidence) != original || !got.Completed {
		t.Fatalf("raw usage bytes changed: %v", err)
	}
	if strings.Contains(string(got.Payload), "synthetic-secret") || strings.Contains(string(got.Payload), "raw_request") || strings.Contains(string(got.Payload), "provider_response_headers") {
		t.Fatal("untrusted SDK metadata escaped adapter")
	}
}

func TestJSONIdleAndCancellationCloseUpstream(t *testing.T) {
	for _, mode := range []string{"idle", "cancel", "close"} {
		t.Run(mode, func(t *testing.T) {
			started, ended := make(chan struct{}), make(chan struct{})
			e := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"choices":[`)
				w.(http.Flusher).Flush()
				close(started)
				<-r.Context().Done()
				close(ended)
			})
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- e.Execute(ctx, request(body), discard) }()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("JSON request did not start")
			}
			want := context.DeadlineExceeded
			if mode == "cancel" {
				cancel()
				want = context.Canceled
			} else if mode == "close" {
				go e.Close()
				want = context.Canceled
			}
			select {
			case err := <-done:
				if !errors.Is(err, want) || (mode == "idle" && ctx.Err() != nil) {
					t.Fatalf("JSON cancellation/deadline not preserved: %v", err)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("JSON receiver did not stop")
			}
			select {
			case <-ended:
			case <-time.After(time.Second):
				t.Fatal("JSON connection not reclaimed")
			}
		})
	}
}
