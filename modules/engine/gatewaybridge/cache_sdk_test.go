//go:build sdk_e2e

package gatewaybridge

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestEmbeddedCachePythonSDK(t *testing.T) {
	python := os.Getenv("AIGW_SDK_PYTHON")
	if python == "" {
		t.Fatal("AIGW_SDK_PYTHON required")
	}
	cfg := testutil.MainlandConfig()
	cfg.Models = cfg.Models[3:]
	cfg.Models[0].MaxOutputTokens = 32
	cfg.Models[0].CachePricing = &provider.CachePricing{ReadPerMillion: 500000}
	usage := `{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5,"prompt_cache_hit_tokens":2,"prompt_cache_miss_tokens":1}`
	f := embeddedProductFixture(t, cfg, "/chat/completions", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Stream bool `json:"stream"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("invalid fixture body")
			return
		}
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: {\"id\":\"cache-sdk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"synthetic\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"cache-sdk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: {\"id\":\"cache-sdk\",\"choices\":[],\"usage\":%s}\n\ndata: [DONE]\n\n", usage)
		} else {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":"cache-sdk","choices":[{"index":0,"message":{"role":"assistant","content":"synthetic"},"finish_reason":"stop"}],"usage":%s}`, usage)
		}
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, python, "-c", `
import os, openai
assert openai.__version__ == "3.46.0"
client = openai.OpenAI(api_key=os.environ["AIGW_SDK_KEY"], base_url=os.environ["AIGW_SDK_URL"], max_retries=0)
for stream in (False,True):
    response = client.chat.completions.create(model="deepseek/fixture",max_tokens=32,messages=[{"role":"user","content":"synthetic"}],stream=stream)
    usage = response.usage if not stream else next(chunk.usage for chunk in response if chunk.usage)
    assert usage.prompt_tokens == 3 and usage.completion_tokens == 2
    assert usage.prompt_tokens_details.cached_tokens == 2
    assert "cached_creation_tokens" not in usage.prompt_tokens_details.model_dump(exclude_none=True)
    response = client.responses.create(model="deepseek/fixture",max_output_tokens=32,input="synthetic",store=False,stream=stream)
    usage = response.usage if not stream else next(event.response.usage for event in response if event.type=="response.completed")
    assert usage.input_tokens == 3 and usage.output_tokens == 2
    assert usage.input_tokens_details.cached_tokens == 2
client.close()
`)
	command.Env = []string{"AIGW_SDK_KEY=" + f.key, "AIGW_SDK_URL=" + f.server.URL + "/v1"}
	if output, err := command.CombinedOutput(); err != nil {
		t.Log(string(output))
		t.Fatal("cache SDK contract failed")
	}
	var receipts int
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM engine_completion_receipts WHERE actual=11`).Scan(&receipts); err != nil || receipts != 4 || f.calls.Load() != 4 {
		t.Fatal("SDK calls did not settle once", err)
	}
}
