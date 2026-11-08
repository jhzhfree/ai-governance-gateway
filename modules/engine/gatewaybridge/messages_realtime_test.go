package gatewaybridge

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func TestNativeMessagesDeliversTextBeforeCompletion(t *testing.T) {
	for _, protocol := range []string{"messages", "chat", "responses", "messages-audit-failure", "messages-checkpoint-failure"} {
		t.Run(protocol, func(t *testing.T) {
			cfg := testutil.MainlandConfig()
			cfg.Models = cfg.Models[3:]
			cfg.Models[0].API = "messages"
			cfg.Models[0].MaxOutputTokens = 32
			cfg.Models[0].AllowMessagesStream = true
			cfg.Models[0].CachePricing = &provider.CachePricing{ReadPerMillion: 500000}
			initial := `{"input_tokens":1,"output_tokens":0,"cache_read_input_tokens":2,"cache_creation_input_tokens":0}`
			final := `{"input_tokens":1,"output_tokens":2,"cache_read_input_tokens":2,"cache_creation_input_tokens":0}`
			delimiter := "event: message_stop"
			if protocol == "messages-checkpoint-failure" {
				delimiter = "event: content_block_stop"
			}
			prefix, tail, _ := strings.Cut(testutil.EmbeddedMessagesStream(initial, final, "synthetic realtime text"), delimiter)
			release := make(chan struct{})
			var once sync.Once
			finish := func() { once.Do(func() { close(release) }) }
			defer finish()
			f := embeddedProductFixture(t, cfg, "/anthropic/v1/messages", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, prefix)
				w.(http.Flusher).Flush()
				select {
				case <-release:
					if protocol == "messages-checkpoint-failure" {
						io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"uncommitted-second\"}}\n\n")
						w.(http.Flusher).Flush()
						<-r.Context().Done()
					} else {
						io.WriteString(w, delimiter+tail)
					}
				case <-r.Context().Done():
				}
			}))
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			path, body, terminal := "/v1/messages", `{"model":"deepseek/fixture","max_tokens":32,"stream":true,"messages":[{"role":"user","content":"synthetic"}]}`, "event: message_stop"
			if protocol == "chat" {
				path = "/v1/chat/completions"
				terminal = "data: [DONE]"
			}
			if protocol == "responses" {
				path = "/v1/responses"
				body = `{"model":"deepseek/fixture","max_output_tokens":32,"stream":true,"store":false,"input":"synthetic"}`
				terminal = "event: response.completed"
			}
			r, _ := http.NewRequestWithContext(ctx, "POST", f.server.URL+path, strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+f.key)
			response, err := http.DefaultClient.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != 200 {
				t.Fatal("native stream refused", response.StatusCode)
			}
			scan := bufio.NewScanner(response.Body)
			seen := false
			for scan.Scan() {
				if strings.Contains(scan.Text(), "synthetic realtime text") {
					seen = true
					break
				}
			}
			if !seen {
				t.Fatal("text withheld until upstream stop", scan.Err())
			}
			var events, receipts, dispatched int
			if err := f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM engine_completion_events),(SELECT count(*) FROM engine_completion_receipts),(SELECT count(*) FROM budget_reservations WHERE state='dispatched')`).Scan(&events, &receipts, &dispatched); err != nil || events != 0 || receipts != 0 || dispatched != 1 {
				t.Fatal("provisional usage committed before message_stop", err)
			}
			if protocol == "messages-audit-failure" {
				if _, err := f.db.Exec(ctx, `ALTER TABLE content_audit_payloads ADD CONSTRAINT reject_response CHECK(direction<>'response') NOT VALID`); err != nil {
					t.Fatal(err)
				}
			}
			if protocol == "messages-checkpoint-failure" {
				if _, err := f.db.Exec(ctx, `ALTER TABLE content_audit_checkpoint_parts ADD CONSTRAINT reject_future CHECK(false) NOT VALID`); err != nil {
					t.Fatal(err)
				}
			}
			finish()
			var result strings.Builder
			for scan.Scan() {
				result.WriteString(scan.Text())
				result.WriteByte('\n')
			}
			failed := strings.Contains(protocol, "failure")
			if scan.Err() != nil || (strings.Contains(result.String(), terminal) == failed) {
				t.Fatal("stream did not seal", scan.Err())
			}
			if failed && (!strings.Contains(result.String(), "content_audit_unavailable") || strings.Contains(result.String(), "uncommitted-second")) {
				t.Fatal("uncommitted text escaped or audit error lost")
			}
			wantPosted, wantAudited := 1, 1
			if failed {
				wantAudited = 0
			}
			if protocol == "messages-checkpoint-failure" {
				wantPosted = 0
			}
			var posted, audited int
			if err := f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM engine_completion_receipts WHERE actual=11),(SELECT count(*) FROM content_audit_records WHERE state='complete')`).Scan(&posted, &audited); err != nil || posted != wantPosted || audited != wantAudited || f.calls.Load() != 1 {
				t.Fatal("realtime settlement/audit not exactly once", err)
			}
		})
	}
}
