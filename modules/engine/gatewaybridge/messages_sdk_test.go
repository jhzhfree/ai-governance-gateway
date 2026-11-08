//go:build sdk_e2e

package gatewaybridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"testing"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func TestEmbeddedMessagesPythonSDK(t *testing.T) {
	python := os.Getenv("AIGW_MESSAGES_SDK_PYTHON")
	if python == "" {
		t.Fatal("AIGW_MESSAGES_SDK_PYTHON required")
	}
	cfg := testutil.MainlandConfig()
	cfg.Models = cfg.Models[3:]
	cfg.Models[0].MaxOutputTokens = 32
	cfg.Models[0].MaxInputTokens = 512
	cfg.Models[0].API = "messages"
	cfg.Models[0].CachePricing = &provider.CachePricing{ReadPerMillion: 500000}
	f := embeddedProductFixture(t, cfg, "/anthropic/v1/messages", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model     string            `json:"model"`
			MaxTokens int               `json:"max_tokens"`
			Stream    bool              `json:"stream"`
			Messages  []json.RawMessage `json:"messages"`
			System    json.RawMessage   `json:"system"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Model != "fixture-model" || req.MaxTokens != 32 || req.Stream || len(req.Messages) != 3 || len(req.System) == 0 {
			t.Error("SDK native conversation changed")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"msg_sdk","type":"message","role":"assistant","model":"fixture-model","content":[{"type":"text","text":"synthetic"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{ "input_tokens":1, "output_tokens":2, "cache_read_input_tokens":2, "cache_creation_input_tokens":0 }}`)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, python, "-c", `
import os, sys, re, anthropic
sys.excepthook = lambda typ,value,tb: sys.stderr.write("SDK_DIAGNOSTIC:"+typ.__name__+":"+str(getattr(value,"status_code",0))+":"+str(tb.tb_lineno)+":"+re.sub("[^a-z_]", "", str(getattr(value,"body",{}).get("error",{}).get("message","")))[:80]+"\n")
assert anthropic.__version__ == "2.6.0"
client = anthropic.Anthropic(api_key=os.environ["AIGW_SDK_KEY"], base_url=os.environ["AIGW_SDK_URL"], max_retries=0, default_headers={"anthropic-version":"2023-06-01"})
args = dict(model="deepseek/fixture", max_tokens=32, system="synthetic system", messages=[{"role":"user","content":"synthetic first"},{"role":"assistant","content":"synthetic prior"},{"role":"user","content":[{"type":"text","text":"synthetic final"}]}])
response = client.messages.create(**args)
assert response.content[0].text == "synthetic" and response.stop_reason == "end_turn"
assert response.usage.input_tokens == 1 and response.usage.output_tokens == 2
assert response.usage.cache_read_input_tokens == 2 and response.usage.cache_creation_input_tokens == 0
for extra in ({"stream":True}, {"tools":[{"name":"synthetic","input_schema":{"type":"object"}}]}, {"thinking":{"type":"enabled","budget_tokens":1024}}):
    try:
        client.messages.create(**args, **extra)
        raise AssertionError("unreviewed feature accepted")
    except anthropic.APIStatusError as err:
        assert err.status_code == 400
client.close()
`)
	command.Env = []string{"AIGW_SDK_KEY=" + f.key, "AIGW_SDK_URL=" + f.server.URL}
	if output, err := command.CombinedOutput(); err != nil {
		if diagnostic := regexp.MustCompile(`SDK_DIAGNOSTIC:[A-Za-z_]+:[0-9]+:[0-9]+:[a-z_]*`).Find(output); len(diagnostic) > 0 {
			t.Log(string(diagnostic))
		}
		t.Fatal("native Messages SDK contract failed; output suppressed")
	}
	var receipts int
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM engine_completion_receipts WHERE actual=11`).Scan(&receipts); err != nil || receipts != 1 || f.calls.Load() != 1 {
		t.Fatal("SDK rejected requests dispatched or settlement differs", err)
	}
}
