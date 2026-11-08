package gatewaybridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
)

func TestEmbeddedInputAdmissionBeforeReservation(t *testing.T) {
	f := embeddedResponsesFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("oversized request dispatched") }))
	for _, path := range []string{"/v1/chat/completions", "/v1/responses"} {
		for _, messages := range [][]provider.Message{
			{{Role: "user", Content: strings.Repeat("你", 50)}},
			{{Role: "user", Content: "synthetic"}, {Role: "assistant", Content: "synthetic"}, {Role: "user", Content: "synthetic"}},
		} {
			var body any = map[string]any{"model": "deepseek/fixture", "messages": messages, "max_tokens": 32}
			if path == "/v1/responses" {
				body = map[string]any{"model": "deepseek/fixture", "input": messages, "max_output_tokens": 32, "store": false}
			}
			raw, _ := json.Marshal(body)
			r, _ := http.NewRequest("POST", f.server.URL+path, strings.NewReader(string(raw)))
			r.Header.Set("Authorization", "Bearer "+f.key)
			resp, err := http.DefaultClient.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 400 || !strings.Contains(string(data), "estimated_input_limit_exceeded") {
				t.Fatalf("wrong admission status=%d body=%s", resp.StatusCode, data)
			}
		}
	}
	var reservations, bindings, attempts int
	if err := f.db.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM budget_reservations),(SELECT count(*) FROM engine_execution_bindings),(SELECT count(*) FROM inference_attempts)`).Scan(&reservations, &bindings, &attempts); err != nil {
		t.Fatal(err)
	}
	if reservations != 0 || bindings != 0 || attempts != 0 || f.calls.Load() != 0 {
		t.Fatal("admission caused dispatch or durable charges")
	}
	route, _ := f.managed.Find("deepseek/fixture")
	req := provider.Request{Model: "deepseek/fixture", MaxTokens: 32, Messages: []provider.Message{{Role: "user", Content: strings.Repeat("你", 50)}}}
	if !f.managed.SupportsPublicProtocol(route, req, "/v1/responses") {
		t.Fatal("input bound mislabeled protocol capability")
	}
	if response, err := f.managed.Open(context.Background(), route, req, "request_synthetic", "attempt_synthetic"); err == nil || response != nil {
		t.Fatal("direct open bypassed admission")
	}
}
