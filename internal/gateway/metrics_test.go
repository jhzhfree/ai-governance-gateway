package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jhzhfree/ai-governance-gateway/internal/observe"
)

func TestIngressMetricsIncludePreAuthorizationFailures(t *testing.T) {
	monitor := observe.New()
	h := &Handler{Monitor: monitor}
	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest("GET", "/v1/chat/completions?private=query", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatal("unexpected response")
	}
	token := strings.Repeat("x", 32)
	metrics, err := monitor.MetricsHandler(token)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	out := httptest.NewRecorder()
	metrics.ServeHTTP(out, req)
	if !strings.Contains(out.Body.String(), `aigw_gateway_requests_total{route="chat",status_class="4xx"} 1`) {
		t.Fatal("ingress missing")
	}
	if monitor.Snapshot("", "").Tracked {
		t.Fatal("unauthorized request assigned a project")
	}
	// ResponseController must continue to discover SSE flush through the wrapper.
	wrapped := &measuredWriter{ResponseWriter: response}
	if err := http.NewResponseController(wrapped).Flush(); err != nil || !response.Flushed {
		t.Fatal("stream flush broken")
	}
}
