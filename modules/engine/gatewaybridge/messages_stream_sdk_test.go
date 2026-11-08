//go:build sdk_e2e

package gatewaybridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func TestEmbeddedMessagesStreamPythonSDK(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "missing-stop"}[missing], func(t *testing.T) {
			cfg := testutil.MainlandConfig()
			cfg.Models = cfg.Models[3:]
			cfg.Models[0].API = "messages"
			cfg.Models[0].MaxOutputTokens = 32
			cfg.Models[0].MaxInputTokens = 512
			cfg.Models[0].AllowMessagesStream = true
			cfg.Models[0].CachePricing = &provider.CachePricing{ReadPerMillion: 500000}
			f := embeddedProductFixture(t, cfg, "/anthropic/v1/messages", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Model     string `json:"model"`
					Stream    bool   `json:"stream"`
					MaxTokens int    `json:"max_tokens"`
				}
				if json.NewDecoder(r.Body).Decode(&req) != nil || !req.Stream || req.Model != "fixture-model" || req.MaxTokens != 32 {
					t.Error("SDK changed physical stream request")
					return
				}
				wire := testutil.EmbeddedMessagesStream(`{"input_tokens":1,"output_tokens":0,"cache_read_input_tokens":2,"cache_creation_input_tokens":0}`, `{"input_tokens":1,"output_tokens":2,"cache_read_input_tokens":2,"cache_creation_input_tokens":0}`, "synthetic")
				if missing {
					wire = strings.Split(wire, "event: message_stop")[0]
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, wire)
			}))
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			run := func(envName, program string) {
				t.Helper()
				python := os.Getenv(envName)
				if python == "" {
					t.Fatal(envName + " required")
				}
				cmd := exec.CommandContext(ctx, python, "-c", program)
				cmd.Env = []string{"AIGW_SDK_URL=" + f.server.URL, "AIGW_SDK_KEY=" + f.key, "AIGW_CASE=" + map[bool]string{true: "missing", false: "complete"}[missing]}
				if err := cmd.Run(); err != nil {
					t.Fatal("native streaming SDK contract failed; output suppressed", err)
				}
			}
			run("AIGW_MESSAGES_SDK_PYTHON", `
import os, anthropic
assert anthropic.__version__ == "2.6.0"
client = anthropic.Anthropic(api_key=os.environ["AIGW_SDK_KEY"],base_url=os.environ["AIGW_SDK_URL"],max_retries=0,default_headers={"anthropic-version":"2023-06-01"})
args = dict(model="deepseek/fixture",max_tokens=32,messages=[{"role":"user","content":"synthetic"}])
if os.environ["AIGW_CASE"] == "missing":
    try:
        with client.messages.stream(**args) as stream:
            stream.get_final_message()
        raise AssertionError("missing stop accepted")
    except anthropic.APIError:
        pass
else:
    events = list(client.messages.create(**args,stream=True))
    assert events[0].type == "message_start" and events[-1].type == "message_stop"
    assert events[0].message.usage.output_tokens == 0 and events[0].message.usage.cache_read_input_tokens == 2
    final = next(e.usage for e in events if e.type == "message_delta")
    assert final.output_tokens == 2
    with client.messages.stream(**args) as stream:
        result = stream.get_final_message()
        assert result.content[0].text == "synthetic"
        assert result.usage.input_tokens == 1 and result.usage.output_tokens == 2
        assert result.usage.cache_read_input_tokens == 2 and result.usage.cache_creation_input_tokens == 0
client.close()
`)
			if !missing {
				run("AIGW_SDK_PYTHON", `
import os, openai
assert openai.__version__ == "3.46.0"
client = openai.OpenAI(api_key=os.environ["AIGW_SDK_KEY"],base_url=os.environ["AIGW_SDK_URL"]+"/v1",max_retries=0)
chunks = list(client.chat.completions.create(model="deepseek/fixture",max_tokens=32,stream=True,messages=[{"role":"user","content":"synthetic"}]))
usage = next(c.usage for c in chunks if c.usage)
assert usage.prompt_tokens == 3 and usage.completion_tokens == 2 and usage.prompt_tokens_details.cached_tokens == 2
events = list(client.responses.create(model="deepseek/fixture",max_output_tokens=32,input="synthetic",stream=True,store=False))
result = next(e.response for e in events if e.type == "response.completed")
assert result.usage.input_tokens == 3 and result.usage.output_tokens == 2 and result.usage.input_tokens_details.cached_tokens == 2
client.close()
`)
			}
			var posted, unknown int
			if err := f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM engine_completion_receipts WHERE actual=11),(SELECT count(*) FROM budget_reservations WHERE state='uncertain')`).Scan(&posted, &unknown); err != nil {
				t.Fatal(err)
			}
			if missing {
				if posted != 0 || unknown != 1 || f.calls.Load() != 1 {
					t.Fatal("missing stop charged or replayed")
				}
			} else if posted != 4 || unknown != 0 || f.calls.Load() != 4 {
				t.Fatal("SDK final counts not exactly once", posted, unknown, f.calls.Load())
			}
		})
	}
}
