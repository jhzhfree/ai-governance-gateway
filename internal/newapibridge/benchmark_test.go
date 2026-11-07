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
	for _, protocol := range []string{"chat", "stream", "embedding"} {
		for _, content := range []bool{false, true} {
			b.Run(fmt.Sprintf("%s/content=%t", protocol, content), func(b *testing.B) {
				ctx := context.Background()
				db, ledger, _ := testutil.Database(b)
				var upstreamCalls atomic.Int64
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					upstreamCalls.Add(1)
					if r.Header.Get("Authorization") != "Bearer syntheticvendor" {
						w.WriteHeader(400)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					if protocol == "embedding" {
						fmt.Fprint(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"governance-model","usage":{"prompt_tokens":2,"total_tokens":2}}`)
						return
					}
					if protocol == "stream" {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, "data: {\"id\":\"synthetic\",\"object\":\"chat.completion.chunk\",\"model\":\"governance-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"synthetic\"},\"finish_reason\":null}]}\n\n")
						fmt.Fprint(w, "data: {\"id\":\"synthetic\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":3,\"total_tokens\":5}}\n\ndata: [DONE]\n\n")
						return
					}
					fmt.Fprint(w, `{"id":"synthetic","object":"chat.completion","model":"governance-model","choices":[{"index":0,"message":{"role":"assistant","content":"synthetic"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`)
				}))
				b.Cleanup(upstream.Close)
				root := b.TempDir()
				command := exec.Command(binary)
				command.Dir = root
				command.Env = append(os.Environ(), "GOVERNANCE_FIXTURE_DIR="+root, "GOVERNANCE_FIXTURE_UPSTREAM="+upstream.URL, "GOVERNANCE_FIXTURE_PROTOCOL=openai", "SQL_MAX_OPEN_CONNS="+sqliteConnections, "SQL_MAX_IDLE_CONNS=1", "GOMAXPROCS="+strconv.Itoa(runtime.GOMAXPROCS(0)))
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
				directory, err := provider.NewGoverned(cfg)
				if err != nil {
					b.Fatal(err)
				}
				source, err := newapibridge.NewRuntime(ctx, newapibridge.Config{Origin: origin, ServiceToken: "sk-syntheticgovernancefixture", MaxConcurrent: 64, AllowLoopbackHTTP: true, Journal: newapibridge.NewJournal(db)}, directory)
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
					if err := admission.Update(ctx, tx, org.ID, project.ID, admission.Policy{RequestsPerMinute: 10000, MaxConcurrent: 64}); err != nil {
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
				if protocol == "stream" {
					payload = `{"model":"zhipu/fixture","stream":true,"max_tokens":32,"messages":[{"role":"user","content":"synthetic"}]}`
				}
				if protocol == "embedding" {
					path = "/v1/embeddings"
					payload = `{"model":"zhipu/fixture","input":"synthetic"}`
					cost = 4
				}
				invoke := func() bool {
					r := httptest.NewRequest("POST", path, strings.NewReader(payload))
					r.Header.Set("Authorization", "Bearer "+key.Secret)
					w := httptest.NewRecorder()
					h.ServeHTTP(w, r)
					if w.Code != 200 {
						b.Logf("synthetic benchmark failure: status=%d body=%s", w.Code, w.Body.String())
					}
					return w.Code == 200 && (protocol != "stream" || strings.Contains(w.Body.String(), "[DONE]"))
				}
				if !invoke() {
					b.Fatal("warmup request failed")
				}
				durations := make([]int64, b.N)
				var next, failures atomic.Int64
				b.ReportAllocs()
				b.ResetTimer()
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						index := next.Add(1) - 1
						started := time.Now()
						if !invoke() {
							failures.Add(1)
						}
						durations[index] = time.Since(started).Nanoseconds()
					}
				})
				b.StopTimer()
				if failures.Load() != 0 || upstreamCalls.Load() != int64(b.N+1) {
					_ = nativeLog.Sync()
					logData, _ := os.ReadFile(nativeLog.Name())
					b.Logf("synthetic native SQLite lock diagnostics: %d", strings.Count(string(logData), "sqlite error 5")+strings.Count(string(logData), "database is locked"))
					b.Fatalf("benchmark requests failed or retried: failures=%d calls=%d expected=%d", failures.Load(), upstreamCalls.Load(), b.N+1)
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

				if checkpoint, err := audit.Verify(ctx, org.ID); err != nil || !checkpoint.Valid {
					b.Fatal("benchmark audit chain invalid")
				}
				sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
				for _, p := range []int{50, 95, 99} {
					index := (len(durations)*p+99)/100 - 1
					b.ReportMetric(float64(durations[index])/1e6, fmt.Sprintf("p%d-ms", p))
				}
				b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "req/s")
			})
		}
	}
}
