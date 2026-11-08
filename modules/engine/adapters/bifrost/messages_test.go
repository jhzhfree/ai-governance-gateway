package bifrost_test

import (
	"context"
	"encoding/json"
	"fmt"
	adapter "github.com/jhzhfree/ai-governance-gateway/modules/engine/adapters/bifrost"
	"github.com/jhzhfree/ai-governance-gateway/modules/engine/execution"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNativeMessagesSDKConversionAndEvidence(t *testing.T) {
	const usage = `{ "input_tokens":1,"output_tokens":2,"cache_read_input_tokens":2,"cache_creation_input_tokens":0,"service_tier":"standard" }`
	for _, mode := range []string{"valid", "missing creation", "nonzero creation", "server tool", "missing usage", "wrong model", "wrong finish", "duplicate finish", "unexpected content", "malformed", "wrong media", "stream", "system order", "prefill", "adjacent roles"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/anthropic/v1/messages" || r.Header.Get("Authorization") != "Bearer synthetic-messages-key" || r.Header.Get("Anthropic-Beta") != "" || r.Header.Get("User-Agent") == "claude-code" {
					t.Error("unreviewed transport")
				}
				var wire map[string]json.RawMessage
				if json.NewDecoder(r.Body).Decode(&wire) != nil || string(wire["model"]) != `"fixture-model"` || string(wire["max_tokens"]) != "32" || string(wire["thinking"]) != `{"type":"disabled"}` || string(wire["stream"]) != "false" {
					t.Error("SDK changed approved model/cap/thinking")
				}
				rawUsage := usage
				switch mode {
				case "missing creation":
					rawUsage = strings.Replace(rawUsage, `,"cache_creation_input_tokens":0`, "", 1)
				case "nonzero creation":
					rawUsage = strings.Replace(rawUsage, `"cache_creation_input_tokens":0`, `"cache_creation_input_tokens":1`, 1)
				case "server tool":
					rawUsage = strings.TrimSuffix(rawUsage, " }") + `,"server_tool_use":{"web_search_requests":1}}`
				case "missing usage":
					rawUsage = "null"
				}
				raw := fmt.Sprintf(`{"id":"msg_synthetic","type":"message","role":"assistant","model":"fixture-model","content":[{"type":"text","text":"synthetic "},{"type":"text","text":"response"}],"stop_reason":"end_turn","stop_sequence":null,"usage":%s}`, rawUsage)
				switch mode {
				case "wrong model":
					raw = strings.Replace(raw, `"model":"fixture-model"`, `"model":"unreviewed-model"`, 1)
				case "wrong finish":
					raw = strings.Replace(raw, `"end_turn"`, `"tool_use"`, 1)
				case "duplicate finish":
					raw = strings.Replace(raw, `"stop_reason":"end_turn"`, `"stop_reason":"max_tokens","stop_reason":"end_turn"`, 1)
				case "unexpected content":
					raw = strings.Replace(raw, `"type":"text"`, `"type":"thinking"`, 1)
				case "malformed":
					raw += " {}"
				}
				media := "application/json"
				if mode == "wrong media" {
					media = "text/html"
				}
				w.Header().Set("Content-Type", media)
				fmt.Fprint(w, raw)
			}))
			defer server.Close()
			e, err := adapter.New(context.Background(), adapter.Config{API: "messages", Supplier: "deepseek", Model: "fixture-model", APIKey: "synthetic-messages-key", BaseURL: server.URL, TestLoopback: true, MaxOutputTokens: 32})
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			body := `{"model":"fixture-model","max_tokens":32,"stream":false,"messages":[{"role":"system","content":"synthetic system"},{"role":"user","content":"first"},{"role":"assistant","content":"response"},{"role":"user","content":"last"}]}`
			rejected := mode == "stream" || mode == "system order" || mode == "prefill" || mode == "adjacent roles"
			switch mode {
			case "stream":
				body = strings.Replace(body, `"stream":false`, `"stream":true`, 1)
			case "system order":
				body = strings.Replace(body, `"role":"assistant"`, `"role":"system"`, 1)
			case "prefill":
				body = strings.Replace(body, `"role":"user","content":"last"`, `"role":"assistant","content":"last"`, 1)
			case "adjacent roles":
				body = strings.Replace(body, `"role":"assistant"`, `"role":"user"`, 1)
			}
			events := 0
			err = e.Execute(context.Background(), execution.Request{RequestID: "native-request", AttemptID: "native-attempt", Body: json.RawMessage(body)}, func(ctx context.Context, event execution.Event) error {
				events++
				if !event.Completed || event.Usage == nil || event.Usage.InputTokens != 3 || event.Usage.OutputTokens != 2 || event.Usage.TotalTokens != 5 || string(event.Usage.Evidence) != usage || !strings.Contains(string(event.Payload), "synthetic response") {
					t.Error("SDK lost original evidence or text semantics")
				}
				return nil
			})
			if mode == "valid" {
				if err != nil || events != 1 || calls.Load() != 1 {
					t.Fatal("valid Messages rejected", err, calls.Load(), events)
				}
			} else if err == nil || events != 0 || (rejected && calls.Load() != 0) || (!rejected && calls.Load() != 1) {
				t.Fatal("failure boundary incorrect", err, calls.Load(), events)
			}
		})
	}
}
