//go:build sdk_e2e

package httpapi_test

import (
	"context"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
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

func TestMessagesPythonSDKStreaming(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{true: "missing-proof", false: "complete"}[missing], func(t *testing.T) {
			python := os.Getenv("AIGW_MESSAGES_SDK_PYTHON")
			if python == "" {
				t.Fatal("AIGW_MESSAGES_SDK_PYTHON required")
			}
			f, calls := responsesFixture(t, responsesFixtureOptions{messagesStream: true, cacheEvidence: true, omitProof: missing})
			o, p, key := providerProject(f)
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, python, "-c", `
import os,sys,anthropic
sys.excepthook = lambda typ,value,tb: sys.stderr.write("SDK_DIAGNOSTIC:"+typ.__name__+"\n")
client=anthropic.Anthropic(api_key=os.environ["AIGW_SDK_KEY"],base_url=os.environ["AIGW_SDK_URL"],max_retries=0,default_headers={"anthropic-version":"2023-06-01"})
kwargs=dict(model="zhipu/fixture",max_tokens=8,messages=[{"role":"user","content":"synthetic"}])
if os.environ["MISSING"]=="1":
    try:
        with client.messages.stream(**kwargs) as stream:
            stream.get_final_message()
    except anthropic.APIError:
        pass
    else:
        raise AssertionError("unproven completion accepted")
else:
    with client.messages.create(**kwargs,stream=True) as stream:
        events=list(stream)
    assert events[0].type=="message_start" and events[-1].type=="message_stop"
    assert events[0].message.usage.input_tokens==2 and events[0].message.usage.output_tokens==1
    with client.messages.stream(**kwargs) as stream:
        text="".join(stream.text_stream)
        final=stream.get_final_message()
    assert text=="synthetic response text" and final.content[0].text==text
    assert final.stop_reason=="end_turn" and final.stop_sequence is None
    assert final.usage.input_tokens==2 and final.usage.output_tokens==3
    assert final.usage.cache_read_input_tokens==4 and final.usage.cache_creation_input_tokens==5
client.close()
`)
			command.Env = []string{"AIGW_SDK_KEY=" + key.Secret, "AIGW_SDK_URL=" + f.server.URL, "MISSING=" + map[bool]string{true: "1", false: "0"}[missing]}
			if output, err := command.CombinedOutput(); err != nil {
				if detail := regexp.MustCompile(`SDK_DIAGNOSTIC:[A-Za-z_]+`).Find(output); len(detail) > 0 {
					t.Log(string(detail))
				}
				t.Fatal("Messages streaming SDK failed; output suppressed")
			}
			usage := parse[control.Usage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/usage", nil, 200))
			if missing {
				if calls.Load() != 1 || usage.Held != 416 || usage.Spent != 0 {
					t.Fatal("SDK failure replayed or lost hold")
				}
			} else if calls.Load() != 2 || usage.Held != 0 || usage.Spent != 66 {
				t.Fatal("SDK stream accounting mismatch")
			}
		})
	}
}
