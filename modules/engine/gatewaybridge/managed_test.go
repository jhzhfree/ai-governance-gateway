package gatewaybridge

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
	adapter "github.com/jhzhfree/ai-governance-gateway/modules/engine/adapters/bifrost"
	"github.com/jhzhfree/ai-governance-gateway/modules/engine/execution"
)

func TestManagedDatabaseRotationFailureAndRecovery(t *testing.T) {
	db, _, _ := testutil.Database(t)
	ctx := context.Background()
	actor := identity.Principal{Issuer: "synthetic", Subject: "owner"}
	source := testutil.MainlandManager(t, db, actor, http.NotFoundHandler())
	cfg := testutil.MainlandConfig()
	cfg.Models = cfg.Models[3:]
	cfg.Models[0].MaxOutputTokens = 32
	if err := source.SetCredential(ctx, actor, "deepseek", 0, "synthetic-old"); err != nil {
		t.Fatal(err)
	}
	if err := source.Publish(ctx, actor, 0, cfg); err != nil {
		t.Fatal(err)
	}
	var oldCalls, newCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer synthetic-old":
			oldCalls.Add(1)
		case "Bearer synthetic-new":
			newCalls.Add(1)
		default:
			t.Error("incorrect credential")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"synthetic"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	}))
	defer upstream.Close()
	pool, err := newPool(ctx, PoolConfig{2, 2}, func(ctx context.Context, r *provider.Route, n int) (ownedBinding, error) {
		b, e := newSupplier(ctx, r, n, func(ctx context.Context, cfg adapter.Config) (execution.Executor, error) {
			cfg.BaseURL = upstream.URL
			cfg.TestLoopback = true
			return adapter.New(ctx, cfg)
		})
		if e != nil {
			return nil, e
		}
		return b, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	managed := newManaged(source, pool)
	defer managed.Close()
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if models := managed.ModelsIn(ctx, tx); len(models) != 1 || pool.Stats().Version != 0 {
		t.Fatal("transactional discovery published execution state")
	}
	tx.Rollback(ctx)
	old, ok := managed.Find("deepseek/fixture")
	if !ok {
		t.Fatal("route unavailable")
	}
	before, err := managed.Open(ctx, old, poolRequest(old), "r", "a")
	if err != nil {
		t.Fatal(err)
	}
	if err := source.SetCredential(ctx, actor, "deepseek", 1, "synthetic-new"); err != nil {
		t.Fatal(err)
	}
	newer, ok := managed.Find("deepseek/fixture")
	if !ok || newer.ExecutionIdentity() == old.ExecutionIdentity() {
		t.Fatal("database credential change not published")
	}
	if managed.Validate(old, poolRequest(old)) == nil {
		t.Fatal("stale snapshot admitted")
	}
	after, err := managed.Open(ctx, newer, poolRequest(newer), "r2", "a2")
	if err != nil {
		t.Fatal(err)
	}
	for _, response := range []*http.Response{before, after} {
		b, e := io.ReadAll(response.Body)
		response.Body.Close()
		if e != nil || !strings.Contains(string(b), `"total_tokens":5`) {
			t.Fatal("reload truncated response")
		}
	}
	if oldCalls.Load() != 1 || newCalls.Load() != 1 || pool.Stats().Draining != 0 {
		t.Fatal("rotation duplicated execution or leaked instances")
	}
	// An undecryptable stored key must not retain cached routing or use env keys.
	if _, err := db.Exec(ctx, `UPDATE provider_credentials SET ciphertext=set_byte(ciphertext,0,get_byte(ciphertext,0)#1) WHERE provider='deepseek'`); err != nil {
		t.Fatal(err)
	}
	if managed.Ready() || managed.Validate(newer, poolRequest(newer)) == nil {
		t.Fatal("failed source retained admission")
	}
	if _, err := managed.Open(ctx, newer, poolRequest(newer), "r3", "a3"); err == nil {
		t.Fatal("failed source dispatched cached key")
	}
	if err := source.SetCredential(ctx, actor, "deepseek", 2, "synthetic-new"); err != nil {
		t.Fatal(err)
	}
	if !managed.Ready() {
		t.Fatal("valid new credential failed to recover")
	}
	managed.Close()
	if !errors.Is(managed.Refresh(ctx), execution.ErrClosed) {
		t.Fatal("closed controller refreshed")
	}
}

type snapshotFunc func(context.Context) (*provider.Catalog, error)

func (f snapshotFunc) ExecutionSnapshot(ctx context.Context) (*provider.Catalog, error) {
	return f(ctx)
}
func managedCatalog(t *testing.T, secret string) *provider.Catalog {
	t.Helper()
	cfg := testutil.MainlandConfig()
	cfg.Models = cfg.Models[:1]
	cfg.Models[0].MaxOutputTokens = 32
	c, err := provider.New(cfg, func(string) string { return secret }, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func TestManagedSerializesReadsAndRejectsCapacityFallback(t *testing.T) {
	old, newer, third := managedCatalog(t, "synthetic-old"), managedCatalog(t, "synthetic-new"), managedCatalog(t, "synthetic-third")
	started, unblock := make(chan struct{}), make(chan struct{})
	var reads atomic.Int32
	source := snapshotFunc(func(ctx context.Context) (*provider.Catalog, error) {
		switch reads.Add(1) {
		case 1:
			close(started)
			select {
			case <-unblock:
				return old, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		case 2:
			return newer, nil
		default:
			return third, nil
		}
	})
	pool, _ := testPool(t, PoolConfig{2, 2})
	m := newManaged(source, pool)
	defer m.Close()
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { first <- m.Refresh(context.Background()) }()
	<-started
	go func() { second <- m.Refresh(context.Background()) }()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(m.Refresh(canceled), context.Canceled) {
		t.Fatal("queued refresh ignored cancellation")
	}
	if reads.Load() != 1 {
		t.Fatal("database reads were not serialized")
	}
	close(unblock)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	a, _ := old.Find("aliyun/fixture")
	b, _ := newer.Find("aliyun/fixture")
	if m.Validate(a, poolRequest(a)) == nil || m.Validate(b, poolRequest(b)) != nil {
		t.Fatal("older read overrode newer snapshot")
	}
	response, err := m.Open(context.Background(), b, poolRequest(b), "r", "a")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	c, _ := third.Find("aliyun/fixture")
	m.source = snapshotFunc(func(context.Context) (*provider.Catalog, error) { return old, nil })
	if !errors.Is(m.Refresh(context.Background()), execution.ErrOverloaded) {
		t.Fatal("old lease did not count toward publication capacity")
	}
	if m.Validate(c, poolRequest(c)) == nil {
		t.Fatal("failed publication retained cached admission")
	}
	response.Body.Close()
	if err := m.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.Validate(a, poolRequest(a)) != nil {
		t.Fatal("capacity recovery failed")
	}
}
func TestManagedCloseCancelsSourceRead(t *testing.T) {
	started := make(chan struct{})
	source := snapshotFunc(func(ctx context.Context) (*provider.Catalog, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	pool, _ := testPool(t, PoolConfig{2, 2})
	m := newManaged(source, pool)
	done := make(chan error, 1)
	go func() { done <- m.Refresh(context.Background()) }()
	<-started
	m.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("closed refresh succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("source read leaked after close")
	}
	if pool.Stats().Current != 0 {
		t.Fatal("closed controller published resources")
	}
}
