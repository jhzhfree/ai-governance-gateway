package bifrost_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
	adapter "github.com/jhzhfree/ai-governance-gateway/modules/engine/adapters/bifrost"
	"github.com/jhzhfree/ai-governance-gateway/modules/engine/execution"
)

func TestNativeMessagesStreamOriginalSequence(t *testing.T) {
	const initial = `{ "input_tokens":1, "output_tokens":0, "cache_read_input_tokens":2, "cache_creation_input_tokens":0 }`
	const final = `{ "input_tokens":1, "output_tokens":2, "cache_read_input_tokens":2, "cache_creation_input_tokens":0 }`
	for _, mode := range []string{"valid", "ping", "length", "refusal", "missing initial", "missing zero", "final mismatch", "output regressed", "extra billing", "missing stop", "missing final", "duplicate start", "duplicate final", "wrong index", "wrong event", "duplicate event", "duplicate type", "wrong model", "nonempty block start", "thinking", "error", "oversized frame"} {
		t.Run(mode, func(t *testing.T) {
			start, finish := initial, final
			switch mode {
			case "missing initial":
				start = "null"
			case "missing zero":
				start = strings.Replace(start, `, "cache_creation_input_tokens":0`, "", 1)
			case "final mismatch":
				finish = strings.Replace(finish, `"input_tokens":1`, `"input_tokens":2`, 1)
			case "output regressed":
				start = strings.Replace(start, `"output_tokens":0`, `"output_tokens":3`, 1)
			case "extra billing":
				finish = strings.TrimSuffix(finish, " }") + `,"server_tool_use":{"web_search_requests":1}}`
			}
			wire := testutil.EmbeddedMessagesStream(start, finish, "synthetic text")
			switch mode {
			case "ping":
				wire = ": keepalive\n\nevent: ping\ndata: {\"type\":\"ping\"}\n\n" + wire
			case "length":
				wire = strings.Replace(wire, `"stop_reason":"end_turn"`, `"stop_reason":"max_tokens"`, 1)
			case "refusal":
				wire = strings.Replace(wire, `"stop_reason":"end_turn"`, `"stop_reason":"refusal"`, 1)
			case "missing stop":
				wire = strings.Split(wire, "event: message_stop")[0]
			case "missing final":
				a, _, _ := strings.Cut(wire, "event: message_delta")
				wire = a + "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
			case "duplicate start":
				first, _, _ := strings.Cut(wire, "event: content_block_start")
				wire = first + wire
			case "duplicate final":
				a, tail, _ := strings.Cut(wire, "event: message_delta")
				frame, _, _ := strings.Cut(tail, "event: message_stop")
				wire = a + "event: message_delta" + frame + "event: message_delta" + tail
			case "wrong index":
				wire = strings.Replace(wire, `"index":0`, `"index":1`, 1)
			case "wrong event":
				wire = strings.Replace(wire, "event: message_start", "event: content_block_start", 1)
			case "duplicate event":
				wire = strings.Replace(wire, "event: message_start", "event: message_start\nevent: message_start", 1)
			case "duplicate type":
				wire = strings.Replace(wire, `"type":"message_start"`, `"type":"ping","type":"message_start"`, 1)
			case "wrong model":
				wire = strings.Replace(wire, `"model":"fixture-model"`, `"model":"other-model"`, 1)
			case "nonempty block start":
				wire = strings.Replace(wire, `"text":""`, `"text":"lost text"`, 1)
			case "thinking":
				wire = strings.Replace(wire, `"type":"text_delta"`, `"type":"thinking_delta"`, 1)
			case "error":
				wire = "data: {\"error\":{\"message\":\"synthetic secret\"}}\n\n"
			case "oversized frame":
				wire = ":" + strings.Repeat(" ", 64<<10) + "\n\n" + wire
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/anthropic/v1/messages" || r.Header.Get("Accept") != "text/event-stream" || r.Header.Get("Authorization") != "Bearer synthetic" {
					t.Error("unreviewed native stream transport")
				}
				var body map[string]json.RawMessage
				if json.NewDecoder(r.Body).Decode(&body) != nil || string(body["stream"]) != "true" || string(body["thinking"]) != `{"type":"disabled"}` {
					t.Error("stream capability changed")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, wire)
			}))
			defer server.Close()
			e, err := adapter.New(context.Background(), adapter.Config{API: "messages", Supplier: "deepseek", Model: "fixture-model", APIKey: "synthetic", BaseURL: server.URL, TestLoopback: true, MaxOutputTokens: 32})
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			completed, early, textEvents := 0, 0, 0
			err = e.Execute(context.Background(), execution.Request{RequestID: "synthetic-request", AttemptID: "synthetic-attempt", Body: json.RawMessage(`{"model":"fixture-model","max_tokens":32,"stream":true,"messages":[{"role":"user","content":"synthetic"}]}`)}, func(ctx context.Context, event execution.Event) error {
				if len(event.InitialUsage) > 0 {
					early++
					if string(event.InitialUsage) != start || event.Completed || event.Usage != nil {
						t.Error("early evidence fabricated or charged")
					}
				}
				if strings.Contains(string(event.Payload), "synthetic text") {
					textEvents++
				}
				if event.Completed {
					completed++
					if event.Usage == nil || string(event.Usage.Evidence) != final || event.Usage.InputTokens != 3 || event.Usage.OutputTokens != 2 {
						t.Error("final cumulative evidence changed")
					}
				}
				return nil
			})
			valid := mode == "valid" || mode == "ping" || mode == "length" || mode == "refusal"
			if valid {
				if err != nil || completed != 1 || early != 1 || textEvents != 1 {
					t.Fatal("valid realtime stream failed", err, completed, early, textEvents)
				}
			} else if err == nil || completed != 0 {
				t.Fatal("invalid stream claimed completion", err, completed)
			}
			if calls.Load() != 1 {
				t.Fatal("stream retried")
			}
		})
	}
}
