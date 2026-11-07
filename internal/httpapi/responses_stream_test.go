package httpapi_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/jhzhfree/ai-governance-gateway/internal/control"
)

func responsesEvents(t *testing.T, body string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event map[string]any
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) != nil {
			t.Fatal("invalid Responses event JSON")
		}
		if event["sequence_number"] != float64(len(events)) {
			t.Fatal("non-monotonic Responses event sequence")
		}
		events = append(events, event)
	}
	return events
}

func TestResponsesStreamCompletionRequiresEvidenceAndAudit(t *testing.T) {
	for _, mode := range []string{"complete", "missing-proof", "audit-failure", "checkpoint-failure", "length"} {
		t.Run(mode, func(t *testing.T) {
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			f, calls := responsesFixture(t, responsesFixtureOptions{release: release, omitProof: mode == "missing-proof", extraDelta: mode == "checkpoint-failure", finishReason: map[bool]string{true: "length", false: "stop"}[mode == "length"]})
			t.Cleanup(unblock)
			o, p, key := providerProject(f)
			enableContent(f, o, p, 65536)
			if mode == "audit-failure" {
				if _, err := f.pool.Exec(context.Background(), `ALTER TABLE content_audit_payloads ADD CONSTRAINT reject_response CHECK(direction<>'response') NOT VALID`); err != nil {
					t.Fatal(err)
				}
			}
			payload := map[string]any{"model": "zhipu/fixture", "input": "synthetic stream input", "instructions": "synthetic instructions", "max_output_tokens": 16, "store": false, "stream": true}
			encoded, _ := json.Marshal(payload)
			request, _ := http.NewRequest("POST", f.server.URL+"/v1/responses", bytes.NewReader(encoded))
			request.Header.Set("Authorization", "Bearer "+key.Secret)
			request.Header.Set("Content-Type", "application/json")
			response, err := f.server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			reader := bufio.NewReader(response.Body)
			var early strings.Builder
			for range 32 {
				line, err := reader.ReadString('\n')
				if err != nil {
					t.Fatal("response delta did not arrive before supplier completion")
				}
				early.WriteString(line)
				if strings.HasPrefix(line, "data: ") && strings.Contains(line, `"type":"response.output_text.delta"`) {
					break
				}
			}
			if !strings.Contains(early.String(), "synthetic response text") || response.StatusCode != 200 {
				t.Fatal("live text delta missing")
			}
			// Four initialization events share one durable checkpoint; the first
			// live text delta has its own checkpoint before it reaches the client.
			var parts int
			if err := f.pool.QueryRow(context.Background(), `SELECT parts FROM content_audit_checkpoints`).Scan(&parts); err != nil || parts != 2 {
				t.Fatal("initial metadata and live delta checkpoint boundaries changed", parts, err)
			}
			usage := parse[control.Usage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/usage", nil, 200))
			if usage.Spent != 0 || usage.Held != 320 {
				t.Fatal("stream settled before original completion")
			}
			if mode == "checkpoint-failure" {
				if _, err := f.pool.Exec(context.Background(), `ALTER TABLE content_audit_checkpoint_parts ADD CONSTRAINT reject_future CHECK(false) NOT VALID`); err != nil {
					t.Fatal(err)
				}
			}
			unblock()
			rest, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			body := early.String() + string(rest)
			events := responsesEvents(t, body)
			if len(events) == 0 {
				t.Fatal("empty stream")
			}
			completed := strings.Contains(body, `"type":"response.completed"`)
			if completed != (mode == "complete") || strings.Contains(body, "[DONE]") {
				t.Fatal("unproven completion or Chat sentinel escaped")
			}
			if mode == "complete" && events[len(events)-1]["type"] != "response.completed" {
				t.Fatal("terminal event order invalid")
			}
			if mode == "length" && (events[len(events)-1]["type"] != "response.incomplete" || !strings.Contains(body, `"reason":"max_output_tokens"`)) {
				t.Fatal("length termination lost")
			}
			if mode == "audit-failure" && !strings.Contains(body, "content_audit_unavailable") {
				t.Fatal("audit failure was not explicit")
			}
			usage = parse[control.Usage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/usage", nil, 200))
			if mode == "missing-proof" || mode == "checkpoint-failure" {
				if usage.Spent != 0 || usage.Held != 320 {
					t.Fatal("missing completion proof refunded or settled")
				}
			} else if usage.Spent != 16 || usage.Held != 0 {
				t.Fatal("known supplier cost lost")
			}
			if mode == "checkpoint-failure" && strings.Contains(body, "uncommitted-second") {
				t.Fatal("uncommitted delta escaped audit checkpoint")
			}
			if calls.Load() != 1 {
				t.Fatal("stream replayed")
			}
		})
	}
}
