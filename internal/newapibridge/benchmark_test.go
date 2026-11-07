package newapibridge_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jhzhfree/ai-governance-gateway/internal/admission"
	"github.com/jhzhfree/ai-governance-gateway/internal/circuit"
	"github.com/jhzhfree/ai-governance-gateway/internal/contentaudit"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	recorder "github.com/jhzhfree/ai-governance-gateway/internal/execution"
	"github.com/jhzhfree/ai-governance-gateway/internal/gateway"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/newapibridge"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

// Real enterprise authorization, PostgreSQL, durable new-api process/SQLite and
// local HTTP supplier. No external latency, ingress TLS, OIDC or terminal proof.
func BenchmarkNewAPIGovernance(b *testing.B) {
	binary := os.Getenv("TEST_NEWAPI_SERVER_BINARY")
	if binary == "" {
		b.Fatal("pinned new-api fixture binary required")
	}
	sqliteConnections := os.Getenv("AIGW_BENCH_SQLITE_CONNECTIONS")
	if sqliteConnections == "" {
		sqliteConnections = "1000"
	}
	size, err := strconv.Atoi(sqliteConnections)
	if err != nil || size < 1 || size > 1000 {
		b.Fatal("benchmark SQLite connections must be 1..1000")
	}
	concurrency := 0
	if value := os.Getenv("AIGW_BENCH_CONCURRENCY"); value != "" {
		concurrency, err = strconv.Atoi(value)
		if err != nil || concurrency < 1 || concurrency > 64 {
			b.Fatal("benchmark concurrency must be 1..64")
		}
	}
	delayMS := 0
	if value := os.Getenv("AIGW_BENCH_UPSTREAM_DELAY_MS"); value != "" {
		delayMS, err = strconv.Atoi(value)
		if err != nil || delayMS < 0 || delayMS > 10000 {
			b.Fatal("synthetic upstream delay must be 0..10000 ms")
		}
	}
	for _, protocol := range []string{"chat", "stream", "embedding", "responses", "responses_stream", "messages", "cache_ttl", "messages_stream"} {
		for _, content := range []bool{false, true} {
			b.Run(fmt.Sprintf("%s/content=%t", protocol, content), func(b *testing.B) {
				ctx := context.Background()
				db, ledger, _ := testutil.Database(b)
				var upstreamCalls, active, peak atomic.Int64
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					upstreamCalls.Add(1)
					current := active.Add(1)
					defer active.Add(-1)
					for previous := peak.Load(); current > previous; previous = peak.Load() {
						if peak.CompareAndSwap(previous, current) {
							break
						}
					}

					credential := r.Header.Get("Authorization")
					if protocol == "cache_ttl" || protocol == "messages_stream" {
						credential = "Bearer " + r.Header.Get("X-Api-Key")
					}
					if credential != "Bearer syntheticvendor" {
						w.WriteHeader(400)
						return
					}
					if delayMS > 0 && protocol != "stream" && protocol != "responses_stream" && protocol != "messages_stream" {
						select {
						case <-time.After(time.Duration(delayMS) * time.Millisecond):
						case <-r.Context().Done():
							return
						}
					}
					w.Header().Set("Content-Type", "application/json")
					if protocol == "cache_ttl" || protocol == "messages_stream" {
						if r.URL.Path != "/v1/messages" {
							w.WriteHeader(400)
							return
						}
						if protocol == "messages_stream" {
							w.Header().Set("Content-Type", "text/event-stream")
							for _, frame := range []string{
								`{"type":"message_start","message":{"id":"msg_synthetic","type":"message","role":"assistant","model":"governance-model","content":[],"usage":{"input_tokens":2,"output_tokens":1,"cache_read_input_tokens":4,"cache_creation_input_tokens":5,"cache_creation":{"ephemeral_5m_input_tokens":3,"ephemeral_1h_input_tokens":2}}}}`,
								`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
								`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"synthetic"}}`,
							} {
								fmt.Fprintf(w, "data: %s\n\n", frame)
							}
							w.(http.Flusher).Flush()
							if delayMS > 0 {
								select {
								case <-time.After(time.Duration(delayMS) * time.Millisecond):
								case <-r.Context().Done():
									return
								}
							}
							for _, frame := range []string{
								`{"type":"content_block_stop","index":0}`,
								`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
								`{"type":"message_stop"}`,
							} {
								fmt.Fprintf(w, "data: %s\n\n", frame)
							}
							return
						}
						fmt.Fprint(w, `{"id":"msg_synthetic","type":"message","role":"assistant","model":"governance-model","content":[{"type":"text","text":"synthetic"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3,"cache_read_input_tokens":4,"cache_creation_input_tokens":5,"cache_creation":{"ephemeral_5m_input_tokens":3,"ephemeral_1h_input_tokens":2}}}`)
						return
					}
					if protocol == "messages" {
						fmt.Fprint(w, `{"id":"synthetic","object":"chat.completion","model":"governance-model","choices":[{"index":0,"message":{"role":"assistant","content":"synthetic"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5,"prompt_tokens_details":{"cached_tokens":1,"cached_creation_tokens":0}}}`)
						return
					}
					if protocol == "embedding" {
						fmt.Fprint(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"governance-model","usage":{"prompt_tokens":2,"total_tokens":2}}`)
						return
					}
					if protocol == "stream" || protocol == "responses_stream" {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, "data: {\"id\":\"synthetic\",\"object\":\"chat.completion.chunk\",\"model\":\"governance-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"synthetic\"},\"finish_reason\":null}]}\n\n")
						w.(http.Flusher).Flush()
						if delayMS > 0 {
							select {
							case <-time.After(time.Duration(delayMS) * time.Millisecond):
							case <-r.Context().Done():
								return
							}
						}
						fmt.Fprint(w, "data: {\"id\":\"synthetic\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":3,\"total_tokens\":5}}\n\ndata: [DONE]\n\n")
						return
					}
					fmt.Fprint(w, `{"id":"synthetic","object":"chat.completion","model":"governance-model","choices":[{"index":0,"message":{"role":"assistant","content":"synthetic"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`)
				}))
				b.Cleanup(upstream.Close)
				root := b.TempDir()
				nativeProtocol := "openai"
				if protocol == "cache_ttl" || protocol == "messages_stream" {
					nativeProtocol = "anthropic"
				}
				command := exec.Command(binary)
				command.Dir = root
				command.Env = append(os.Environ(), "GOVERNANCE_FIXTURE_DIR="+root, "GOVERNANCE_FIXTURE_UPSTREAM="+upstream.URL, "GOVERNANCE_FIXTURE_PROTOCOL="+nativeProtocol, "SQL_MAX_OPEN_CONNS="+sqliteConnections, "SQL_MAX_IDLE_CONNS=1", "GOMAXPROCS="+strconv.Itoa(runtime.GOMAXPROCS(0)))
				nativeLog, err := os.CreateTemp(root, "native-*.log")
				if err != nil {
					b.Fatal("fixture log unavailable")
				}
				b.Cleanup(func() { _ = nativeLog.Close() })
				command.Stdout = nativeLog
				command.Stderr = nativeLog
				if err := command.Start(); err != nil {
					b.Fatal("fixture failed to start")
				}
				b.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
				origin := ""
				deadline := time.Now().Add(30 * time.Second)
				for time.Now().Before(deadline) {
					data, err := os.ReadFile(filepath.Join(root, "address"))
					if err == nil {
						origin = string(data)
						break
					}
					time.Sleep(20 * time.Millisecond)
				}
				if origin == "" {
					b.Fatal("fixture readiness timeout")
				}
				req, _ := http.NewRequest("GET", origin+"/internal/governance/v1/channels/1/revision", nil)
				req.Header.Set("Authorization", "Bearer sk-syntheticgovernancefixture")
				client := &http.Client{Timeout: 5 * time.Second}
				response, err := client.Do(req)
				if err != nil {
					b.Fatal("fixture manifest unavailable")
				}
				var manifest struct {
					Revision string `json:"revision"`
				}
				err = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&manifest)
				response.Body.Close()
				if err != nil || response.StatusCode != 200 || len(manifest.Revision) != 64 {
					b.Fatal("fixture manifest invalid")
				}
				revision := manifest.Revision
				actor := identity.Principal{Issuer: "synthetic", Subject: "benchmark"}

				cfg := testutil.MainlandConfig()
				cfg.Models = cfg.Models[2:3]
				cfg.Models[0].UpstreamModel = "governance-model"
				cfg.Models[0].NewAPIChannelID = 1
				cfg.Models[0].NewAPIChannelRevision = revision
				if protocol == "embedding" {
					cfg.Models[0].API = "embeddings"
					cfg.Models[0].MaxOutputTokens = 0
					cfg.Models[0].OutputPerMillion = 0
				}
				if protocol != "embedding" {
					cfg.Models[0].MaxOutputTokens = 32
				}
				if protocol == "messages" || protocol == "cache_ttl" || protocol == "messages_stream" {
					cfg.Models[0].CachePricing = &provider.CachePricing{ReadPerMillion: 500000, WritePerMillion: 3000000}
					if protocol == "cache_ttl" || protocol == "messages_stream" {
						cfg.Models[0].CachePricing.WriteTTL = &provider.CacheWriteTTL{FiveMinutePerMillion: 3000000, OneHourPerMillion: 6000000}
					}
				}
				cfg.Models[0].AllowMessagesStream = protocol == "messages_stream"
				directory, err := provider.NewGoverned(cfg)
				if err != nil {
					b.Fatal(err)
				}
				executionLimit := 64
				if concurrency > 0 {
					executionLimit = concurrency
				}
				source, err := newapibridge.NewRuntime(ctx, newapibridge.Config{Origin: origin, ServiceToken: "sk-syntheticgovernancefixture", MaxConcurrent: executionLimit, AllowLoopbackHTTP: true, Journal: newapibridge.NewJournal(db)}, directory)
				if err != nil {
					b.Fatal(err)
				}
				b.Cleanup(source.Close)

				store := control.NewWithCatalog(db, source)
				org, err := store.CreateOrganizationWithCurrency(ctx, actor, "benchmark", "2026-11", provider.Currency, 100000000)
				if err != nil {
					b.Fatal(err)
				}
				project, err := store.CreateProject(ctx, actor, org.ID, "benchmark", 100000000)
				if err != nil {
					b.Fatal(err)
				}
				if err := store.SetModels(ctx, actor, org.ID, project.ID, []string{"zhipu/fixture"}); err != nil {
					b.Fatal(err)
				}
				key, err := store.CreateKey(ctx, actor, org.ID, project.ID, "benchmark", []string{"zhipu/fixture"}, time.Hour)
				if err != nil {
					b.Fatal(err)
				}
				audit := testutil.ContentAudit(b, db)
				if err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
					if err := admission.Update(ctx, tx, org.ID, project.ID, admission.Policy{RequestsPerMinute: 10000, MaxConcurrent: executionLimit}); err != nil {
						return err
					}
					return audit.SetPolicy(ctx, tx, org.ID, project.ID, contentaudit.Policy{Enabled: content, RetentionDays: 1, MaxBytes: 8192, ConsentRef: "synthetic"})
				}); err != nil {
					b.Fatal(err)
				}
				h := &gateway.Handler{Providers: source, Executor: source, Ledger: ledger, Authorize: store.ResolveKey, Attempts: recorder.New(db), Admission: admission.New(db), Circuits: circuit.New(db), Content: audit, DeviceProof: store.VerifyDeviceRequest}
				path := "/v1/chat/completions"
				payload := `{"model":"zhipu/fixture","max_tokens":32,"messages":[{"role":"user","content":"synthetic"}]}`
				cost := int64(16)
				if protocol == "messages" {
					path = "/v1/messages"
					cost = 15
				}
				if protocol == "cache_ttl" || protocol == "messages_stream" {
					cost = 39
				}
				if protocol == "messages_stream" {
					path = "/v1/messages"
					payload = `{"model":"zhipu/fixture","stream":true,"max_tokens":32,"messages":[{"role":"user","content":"synthetic"}]}`
				}
				if protocol == "stream" {
					payload = `{"model":"zhipu/fixture","stream":true,"max_tokens":32,"messages":[{"role":"user","content":"synthetic"}]}`
				}
				if protocol == "embedding" {
					path = "/v1/embeddings"
					payload = `{"model":"zhipu/fixture","input":"synthetic"}`
					cost = 4
				}
				if protocol == "responses" || protocol == "responses_stream" {
					path = "/v1/responses"
					payload = fmt.Sprintf(`{"model":"zhipu/fixture","input":"synthetic","max_output_tokens":32,"store":false,"stream":%t}`, protocol == "responses_stream")
				}
				var gatewayActive, gatewayPeak atomic.Int64
				invoke := func() bool {
					active := gatewayActive.Add(1)
					defer gatewayActive.Add(-1)
					for previous := gatewayPeak.Load(); active > previous; previous = gatewayPeak.Load() {
						if gatewayPeak.CompareAndSwap(previous, active) {
							break
						}
					}

					r := httptest.NewRequest("POST", path, strings.NewReader(payload))
					r.Header.Set("Authorization", "Bearer "+key.Secret)
					w := httptest.NewRecorder()
					h.ServeHTTP(w, r)
					if w.Code != 200 {
						b.Logf("synthetic benchmark failure: status=%d body=%s", w.Code, w.Body.String())
					}
					if w.Code != 200 {
						return false
					}
					if protocol == "messages" {
						var result provider.MessagesResult
						return json.Unmarshal(w.Body.Bytes(), &result) == nil && result.Type == "message" && result.StopReason == "end_turn" && result.Usage.InputTokens == 1 && result.Usage.CacheReadInputTokens == 1 && result.Usage.CacheCreationInputTokens == 0 && result.Usage.OutputTokens == 3
					}
					if protocol == "messages_stream" {
						body := w.Body.String()
						return strings.Count(body, "event: message_start\n") == 1 && strings.Count(body, "event: message_stop\n") == 1 && strings.Contains(body, `"input_tokens":2`) && strings.Contains(body, `"cache_read_input_tokens":4`) && strings.Contains(body, `"cache_creation_input_tokens":5`) && strings.Contains(body, `"output_tokens":3`) && !strings.Contains(body, "event: error") && !strings.Contains(body, "[DONE]")
					}
					if protocol == "responses_stream" {
						return strings.Contains(w.Body.String(), `"type":"response.completed"`) && !strings.Contains(w.Body.String(), "[DONE]")
					}
					if protocol == "responses" {
						return strings.Contains(w.Body.String(), `"object":"response"`) && strings.Contains(w.Body.String(), `"status":"completed"`)
					}
					return protocol != "stream" || strings.Contains(w.Body.String(), "[DONE]")
				}
				if !invoke() {
					b.Fatal("warmup request failed")
				}
				durations := make([]int64, b.N)
				var next, failures atomic.Int64
				b.ReportAllocs()
				b.ResetTimer()
				workers := concurrency
				if workers == 0 {
					workers = runtime.GOMAXPROCS(0)
				}
				var group sync.WaitGroup
				for range workers {
					group.Go(func() {
						for {
							index := next.Add(1) - 1
							if index >= int64(b.N) {
								return
							}
							started := time.Now()
							if !invoke() {
								failures.Add(1)
							}
							durations[index] = time.Since(started).Nanoseconds()
						}
					})
				}
				group.Wait()
				b.StopTimer()
				if failures.Load() != 0 || upstreamCalls.Load() != int64(b.N+1) {
					_ = nativeLog.Sync()
					logData, _ := os.ReadFile(nativeLog.Name())
					b.Logf("synthetic native SQLite lock diagnostics: %d", strings.Count(string(logData), "sqlite error 5")+strings.Count(string(logData), "database is locked"))
					b.Fatalf("benchmark requests failed or retried: failures=%d calls=%d expected=%d", failures.Load(), upstreamCalls.Load(), b.N+1)
				}
				if delayMS >= 1000 && b.N >= workers && (peak.Load() != int64(workers) || gatewayPeak.Load() != int64(workers)) {
					b.Fatal("configured long-call concurrency was not reached")
				}
				var held, spent int64
				if err := db.QueryRow(ctx, `SELECT held,spent FROM budget_accounts WHERE organization_id=$1 AND project_id=''`, org.ID).Scan(&held, &spent); err != nil || held != 0 || spent != int64(b.N+1)*cost {
					b.Fatal("benchmark account conservation failed")
				}
				var receipts int64
				// Recovery is paged at 100; stress runs may exceed one page.
				for pass := 0; pass < 2+(b.N+1)/100; pass++ {
					if err := source.ReconcileOnce(ctx); err != nil {
						b.Fatal("outbox recovery failed")
					}
					if err := db.QueryRow(ctx, `SELECT count(*) FROM newapi_execution_receipts`).Scan(&receipts); err != nil {
						b.Fatal("receipt query failed")
					}
					if receipts == int64(b.N+1) {
						break
					}
				}
				if receipts != int64(b.N+1) {
					b.Fatal("missing durable receipts")
				}

				if protocol == "cache_ttl" || protocol == "messages_stream" {
					var proven int64
					if err := db.QueryRow(ctx, `SELECT count(*) FROM newapi_execution_receipts WHERE cache_details_version=2 AND cache_read_tokens=4 AND cache_write_tokens=5 AND cache_write_5m_tokens=3 AND cache_write_1h_tokens=2 AND actual=39`).Scan(&proven); err != nil || proven != int64(b.N+1) {
						b.Fatal("mixed duration evidence missing under load")
					}
				}
				var complete, total int64
				if err := db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE state='complete'),count(*) FROM content_audit_records`).Scan(&complete, &total); err != nil {
					b.Fatal("audit completion query failed")
				}
				expectedAudits := int64(0)
				if content {
					expectedAudits = int64(b.N + 1)
				}
				if total != expectedAudits || complete != expectedAudits {
					b.Fatal("audit captures incomplete or missing")
				}
				if checkpoint, err := audit.Verify(ctx, org.ID); err != nil || !checkpoint.Valid {
					b.Fatal("benchmark audit chain invalid")
				}
				sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
				for _, p := range []int{50, 95, 99} {
					index := (len(durations)*p+99)/100 - 1
					b.ReportMetric(float64(durations[index])/1e6, fmt.Sprintf("p%d-ms", p))
				}
				b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "req/s")
				b.ReportMetric(float64(workers), "workers")
				b.ReportMetric(float64(peak.Load()), "upstream-peak")
				b.ReportMetric(float64(gatewayPeak.Load()), "gateway-peak")
				b.ReportMetric(float64(delayMS), "synthetic-delay-ms")
			})
		}
	}
}
