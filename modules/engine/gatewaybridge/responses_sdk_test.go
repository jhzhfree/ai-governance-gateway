//go:build sdk_e2e

package gatewaybridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestEmbeddedResponsesPythonSDK(t *testing.T) {
	python := os.Getenv("AIGW_SDK_PYTHON")
	if python == "" {
		t.Fatal("AIGW_SDK_PYTHON is required for sdk_e2e")
	}
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing_usage_%t", missing), func(t *testing.T) {
			f := embeddedResponsesFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model     string            `json:"model"`
					Stream    bool              `json:"stream"`
					MaxTokens int               `json:"max_tokens"`
					Messages  []json.RawMessage `json:"messages"`
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "fixture-model" || body.MaxTokens != 32 || len(body.Messages) < 1 {
					t.Error("invalid converted supplier request")
					w.WriteHeader(400)
					return
				}
				if body.Stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"synthetic SDK reply\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
					if !missing {
						fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n")
					}
					fmt.Fprint(w, "data: [DONE]\n\n")
				} else {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"synthetic SDK reply"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
				}
			}))
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, python, "-c", `
import os
import openai
assert openai.__version__ == "3.46.0"
client = openai.OpenAI(api_key=os.environ["AIGW_SDK_KEY"], base_url=os.environ["AIGW_SDK_URL"], max_retries=0)
args = dict(model="deepseek/fixture", input="synthetic SDK input", instructions="synthetic instructions", max_output_tokens=32, store=False)
if os.environ["AIGW_SDK_MISSING"] == "true":
    with client.responses.stream(**args) as stream:
        try:
            stream.get_final_response()
        except RuntimeError:
            pass
        else:
            raise AssertionError("missing original usage accepted")
else:
    response = client.responses.create(**args)
    assert response.object == "response" and response.status == "completed" and response.store is False
    assert response.output_text == "synthetic SDK reply"
    assert (response.usage.input_tokens, response.usage.output_tokens, response.usage.total_tokens) == (3, 2, 5)
    args["input"] = [{"type":"message","role":"user","content":[{"type":"input_text","text":"synthetic SDK input"}]}]
    events = list(client.responses.create(**args, stream=True))
    assert "".join(e.delta for e in events if e.type == "response.output_text.delta") == "synthetic SDK reply"
    assert events[-1].type == "response.completed" and events[-1].response.usage.total_tokens == 5
    assert [e.sequence_number for e in events] == list(range(len(events)))
    with client.responses.stream(**args) as stream:
        assert "".join(e.delta for e in stream if e.type == "response.output_text.delta") == "synthetic SDK reply"
        final = stream.get_final_response()
        assert final.output_text == "synthetic SDK reply" and final.usage.total_tokens == 5
    try:
        client.responses.create(**args, previous_response_id="resp_synthetic")
    except openai.BadRequestError:
        pass
    else:
        raise AssertionError("server state accepted")
client.close()
`)
			command.Env = []string{"AIGW_SDK_KEY=" + f.key, "AIGW_SDK_URL=" + f.server.URL + "/v1", fmt.Sprintf("AIGW_SDK_MISSING=%t", missing)}
			if _, err := command.CombinedOutput(); err != nil {
				t.Fatal("embedded SDK contract failed; fixture output suppressed")
			}
			if !missing {
				f.assertPosted(t, 3)
				return
			}
			var holds, events, receipts int
			if err := f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM budget_reservations WHERE state='uncertain' AND actual IS NULL),(SELECT count(*) FROM engine_completion_events),(SELECT count(*) FROM engine_completion_receipts)`).Scan(&holds, &events, &receipts); err != nil || holds != 1 || events != 0 || receipts != 0 || f.calls.Load() != 1 {
				t.Fatal("missing evidence lost hold or retried", err)
			}
		})
	}
}
