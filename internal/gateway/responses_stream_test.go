package gateway

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
)

func TestResponsesStreamBoundsDoNotAcknowledgeSuccess(t *testing.T) {
	output := httptest.NewRecorder()
	output.Header().Set("Content-Type", "text/event-stream")
	writer := &responsesStreamWriter{ResponseWriter: output, request: provider.Request{Model: "reviewed", MaxTokens: 16}, id: "bounded"}
	chunk := strings.Repeat("x", 32<<10)
	delta, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": chunk}, "finish_reason": nil}}})
	frame := []byte("data: " + string(delta) + "\n\n")
	for range 32 {
		if _, err := writer.Write(frame); err != nil {
			t.Fatal(err)
		}
	}
	before := output.Body.Len()
	if _, err := writer.Write(frame); err == nil {
		t.Fatal("unbounded aggregate output accepted")
	}
	if output.Body.Len() != before || strings.Contains(output.Body.String(), "response.completed") {
		t.Fatal("over-limit output or completion escaped")
	}
	if _, err := writer.Write([]byte("data: [DONE]\n\n")); err == nil {
		t.Fatal("missing terminal usage accepted")
	}
	if _, err := writer.Write([]byte("invalid frame")); err == nil {
		t.Fatal("invalid frame accepted")
	}
}

// One Write is one durable audit checkpoint when the audit writer is installed.
// Metadata may share that checkpoint, but content deltas must remain independent.
type responsesWriteProbe struct {
	*httptest.ResponseRecorder
	writes [][]byte
	fail   bool
}

func (p *responsesWriteProbe) Write(data []byte) (int, error) {
	if p.fail {
		return 0, io.ErrClosedPipe
	}
	p.writes = append(p.writes, append([]byte(nil), data...))
	return p.ResponseRecorder.Write(data)
}
func TestResponsesInitialCheckpointBatch(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "checkpoint-failure"}[failure], func(t *testing.T) {
			probe := &responsesWriteProbe{ResponseRecorder: httptest.NewRecorder(), fail: failure}
			probe.Header().Set("Content-Type", "text/event-stream")
			writer := &responsesStreamWriter{ResponseWriter: probe, request: provider.Request{Model: "reviewed", MaxTokens: 16}, id: "initial"}
			data := []byte(`data: {"choices":[{"index":0,"delta":{"content":"first"},"finish_reason":null}]}` + "\n\n")
			_, err := writer.Write(data)
			if failure {
				if err == nil || writer.started || len(probe.writes) != 0 || probe.Body.Len() != 0 {
					t.Fatal("failed metadata checkpoint released content")
				}
				return
			}
			if err != nil || len(probe.writes) != 2 {
				t.Fatal("initial metadata must use one write, followed by independent delta", err)
			}
			if strings.Count(string(probe.writes[0]), "event: ") != 4 || strings.Contains(string(probe.writes[0]), "output_text.delta") || !strings.Contains(string(probe.writes[1]), "output_text.delta") {
				t.Fatal("incorrect checkpoint boundaries")
			}
			sequence := 0
			for _, line := range strings.Split(probe.Body.String(), "\n") {
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				var event struct {
					Sequence int `json:"sequence_number"`
				}
				if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) != nil || event.Sequence != sequence {
					t.Fatal("event sequence changed")
				}
				sequence++
			}
			if sequence != 5 {
				t.Fatal("events lost")
			}
			if _, err := writer.Write(data); err != nil || len(probe.writes) != 3 {
				t.Fatal("subsequent delta was delayed or initialization repeated", err)
			}
		})
	}
}
func TestResponsesInitialBatchBound(t *testing.T) {
	probe := &responsesWriteProbe{ResponseRecorder: httptest.NewRecorder()}
	writer := &responsesStreamWriter{ResponseWriter: probe}
	events := make([]responsesEvent, 4)
	for i := range events {
		events[i] = responsesEvent{"response.created", map[string]any{"synthetic": strings.Repeat("x", 20<<10)}}
	}
	if writer.emitInitial(events) == nil || probe.Body.Len() != 0 || writer.sequence != 0 {
		t.Fatal("oversized batch partially emitted")
	}
}
