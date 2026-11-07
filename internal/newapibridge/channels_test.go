package newapibridge_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/execution"
	"github.com/jhzhfree/ai-governance-gateway/internal/gateway"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/newapibridge"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func TestUnifiedChannelsPublishRecoveryAndExecution(t *testing.T) {
	ctx := context.Background()
	pool, ledger, scope := testutil.Database(t)
	scope.Currency = provider.Currency
	testutil.Accounts(t, ledger, scope, 100000, 100000)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer synthetic-managed-key" {
			t.Error("wrong supplier credential")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"synthetic","object":"chat.completion","model":"governance-model","choices":[{"index":0,"message":{"role":"assistant","content":"synthetic"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()
	origin := testutil.NewAPIProcess(t, upstream.URL)
	target, _ := url.Parse(origin)
	proxy := httputil.NewSingleHostReverseProxy(target)
	var drop atomic.Bool
	proxy.ModifyResponse = func(res *http.Response) error {
		if res.Request.Method == "POST" && strings.HasSuffix(res.Request.URL.Path, "/channels") && drop.Swap(false) {
			res.Body.Close()
			return errors.New("synthetic lost receipt")
		}
		return nil
	}
	front := httptest.NewServer(proxy)
	defer front.Close()
	admin := identity.Principal{Issuer: "https://fixture.example", Subject: "platform-admin"}
	manager, err := newapibridge.NewChannelManager(pool, front.URL, "sk-syntheticchanneladminfixture", admin, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if _, err := manager.Channels(ctx, identity.Principal{Issuer: admin.Issuer, Subject: "other-owner"}, ""); !errors.Is(err, identity.ErrForbidden) {
		t.Fatal("organization owner gained platform access")
	}
	runtime, err := newapibridge.NewManagedRuntime(ctx, newapibridge.Config{Origin: front.URL, ServiceToken: "sk-syntheticgovernancefixture", AllowLoopbackHTTP: true, MaxConcurrent: 2, Journal: newapibridge.NewJournal(pool)}, manager)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if runtime.Ready() {
		t.Fatal("unpublished directory became ready")
	}
	enabled := true
	secret := "synthetic-managed-key"
	in := newapibridge.ChannelWrite{OperationID: "channel-create-integration", Name: "managed fixture", Type: 1, BaseURL: upstream.URL, Models: []string{"governance-model"}, Enabled: &enabled, APIKey: &secret}
	drop.Store(true)
	op, err := manager.WriteChannel(ctx, admin, 0, in)
	if err != nil || op.State != "pending" {
		t.Fatalf("lost receipt did not remain pending: %v / %s", err, op.State)
	}
	op, err = manager.Operation(ctx, admin, op.ID, true)
	if err != nil || op.State != "applied" {
		t.Fatalf("receipt recovery failed: %v", err)
	}
	if _, err = manager.WriteChannel(ctx, admin, 0, in); err != nil {
		t.Fatal(err)
	}
	conflict := in
	conflict.Name = "different intent"
	if _, err = manager.WriteChannel(ctx, admin, 0, conflict); !errors.Is(err, provider.ErrConfigurationConflict) {
		t.Fatal("operation ID accepted a different request")
	}
	page, err := manager.Channels(ctx, admin, "")
	if err != nil || len(page.Data) != 2 {
		t.Fatal("replay duplicated channel")
	}
	channel := page.Data[1]
	if channel.ID != op.ChannelID || !channel.Editable || channel.KeyCount != 1 || calls.Load() != 0 {
		t.Fatal("channel save executed inference or mismatched receipt")
	}
	var leaked bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM newapi_channel_operations WHERE row_to_json(newapi_channel_operations)::text LIKE '%synthetic-managed-key%')`).Scan(&leaked); err != nil || leaked {
		t.Fatal("credential leaked to control-plane history")
	}
	cfg := testutil.MainlandConfig()
	cfg.Models = cfg.Models[:1]
	cfg.Models[0].NewAPIChannelID = channel.ID
	cfg.Models[0].NewAPIChannelRevision = channel.Revision
	cfg.Models[0].UpstreamModel = "governance-model"
	if err := manager.Publish(ctx, admin, 0, cfg); err != nil {
		t.Fatal(err)
	}
	if runtime.Ready() {
		t.Fatal("unchecked new directory was immediately opened")
	}
	deadline := time.Now().Add(8 * time.Second)
	for !runtime.Ready() && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if !runtime.Ready() {
		t.Fatal("reviewed directory did not activate")
	}
	oldRoute, ok := runtime.Find(cfg.Models[0].ID)
	if !ok {
		t.Fatal("published model missing")
	}
	handler := &gateway.Handler{Executor: runtime, Providers: runtime, Ledger: ledger, Attempts: execution.New(pool), Admission: testutil.PassAdmission{}, Circuits: testutil.PassCircuit{}, Authorize: func(context.Context, string, string) (budget.Scope, error) { return scope, nil }}
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"`+cfg.Models[0].ID+`","messages":[{"role":"user","content":"synthetic"}],"max_tokens":8}`))
	req.Header.Set("Authorization", "Bearer project-fixture")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 200 || calls.Load() != 1 {
		t.Fatalf("managed real relay failed: %d", w.Code)
	}
	if err := runtime.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var settled int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM budget_reservations WHERE state='settled'`).Scan(&settled); err != nil || settled != 1 {
		t.Fatal("managed inference not settled exactly once")
	}
	if err := manager.Publish(ctx, admin, 0, cfg); !errors.Is(err, provider.ErrConfigurationConflict) {
		t.Fatal("stale directory revision accepted")
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE newapi_model_configuration_events RENAME TO blocked_configuration_events`); err != nil {
		t.Fatal(err)
	}
	if manager.Publish(ctx, admin, 1, cfg) == nil {
		t.Fatal("publication succeeded without an audit record")
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE blocked_configuration_events RENAME TO newapi_model_configuration_events`); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status(ctx, admin)
	if err != nil || status.Revision != 1 {
		t.Fatal("failed audit changed directory")
	}
	oldPrice := oldRoute.PriceVersion()
	cfg.Models[0].PriceVersion = "reviewed-v2"
	if err := manager.Publish(ctx, admin, 1, cfg); err != nil {
		t.Fatal(err)
	}
	if oldRoute.PriceVersion() != oldPrice {
		t.Fatal("publication changed in-flight snapshot")
	}
	in.OperationID = "channel-disable-integration"
	in.ExpectedRevision = channel.EditRevision
	in.Enabled = new(bool)
	in.APIKey = nil
	op, err = manager.WriteChannel(ctx, admin, channel.ID, in)
	if err != nil || op.State != "applied" {
		t.Fatal("channel disable failed")
	}
	if err := manager.Publish(ctx, admin, 2, cfg); !errors.Is(err, provider.ErrConfigurationConflict) {
		t.Fatal("disabled or drifted channel approved")
	}
	if err := runtime.ReconcileOnce(ctx); err != nil {
		t.Fatal("channel change blocked immutable recovery")
	}
}
