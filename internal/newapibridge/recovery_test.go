package newapibridge_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/execution"
	"github.com/jhzhfree/ai-governance-gateway/internal/newapibridge"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func TestRecoveryPagesDoNotStarveBehindPoisonEvents(t *testing.T) {
	pool, ledger, scope := testutil.Database(t)
	scope.Currency = provider.Currency
	testutil.Accounts(t, ledger, scope, 10000, 10000)
	cfg := testutil.MainlandConfig()
	for i := range cfg.Models {
		cfg.Models[i].NewAPIChannelID = 1
		cfg.Models[i].NewAPIChannelRevision = strings.Repeat("a", 64)
		cfg.Models[i].UpstreamModel = "backend-model"
	}
	directory, err := provider.NewGoverned(cfg)
	if err != nil {
		t.Fatal(err)
	}
	route, _ := directory.Find("zhipu/fixture")
	const attempt = "ValidAttempt123456"
	const requestID = "ValidRequest123456"
	valid := newapibridge.Event{AttemptID: attempt, RequestID: requestID, State: "completed", Model: "backend-model", ChannelID: 1, PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5}
	var poison []newapibridge.Event
	for i := range 100 {
		poison = append(poison, newapibridge.Event{AttemptID: fmt.Sprintf("PoisonAttempt%020d", i), RequestID: fmt.Sprintf("PoisonRequest%020d", i), State: "completed", Model: "backend-model", ChannelID: 1})
	}
	cursor := poison[99].AttemptID
	var acknowledgements atomic.Int32
	var repaired atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("service credential missing")
		}
		if r.Method == "POST" {
			if r.URL.Path != "/internal/governance/v1/events/"+attempt+"/ack" {
				t.Error("poison event acknowledged")
				w.WriteHeader(400)
				return
			}
			acknowledgements.Add(1)
			w.WriteHeader(204)
			return
		}
		w.Header().Set("X-Aigw-Events-Version", "1")
		w.Header().Set("X-Aigw-Paging-Version", "1")
		w.Header().Set("X-Aigw-Admission-Version", "2")
		w.Header().Set("X-Aigw-Config-Version", "1")
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/internal/governance/v1/channels/1/revision":
			w.Header().Set("X-Aigw-Config-Version", "1")
			revision := strings.Repeat("b", 64)
			if repaired.Load() {
				revision = strings.Repeat("a", 64)
			}
			json.NewEncoder(w).Encode(map[string]any{"channel_id": 1, "revision": revision, "enabled": true})
		case "/internal/governance/v1/events":
			if repaired.Load() {
				json.NewEncoder(w).Encode([]newapibridge.Event{})
				return
			}
			w.Header().Set("X-Aigw-Next-Cursor", cursor)
			json.NewEncoder(w).Encode(poison)
		case "/internal/governance/v1/events/after/" + cursor:
			if acknowledgements.Load() == 0 {
				json.NewEncoder(w).Encode([]newapibridge.Event{valid})
			} else {
				json.NewEncoder(w).Encode([]newapibridge.Event{})
			}
		default:
			t.Error("invalid paging path")
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	ctx := context.Background()
	amount, err := route.Bound(8)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: requestID, PriceVersion: route.PriceVersion(), Amount: amount}); err != nil {
		t.Fatal(err)
	}
	if err := execution.New(pool).Begin(ctx, execution.Start{AttemptID: attempt, RequestID: requestID, Model: "zhipu/fixture", Provider: "zhipu", Region: "cn-mainland"}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Dispatch(ctx, requestID); err != nil {
		t.Fatal(err)
	}
	journal := newapibridge.NewJournal(pool)
	if err := journal.Bind(ctx, server.URL, "backend-model", requestID, attempt, strings.Repeat("a", 64), route, 8); err != nil {
		t.Fatal(err)
	}
	executor, err := newapibridge.NewRuntime(ctx, newapibridge.Config{Origin: server.URL, ServiceToken: token, MaxConcurrent: 1, AllowLoopbackHTTP: true, Journal: journal, Models: map[string]string{"zhipu/fixture": "backend-model"}, Channels: map[string]int{"zhipu/fixture": 1}}, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer executor.Close()
	if executor.Ready() {
		t.Fatal("poison backlog started healthy")
	}
	for range 3 {
		if err := executor.ReconcileOnce(ctx); err == nil {
			t.Fatal("unresolved scan cycle reported healthy")
		}
	}
	if acknowledgements.Load() != 1 {
		t.Fatal("valid event starved behind poison page or acknowledged repeatedly")
	}
	row, err := ledger.Get(ctx, requestID)
	if err != nil || row.State != "settled" || row.Actual == nil || *row.Actual != 16 {
		t.Fatal("valid event not settled")
	}
	repaired.Store(true)
	if err := executor.ReconcileOnce(ctx); err != nil {
		t.Fatal("healthy scan did not recover")
	}
	deadline := time.Now().Add(7 * time.Second)
	for !executor.Ready() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !executor.Ready() {
		t.Fatal("worker did not resume healthy readiness")
	}

}

// A long response occupies the entire configured inference pool. Recovery must
// still reach the data plane, rather than timing out waiting for that connection.
func TestRecoveryWhileInferenceConnectionSaturated(t *testing.T) {
	pool, ledger, scope := testutil.Database(t)
	scope.Currency = provider.Currency
	testutil.Accounts(t, ledger, scope, 10000, 10000)
	cfg := testutil.MainlandConfig()
	for i := range cfg.Models {
		cfg.Models[i].NewAPIChannelID = 1
		cfg.Models[i].NewAPIChannelRevision = strings.Repeat("a", 64)
		cfg.Models[i].UpstreamModel = "backend-model"
	}
	directory, err := provider.NewGoverned(cfg)
	if err != nil {
		t.Fatal(err)
	}
	route, _ := directory.Find("zhipu/fixture")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("missing service credential")
		}
		switch r.URL.Path {
		case "/internal/governance/v1/chat/completions":
			evidence(w, r)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		case "/internal/governance/v1/events":
			w.Header().Set("X-Aigw-Events-Version", "1")
			w.Header().Set("X-Aigw-Paging-Version", "1")
			w.Header().Set("X-Aigw-Admission-Version", "2")
			w.Header().Set("X-Aigw-Config-Version", "1")
			json.NewEncoder(w).Encode([]newapibridge.Event{})
		default:
			t.Error("unexpected recovery request")
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	ctx := context.Background()
	const requestID = "SaturatedRequest1234"
	const attemptID = "SaturatedAttempt1234"
	amount, err := route.Bound(8)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: requestID, PriceVersion: route.PriceVersion(), Amount: amount}); err != nil {
		t.Fatal(err)
	}
	if err := execution.New(pool).Begin(ctx, execution.Start{AttemptID: attemptID, RequestID: requestID, Model: "zhipu/fixture", Provider: "zhipu", Region: "cn-mainland"}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Dispatch(ctx, requestID); err != nil {
		t.Fatal(err)
	}
	executor, err := newapibridge.New(newapibridge.Config{Origin: server.URL, ServiceToken: token, MaxConcurrent: 1, AllowLoopbackHTTP: true, Journal: newapibridge.NewJournal(pool), Models: map[string]string{"zhipu/fixture": "backend-model"}, Channels: map[string]int{"zhipu/fixture": 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer executor.Close()
	req := request()
	req.Stream = true
	response, err := executor.Open(ctx, route, req, requestID, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	recoveryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := executor.ReconcileOnce(recoveryCtx); err != nil {
		t.Fatalf("recovery starved behind active stream: %v", err)
	}
	row, err := ledger.Get(ctx, requestID)
	if err != nil || row.State != "dispatched" {
		t.Fatal("in-progress hold changed during recovery")
	}
}

func TestCacheTTLCapabilityCannotSilentlyDowngrade(t *testing.T) {
	pool, _, _ := testutil.Database(t)
	var supported atomic.Bool
	var duplicated atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range map[string]string{"X-Aigw-Events-Version": "1", "X-Aigw-Paging-Version": "1", "X-Aigw-Admission-Version": "2", "X-Aigw-Config-Version": "1", "X-Aigw-Tariff-Version": "1"} {
			w.Header().Set(k, v)
		}
		if supported.Load() {
			w.Header().Set("X-Aigw-Tariff-Versions", "1,2")
		}
		if duplicated.Load() {
			w.Header().Add("X-Aigw-Tariff-Versions", "1,2")
		}
		w.Write([]byte(`[]`))
	}))
	defer server.Close()
	executor, err := newapibridge.New(newapibridge.Config{Origin: server.URL, ServiceToken: token, Models: map[string]string{"reviewed/model": "backend-model"}, Channels: map[string]int{"reviewed/model": 1}, MaxConcurrent: 1, AllowLoopbackHTTP: true, Journal: newapibridge.NewJournal(pool), RequireTariff: true, RequireTariffV2: true})
	if err != nil {
		t.Fatal(err)
	}
	defer executor.Close()
	if executor.ReconcileOnce(context.Background()) == nil {
		t.Fatal("TTL silently downgraded to aggregate capability")
	}
	supported.Store(true)
	if err := executor.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	duplicated.Store(true)
	if executor.ReconcileOnce(context.Background()) == nil {
		t.Fatal("ambiguous capability accepted")
	}
}
