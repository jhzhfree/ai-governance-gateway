package bifrost

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/jhzhfree/ai-governance-gateway/modules/engine/execution"
)

const contentFrame = `{"choices":[{"index":0,"delta":{"content":"synthetic"},"finish_reason":null}]}`
const finishFrame = `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
const usageFrame = `{"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`

func sse(raw string) string { return "data: " + raw + "\n\n" }
func TestStrictReceiverRejectsUnobservedStreamCorruption(t *testing.T) {
	valid := sse(contentFrame) + sse(finishFrame) + sse(usageFrame) + sse("[DONE]")
	cases := map[string]string{
		"malformed JSON dropped by SDK": sse(contentFrame) + sse(`{"bad":`) + sse(finishFrame) + sse(usageFrame) + sse("[DONE]"),
		"oversized nonforwarding frame": sse(`{"choices":[],"hidden":"`+strings.Repeat("x", (1<<20)+1)+`"}`) + valid,
		"oversized heartbeat":           ":" + strings.Repeat("x", maxStreamFrame) + "\n\n" + valid,
		"total heartbeat limit":         strings.Repeat(":"+strings.Repeat("x", 60000)+"\n\n", 280) + valid,
		"usage then invalid JSON":       sse(contentFrame) + sse(finishFrame) + sse(usageFrame) + sse("invalid") + sse("[DONE]"),
		"usage then EOF":                sse(contentFrame) + sse(finishFrame) + sse(usageFrame),
		"missing terminal delimiter":    sse(contentFrame) + sse(finishFrame) + sse(usageFrame) + "data: [DONE]\n",
		"duplicate usage":               sse(contentFrame) + sse(finishFrame) + sse(usageFrame) + sse(usageFrame) + sse("[DONE]"),
		"missing real finish":           sse(contentFrame) + sse("[DONE]"),
		"invalid UTF8":                  sse(strings.Replace(contentFrame, "synthetic", string([]byte{0xff}), 1)) + valid,
		"unknown SSE field":             "ignored: bad\n\n" + valid,
		"error event":                   "event: error\ndata: {}\n\n" + valid,
		"duplicate choice field":        sse(strings.Replace(contentFrame, `"index":0`, `"index":0,"index":0`, 1)) + valid,
		"content after finish":          sse(finishFrame) + valid,
	}
	for name, wire := range cases {
		t.Run(name, func(t *testing.T) {
			usage := 0
			err := receiveStream(context.Background(), strings.NewReader(wire), execution.Request{RequestID: "r", AttemptID: "a"}, 32, func(_ context.Context, e execution.Event) error {
				if e.Usage != nil {
					usage++
				}
				return nil
			})
			if !errors.Is(err, execution.ErrUpstream) || usage != 0 {
				t.Fatalf("corruption accepted: error=%v authoritative usage=%d", err, usage)
			}
		})
	}
}
func TestStrictReceiverValidFramingAndTerminalEvidence(t *testing.T) {
	for _, wire := range []string{
		sse(contentFrame) + sse(finishFrame) + sse(usageFrame) + sse("[DONE]"),
		strings.ReplaceAll(": heartbeat\n\nevent: message\n"+sse(contentFrame)+sse(finishFrame)+sse(usageFrame)+sse("[DONE]"), "\n", "\r\n"),
		"data: {\ndata: \"choices\":[{\"index\":0,\"delta\":{\"content\":\"synthetic\"},\"finish_reason\":null}]}\n\n" + sse(finishFrame) + sse(usageFrame) + sse("[DONE]"),
	} {
		reader := bytes.NewReader([]byte(wire))
		calls, usage := 0, 0
		err := receiveStream(context.Background(), reader, execution.Request{RequestID: "r", AttemptID: "a"}, 32, func(_ context.Context, e execution.Event) error {
			calls++
			if e.Usage != nil {
				usage++
				if e.Usage.TotalTokens != 5 {
					t.Error("wrong usage")
				}
			}
			if e.RequestID != "r" || e.AttemptID != "a" || e.UpstreamStatus != 200 {
				t.Error("correlation lost")
			}
			return nil
		})
		if err != nil || calls != 3 || usage != 1 {
			t.Fatalf("valid stream rejected: %v calls=%d usage=%d", err, calls, usage)
		}
	}
}
func TestStrictReceiverReadFailureAndConsumerBackpressure(t *testing.T) {
	failed := errors.New("synthetic consumer stopped")
	err := receiveStream(context.Background(), strings.NewReader(sse(contentFrame)), execution.Request{}, 32, func(context.Context, execution.Event) error { return failed })
	if !errors.Is(err, failed) {
		t.Fatal("consumer error lost")
	}
	err = receiveStream(context.Background(), io.MultiReader(strings.NewReader(sse(finishFrame)+sse(usageFrame)), failingReader{}), execution.Request{}, 32, func(_ context.Context, e execution.Event) error {
		if e.Usage != nil {
			t.Error("incomplete usage emitted")
		}
		return nil
	})
	if !errors.Is(err, execution.ErrUpstream) {
		t.Fatal("read failure not sanitized")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("synthetic-private-transport-error")
}
