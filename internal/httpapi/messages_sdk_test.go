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

func TestMessagesPythonSDK(t *testing.T) {
	python := os.Getenv("AIGW_MESSAGES_SDK_PYTHON")
	if python == "" {
		t.Fatal("AIGW_MESSAGES_SDK_PYTHON required for Messages sdk_e2e")
	}
	f, calls := responsesFixture(t, responsesFixtureOptions{cacheEvidence: true})
	_, _, key := providerProject(f)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, python, "-c", `
import os, sys, anthropic
sys.excepthook = lambda typ, value, tb: sys.stderr.write("SDK_DIAGNOSTIC:" + typ.__name__ + ":" + str(getattr(value, "name", "unknown")) + "\n")
assert anthropic.__version__ == "2.6.0"
client = anthropic.Anthropic(api_key=os.environ["AIGW_SDK_KEY"], base_url=os.environ["AIGW_SDK_URL"], max_retries=0, default_headers={"anthropic-version":"2023-06-01"})
response = client.messages.create(model="zhipu/fixture", max_tokens=8, messages=[{"role":"user", "content":"synthetic SDK input"}])
assert response.type == "message" and response.stop_reason == "end_turn"
assert response.content[0].type == "text" and response.content[0].text == "synthetic response text"
assert response.usage.input_tokens == 2 and response.usage.output_tokens == 3
assert response.usage.cache_read_input_tokens == 4 and response.usage.cache_creation_input_tokens == 5
assert response.stop_sequence is None and response.id.startswith("msg_")
try:
    client.messages.create(model="zhipu/fixture", max_tokens=8, messages=[{"role":"user", "content":"synthetic"}], stream=True)
except anthropic.BadRequestError as error:
    assert error.body["error"]["type"] == "invalid_request_error"
else:
    raise AssertionError("unreviewed streaming accepted")
client.close()
`)
	command.Env = []string{"AIGW_SDK_KEY=" + key.Secret, "AIGW_SDK_URL=" + f.server.URL}
	if output, err := command.CombinedOutput(); err != nil {
		if detail := regexp.MustCompile(`SDK_DIAGNOSTIC:[A-Za-z_][A-Za-z_0-9]*:[A-Za-z_][A-Za-z_0-9]*`).Find(output); len(detail) > 0 {
			t.Log(string(detail))
		}
		t.Fatal("Messages SDK contract failed; output suppressed")
	}
	if calls.Load() != 1 {
		t.Fatal("SDK replayed or unreviewed request dispatched")
	}
}
