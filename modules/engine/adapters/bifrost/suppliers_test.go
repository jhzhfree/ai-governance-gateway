package bifrost_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	adapter "github.com/jhzhfree/ai-governance-gateway/modules/engine/adapters/bifrost"
	"github.com/jhzhfree/ai-governance-gateway/modules/engine/execution"
)

func TestMainlandSupplierWireContracts(t *testing.T) {
	for _, profile := range provider.Profiles() {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", profile.ID, stream), func(t *testing.T) {
				endpoint, _ := url.Parse(profile.Endpoint)
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if r.URL.Path != endpoint.Path || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer synthetic-vendor-key" || r.Header.Get("Cookie") != "" {
						t.Error("supplier path or credential isolation failed")
					}
					var wire map[string]json.RawMessage
					if json.NewDecoder(r.Body).Decode(&wire) != nil {
						t.Error("invalid outgoing JSON")
					}
					if string(wire["model"]) != `"fixture-model"` || string(wire["max_tokens"]) != "32" || len(wire["max_completion_tokens"]) != 0 {
						t.Errorf("model/token field changed: model=%s limit=%s alternate=%s", wire["model"], wire["max_tokens"], wire["max_completion_tokens"])
					}
					if profile.ID == "aliyun" {
						if string(wire["enable_thinking"]) != "false" || len(wire["thinking"]) != 0 {
							t.Error("aliyun thinking mode not pinned")
						}
					} else {
						if string(wire["thinking"]) != `{"type":"disabled"}` || len(wire["enable_thinking"]) != 0 {
							t.Error("supplier thinking mode not pinned")
						}
					}
					if stream && profile.IncludeUsage {
						if string(wire["stream_options"]) != `{"include_usage":true}` {
							t.Error("missing usage request")
						}
					} else if len(wire["stream_options"]) != 0 {
						t.Error("unexpected stream options")
					}
					if !stream {
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprint(w, `{"id":"fixture","choices":[{"index":0,"message":{"role":"assistant","content":"synthetic"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					if profile.ID == "deepseek" {
						fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"synthetic\"},\"finish_reason\":null}],\"usage\":null}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5,\"prompt_cache_hit_tokens\":1,\"prompt_cache_miss_tokens\":2}}\n\ndata: [DONE]\n\n")
						return
					}

					fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"synthetic\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\ndata: [DONE]\n\n")
				}))
				defer server.Close()
				engine, err := adapter.New(context.Background(), adapter.Config{Supplier: profile.ID, BaseURL: server.URL, APIKey: "synthetic-vendor-key", Model: "fixture-model", MaxOutputTokens: 32, TestLoopback: true})
				if err != nil {
					t.Fatal(err)
				}
				defer engine.Close()
				payload := fmt.Sprintf(`{"model":"fixture-model","max_tokens":32,"stream":%t,"messages":[{"role":"user","content":"synthetic"}]}`, stream)
				evidence := 0
				err = engine.Execute(context.Background(), execution.Request{RequestID: "r", AttemptID: "a", Body: json.RawMessage(payload)}, func(_ context.Context, event execution.Event) error {
					if event.Usage != nil {
						evidence++
						if event.Usage.TotalTokens != 5 {
							t.Error("usage changed")
						}
					}
					return nil
				})
				if err != nil || calls.Load() != 1 || evidence != 1 {
					t.Fatalf("supplier execution: err=%v requests=%d evidence=%d", err, calls.Load(), evidence)
				}
			})
		}
	}
}
func TestSupplierEndpointsCannotBeOverridden(t *testing.T) {
	for _, cfg := range []adapter.Config{
		{Supplier: "unknown", BaseURL: "https://example.invalid"},
		{Supplier: "aliyun", BaseURL: "https://example.invalid"},
		{Supplier: "zhipu", BaseURL: "https://open.bigmodel.cn/override"},
		{Supplier: "volcengine", BaseURL: "http://127.0.0.1/path", TestLoopback: true},
		{Supplier: "aliyun", BaseURL: "http://localhost", TestLoopback: true},
	} {
		cfg.APIKey = "synthetic-private-key"
		cfg.Model = "fixture-model"
		engine, err := adapter.New(context.Background(), cfg)
		if err == nil {
			engine.Close()
			t.Error("unsafe supplier configuration accepted")
		}
		if strings.Contains(fmt.Sprintf("%v %+v %#v", cfg, cfg, cfg), cfg.APIKey) {
			t.Fatal("configuration formatting leaked credential")
		}
	}
}

func TestSupplierRequestsCannotOverrideServerFields(t *testing.T) {
	for _, profile := range provider.Profiles() {
		t.Run(profile.ID, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
			defer server.Close()
			engine, err := adapter.New(context.Background(), adapter.Config{Supplier: profile.ID, BaseURL: server.URL, APIKey: "synthetic", Model: "fixture-model", TestLoopback: true})
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			base := `{"model":"fixture-model","max_tokens":16,"messages":[{"role":"user","content":"synthetic"}]`
			for _, extra := range []string{`"enable_thinking":true`, `"thinking":{"type":"enabled"}`, `"stream_options":{"include_usage":false}`, `"base_url":"https://example.invalid"`, `"api_key":"synthetic-override"`, `"provider":"other"`, `"max_completion_tokens":1000`} {
				for _, stream := range []bool{false, true} {
					raw := fmt.Sprintf(`%s,"stream":%t,%s}`, base, stream, extra)
					err = engine.Execute(context.Background(), execution.Request{RequestID: "r", AttemptID: "a", Body: json.RawMessage(raw)}, func(context.Context, execution.Event) error { t.Error("invalid request emitted event"); return nil })
					if !errors.Is(err, execution.ErrInvalidRequest) {
						t.Error("request overrode server policy")
					}
				}
			}
			if calls.Load() != 0 {
				t.Fatal("rejected request contacted supplier")
			}
		})
	}
}
