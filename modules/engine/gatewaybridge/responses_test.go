package gatewaybridge

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jhzhfree/ai-governance-gateway/internal/admission"
	"github.com/jhzhfree/ai-governance-gateway/internal/circuit"
	"github.com/jhzhfree/ai-governance-gateway/internal/completion"
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

type responsesProductFixture struct {
	db      *pgxpool.Pool
	server  *httptest.Server
	managed *Managed
	source  *provider.Manager
	actor   identity.Principal
	key     string
	calls   atomic.Int32
}

// Uses the real SDK, durable bindings, managed publication and root governance.
// Only the provider network origin and fixture credentials are synthetic.
func embeddedResponsesFixture(t *testing.T, upstream http.Handler) *responsesProductFixture {
	t.Helper()
	cfg := testutil.MainlandConfig()
	cfg.Models = cfg.Models[3:]
	cfg.Models[0].MaxOutputTokens = 32
	return embeddedProductFixture(t, cfg, "/chat/completions", upstream)
}
func embeddedProductFixture(t *testing.T, cfg provider.Config, path string, upstream http.Handler) *responsesProductFixture {
	t.Helper()
	return embeddedProductFixtureAs(t, cfg, path, upstream, identity.Principal{Issuer: "synthetic", Subject: "owner"})
}
func embeddedProductFixtureAs(t *testing.T, cfg provider.Config, path string, upstream http.Handler, actor identity.Principal) *responsesProductFixture {
	t.Helper()
	ctx := context.Background()
	db, ledger, _ := testutil.Database(t)
	f := &responsesProductFixture{db: db, actor: actor}
	f.source = provider.NewEmbeddedManager(db, func(string) string { return "synthetic-responses-key" }, nil, f.actor)
	if err := f.source.Publish(ctx, f.actor, 0, cfg); err != nil {
		t.Fatal(err)
	}
	supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if r.URL.Path != path || r.Header.Get("Authorization") != "Bearer synthetic-responses-key" {
			t.Error("unexpected physical path or credentials")
		}
		upstream.ServeHTTP(w, r)
	}))
	t.Cleanup(supplier.Close)
	journal := completion.New(db)
	engines, err := newPool(ctx, PoolConfig{4, 4}, func(ctx context.Context, r *provider.Route, n int) (ownedBinding, error) {
		b, err := newSupplier(ctx, r, n, func(ctx context.Context, cfg adapter.Config) (execution.Executor, error) {
			cfg.BaseURL = supplier.URL
			cfg.TestLoopback = true
			return adapter.New(ctx, cfg)
		})
		if err != nil {
			return nil, err
		}
		b.binding.journal = journal
		return b, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f.managed = newManaged(f.source, engines)
	t.Cleanup(f.managed.Close)
	if !f.managed.Ready() {
		t.Fatal("embedded fixture not ready")
	}
	store := control.NewWithCatalog(db, f.managed)
	org, err := store.CreateOrganizationWithCurrency(ctx, f.actor, "Responses fixture", "2026-11", provider.Currency, 10000)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, f.actor, org.ID, "Responses", 5000)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetModels(ctx, f.actor, org.ID, project.ID, []string{cfg.Models[0].ID}); err != nil {
		t.Fatal(err)
	}
	key, err := store.CreateKey(ctx, f.actor, org.ID, project.ID, "Responses", []string{cfg.Models[0].ID}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	f.key = key.Secret
	audit := testutil.ContentAudit(t, db)
	if err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		return audit.SetPolicy(ctx, tx, org.ID, project.ID, contentaudit.Policy{Enabled: true, RetentionDays: 1, MaxBytes: 8192, ConsentRef: "synthetic"})
	}); err != nil {
		t.Fatal(err)
	}
	h := &gateway.Handler{Completions: journal, Providers: f.managed, Executor: f.managed, Ledger: ledger, Authorize: store.ResolveKey, Attempts: recorder.New(db), Admission: admission.New(db), Circuits: circuit.New(db), Content: audit, DeviceProof: store.VerifyDeviceRequest}
	f.server = httptest.NewServer(h)
	t.Cleanup(f.server.Close)
	return f
}

