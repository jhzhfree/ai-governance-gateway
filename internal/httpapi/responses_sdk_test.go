//go:build sdk_e2e

package httpapi_test

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"testing"
	"time"
)

// Explicit opt-in gate: no production credentials or supplier network calls.
// Install the pinned test-only requirements in an isolated Python environment.
func TestResponsesPythonSDK(t *testing.T) {
	python := os.Getenv("AIGW_SDK_PYTHON")
	if python == "" {
		t.Fatal("AIGW_SDK_PYTHON is required for sdk_e2e")
	}
	f, calls := responsesFixture(t)
	_, _, key := providerProject(f)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, python, "-c", `
import os
import openai
import sys
sys.excepthook = lambda typ, value, tb: sys.stderr.write("SDK_DIAGNOSTIC:" + typ.__name__ + ":" + str(getattr(value, "name", "unknown")) + "\n")
assert openai.__version__ == "3.46.0"
client = openai.OpenAI(api_key=os.environ["AIGW_SDK_KEY"], base_url=os.environ["AIGW_SDK_URL"], max_retries=0)
response = client.responses.create(model="zhipu/fixture", input="synthetic SDK input", instructions="synthetic SDK instructions", max_output_tokens=16, store=False)
assert response.object == "response" and response.status == "completed" and response.store is False
assert response.output_text == "synthetic response text"
assert response.usage.input_tokens == 2 and response.usage.output_tokens == 3 and response.usage.total_tokens == 5
assert response.id.startswith("resp_")
events = list(client.responses.create(model="zhipu/fixture", input="synthetic SDK input", instructions="synthetic SDK instructions", max_output_tokens=16, store=False, stream=True))
assert "".join(event.delta for event in events if event.type == "response.output_text.delta") == "synthetic response text"
assert events[-1].type == "response.completed"
assert events[-1].response.output_text == "synthetic response text"
assert events[-1].response.usage.total_tokens == 5
assert [event.sequence_number for event in events] == list(range(len(events)))
with client.responses.stream(model="zhipu/fixture", input="synthetic SDK input", instructions="synthetic SDK instructions", max_output_tokens=16, store=False) as stream:
    assert "".join(event.delta for event in stream if event.type == "response.output_text.delta") == "synthetic response text"
    final = stream.get_final_response()
    assert final.output_text == "synthetic response text" and final.usage.total_tokens == 5
client.close()
`)
	command.Env = []string{"AIGW_SDK_KEY=" + key.Secret, "AIGW_SDK_URL=" + f.server.URL + "/v1"}
	if output, err := command.CombinedOutput(); err != nil {
		if detail := regexp.MustCompile(`SDK_DIAGNOSTIC:[A-Za-z_][A-Za-z_0-9]*:[A-Za-z_][A-Za-z_0-9]*`).Find(output); len(detail) > 0 {
			t.Log(string(detail))
		}
		t.Fatal("Python SDK contract failed; output suppressed to protect fixture credentials")
	}
	if calls.Load() != 3 {
		t.Fatal("SDK request replayed")
	}
}

func TestResponsesPythonSDKRejectsMissingProof(t *testing.T) {
	python := os.Getenv("AIGW_SDK_PYTHON")
	if python == "" {
		t.Fatal("AIGW_SDK_PYTHON is required for sdk_e2e")
	}
	f, calls := responsesFixture(t, responsesFixtureOptions{omitProof: true})
	_, _, key := providerProject(f)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, python, "-c", `
import os
import openai
assert openai.__version__ == "3.46.0"
client = openai.OpenAI(api_key=os.environ["AIGW_SDK_KEY"], base_url=os.environ["AIGW_SDK_URL"], max_retries=0)
with client.responses.stream(model="zhipu/fixture", input="synthetic input", instructions="synthetic instructions", max_output_tokens=16, store=False) as stream:
    try:
        stream.get_final_response()
    except RuntimeError:
        pass
    else:
        raise AssertionError("unverified success accepted")
client.close()
`)
	command.Env = []string{"AIGW_SDK_KEY=" + key.Secret, "AIGW_SDK_URL=" + f.server.URL + "/v1"}
	if _, err := command.CombinedOutput(); err != nil {
		t.Fatal("SDK missing-proof contract failed; output suppressed")
	}
	if calls.Load() != 1 {
		t.Fatal("SDK retried unknown outcome")
	}
}
