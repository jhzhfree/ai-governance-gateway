//go:build sdk_e2e

package gatewaybridge

import (
	"context"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
	"io"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestEmbeddedEmbeddingsPythonSDK(t *testing.T) {
	python := os.Getenv("AIGW_SDK_PYTHON")
	if python == "" {
		t.Fatal("AIGW_SDK_PYTHON required")
	}
	cfg := testutil.MainlandConfig()
	cfg.Models = cfg.Models[:1]
	cfg.Models[0].API = "embeddings"
	cfg.Models[0].MaxOutputTokens = 0
	cfg.Models[0].OutputPerMillion = 0
	f := embeddedProductFixture(t, cfg, "/compatible-mode/v1/embeddings", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list","model":"fixture-model","data":[{"object":"embedding","index":0,"embedding":[0.1,-0.2]}],"usage":{ "prompt_tokens":3, "total_tokens":3 }}`)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, python, "-c", `
import os, openai
assert openai.__version__ == "3.46.0"
client = openai.OpenAI(api_key=os.environ["AIGW_SDK_KEY"], base_url=os.environ["AIGW_SDK_URL"], max_retries=0)
for value in ("synthetic", ["synthetic"]):
    response = client.embeddings.create(model="aliyun/fixture", input=value, encoding_format="float")
    assert response.object == "list" and response.model == "aliyun/fixture"
    assert response.data[0].embedding == [0.1,-0.2]
    assert response.usage.prompt_tokens == response.usage.total_tokens == 3
try:
    client.embeddings.create(model="aliyun/fixture", input="synthetic", encoding_format="base64")
except openai.BadRequestError:
    pass
else:
    raise AssertionError("unreviewed vector encoding accepted")
client.close()
`)
	command.Env = []string{"AIGW_SDK_KEY=" + f.key, "AIGW_SDK_URL=" + f.server.URL + "/v1"}
	if _, err := command.CombinedOutput(); err != nil {
		t.Fatal("embedded embedding SDK contract failed; fixture output suppressed")
	}
	var events, receipts, ledger int
	err := f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM engine_completion_events WHERE raw_usage=convert_to('{ "prompt_tokens":3, "total_tokens":3 }','UTF8') AND input_tokens=3 AND output_tokens=0 AND reconciled),(SELECT count(*) FROM engine_completion_receipts WHERE actual=6),(SELECT count(*) FROM budget_reservations WHERE actual=6 AND state='settled')`).Scan(&events, &receipts, &ledger)
	if err != nil || events != 2 || receipts != 2 || ledger != 2 || f.calls.Load() != 2 {
		t.Fatal("SDK raw evidence or charges differ", err)
	}
}