func (f *responsesProductFixture) assertPosted(t *testing.T, want int) {
	t.Helper()
	var reservations, events, receipts, attempts, audits int
	err := f.db.QueryRow(context.Background(), `SELECT
 (SELECT count(*) FROM budget_reservations WHERE state='settled' AND actual=14),
 (SELECT count(*) FROM engine_completion_events WHERE input_tokens=3 AND output_tokens=2 AND reconciled),
 (SELECT count(*) FROM engine_completion_receipts WHERE actual=14),
 (SELECT count(*) FROM inference_attempts WHERE outcome='completed'),
 (SELECT count(*) FROM content_audit_records WHERE state='complete')`).Scan(&reservations, &events, &receipts, &attempts, &audits)
	if err != nil {
		t.Fatal(err)
	}
	if reservations != want || events != want || receipts != want || attempts != want || audits != want || int(f.calls.Load()) != want {
		t.Fatalf("posting mismatch: ledger=%d events=%d receipts=%d attempts=%d audits=%d calls=%d", reservations, events, receipts, attempts, audits, f.calls.Load())
	}
}

func TestEmbeddedResponsesStreamsBeforeCompletion(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	f := embeddedResponsesFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"synthetic live text\"},\"finish_reason\":null}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\ndata: [DONE]\n\n")
	}))
	defer finish()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "POST", f.server.URL+"/v1/responses", strings.NewReader(`{"model":"deepseek/fixture","input":"synthetic input","max_output_tokens":32,"store":false,"stream":true}`))
	r.Header.Set("Authorization", "Bearer "+f.key)
	response, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("stream rejected", response.StatusCode)
	}
	scan := bufio.NewScanner(response.Body)
	seen := false
	for scan.Scan() {
		if strings.Contains(scan.Text(), `"delta":"synthetic live text"`) {
			seen = true
			break
		}
	}
	if !seen {
		t.Fatal("text buffered until upstream completion", scan.Err())
	}
	var events, receipts, dispatched int
	if err = f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM engine_completion_events),(SELECT count(*) FROM engine_completion_receipts),(SELECT count(*) FROM budget_reservations WHERE state='dispatched')`).Scan(&events, &receipts, &dispatched); err != nil || events != 0 || receipts != 0 || dispatched != 1 {
		t.Fatal("premature completion or settlement", err)
	}
	finish()
	var tail strings.Builder
	for scan.Scan() {
		tail.WriteString(scan.Text())
		tail.WriteByte('\n')
	}
	if scan.Err() != nil || !strings.Contains(tail.String(), "event: response.completed") || strings.Contains(tail.String(), "[DONE]") {
		t.Fatal("public stream did not seal", scan.Err())
	}
	f.assertPosted(t, 1)
}

func TestEmbeddedResponsesCapabilityTracksPublishedSnapshot(t *testing.T) {
	f := embeddedResponsesFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("capability check dispatched")
		w.WriteHeader(500)
	}))
	old, ok := f.managed.Find("deepseek/fixture")
	if !ok {
		t.Fatal("missing route")
	}
	req := poolRequest(old)
	if !f.managed.SupportsPublicProtocol(old, req, "/v1/responses") || f.managed.SupportsPublicProtocol(old, req, "/v1/messages") {
		t.Fatal("incorrect protocol capability")
	}
	cfg := testutil.MainlandConfig()
	cfg.Models = cfg.Models[3:]
	cfg.Models[0].MaxOutputTokens = 32
	cfg.Models[0].InputPerMillion++
	if err := f.source.Publish(context.Background(), f.actor, 1, cfg); err != nil {
		t.Fatal(err)
	}
	current, ok := f.managed.Find("deepseek/fixture")
	if !ok || current.ExecutionIdentity() == old.ExecutionIdentity() {
		t.Fatal("no new snapshot")
	}
	if f.managed.SupportsPublicProtocol(old, req, "/v1/responses") || !f.managed.SupportsPublicProtocol(current, poolRequest(current), "/v1/responses") {
		t.Fatal("stale capability admitted")
	}
	f.managed.Close()
	if f.managed.SupportsPublicProtocol(current, poolRequest(current), "/v1/responses") || f.calls.Load() != 0 {
		t.Fatal("closed capability admitted or dispatched")
	}
}
