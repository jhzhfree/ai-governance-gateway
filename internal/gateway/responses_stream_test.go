package gateway

import (
	"encoding/json"
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
