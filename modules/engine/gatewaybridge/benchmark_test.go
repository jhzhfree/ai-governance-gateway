package gatewaybridge

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
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
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
	adapter "github.com/jhzhfree/ai-governance-gateway/modules/engine/adapters/bifrost"
	"github.com/jhzhfree/ai-governance-gateway/modules/engine/execution"
)

// Only the benchmark's fixed supplier can be mapped to its synthetic loopback
// server. Production route configuration and destination checks are unchanged.
type benchmarkTransport struct {
	target *url.URL
	base   *http.Transport
}

func (t benchmarkTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "api.deepseek.com" || r.URL.Path != "/chat/completions" {
		return nil, fmt.Errorf("unexpected synthetic benchmark destination")
	}
	copy := r.Clone(r.Context())
	copy.URL.Scheme, copy.URL.Host = t.target.Scheme, t.target.Host
	copy.Host = t.target.Host
	return t.base.RoundTrip(copy)
}

// Measures in-process governance plus local HTTP and real PostgreSQL. It does
// not include ingress TLS, OIDC, device proof, external latency or stream load.
func BenchmarkGovernedGateway(b *testing.B) {
	for _, mode := range []string{"native", "managed"} {
		for _, content := range []bool{false, true} {
			b.Run(fmt.Sprintf("%s/content=%t", mode, content), func(b *testing.B) {
				ctx := context.Background()
				db, ledger, _ := testutil.Database(b)
				var upstreamCalls atomic.Int64
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					upstreamCalls.Add(1)
					if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer synthetic-benchmark-key" {
						w.WriteHeader(400)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"synthetic"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
				}))
				b.Cleanup(upstream.Close)
				target, _ := url.Parse(upstream.URL)
				transport := http.DefaultTransport.(*http.Transport).Clone()
				transport.Proxy = nil
				b.Cleanup(transport.CloseIdleConnections)
				actor := identity.Principal{Issuer: "synthetic", Subject: "benchmark"}
				source := provider.NewManager(db, func(string) string { return "synthetic-benchmark-key" }, &http.Client{Transport: benchmarkTransport{target, transport}}, actor)
				cfg := testutil.MainlandConfig()
				cfg.Models = cfg.Models[3:]
				cfg.Models[0].MaxOutputTokens = 32
				if err := source.Publish(ctx, actor, 0, cfg); err != nil {
					b.Fatal(err)
				}
				store := control.NewWithCatalog(db, source)
				org, err := store.CreateOrganizationWithCurrency(ctx, actor, "benchmark", "2026-11", provider.Currency, 100000000)
				if err != nil {
					b.Fatal(err)
				}
				project, err := store.CreateProject(ctx, actor, org.ID, "benchmark", 100000000)
				if err != nil {
					b.Fatal(err)
				}
				if err := store.SetModels(ctx, actor, org.ID, project.ID, []string{"deepseek/fixture"}); err != nil {
					b.Fatal(err)
				}
				key, err := store.CreateKey(ctx, actor, org.ID, project.ID, "benchmark", []string{"deepseek/fixture"}, time.Hour)
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
				h := &gateway.Handler{Providers: source, Ledger: ledger, Authorize: store.ResolveKey, Attempts: recorder.New(db), Admission: admission.New(db), Circuits: circuit.New(db), Content: audit, DeviceProof: store.VerifyDeviceRequest}
				if mode == "managed" {
					pool, err := newPool(ctx, PoolConfig{MaxBindings: 4, MaxConcurrent: 64}, func(ctx context.Context, r *provider.Route, n int) (ownedBinding, error) {
						binding, err := newSupplier(ctx, r, n, func(ctx context.Context, cfg adapter.Config) (execution.Executor, error) {
							cfg.BaseURL, cfg.TestLoopback = upstream.URL, true
							return adapter.New(ctx, cfg)
						})
						if err != nil {
							return nil, err
						}
						return binding, nil
					})
					if err != nil {
						b.Fatal(err)
					}
					managed := newManaged(source, pool)
					b.Cleanup(managed.Close)
					h.Providers, h.Executor = managed, managed
					store = control.NewWithCatalog(db, managed)
					h.Authorize, h.DeviceProof = store.ResolveKey, store.VerifyDeviceRequest
				}
				invoke := func() bool {
					r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"deepseek/fixture","max_tokens":32,"messages":[{"role":"user","content":"synthetic"}]}`))
					r.Header.Set("Authorization", "Bearer "+key.Secret)
					w := httptest.NewRecorder()
					h.ServeHTTP(w, r)
					return w.Code == 200
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
					b.Fatal("benchmark requests failed or retried")
				}
				var held, spent int64
				if err := db.QueryRow(ctx, `SELECT held,spent FROM budget_accounts WHERE organization_id=$1 AND project_id=''`, org.ID).Scan(&held, &spent); err != nil || held != 0 || spent != int64(b.N+1)*14 {
					b.Fatal("benchmark account conservation failed")
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
