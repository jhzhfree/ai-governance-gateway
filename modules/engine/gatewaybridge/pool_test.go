package gatewaybridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
	adapter "github.com/jhzhfree/ai-governance-gateway/modules/engine/adapters/bifrost"
	"github.com/jhzhfree/ai-governance-gateway/modules/engine/execution"
)

func poolRoute(t *testing.T, key string, modelIndex int) *provider.Route {
	t.Helper()
	cfg := testutil.MainlandConfig()
	for i := range cfg.Models {
		cfg.Models[i].MaxOutputTokens = 32
	}
	c, err := provider.New(cfg, func(string) string { return key }, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := c.Find(cfg.Models[modelIndex].ID)
	return r
}
func poolRequest(r *provider.Route) provider.Request {
	alias, _ := r.ExecutionModel()
	return provider.Request{Model: alias, MaxTokens: 32, Messages: []provider.Message{{Role: "user", Content: "synthetic"}}}
}

type fakeBinding struct {
	identity [32]byte
	closed   atomic.Int32
	calls    atomic.Int32
	failure  bool
}

func (f *fakeBinding) Validate(r *provider.Route, req provider.Request) error {
	if f.closed.Load() != 0 {
		return execution.ErrClosed
	}
	if r == nil || !r.Ready() || r.ExecutionIdentity() != f.identity || req.Model != r.ExecutionSettings().Supplier+"/fixture" {
		return execution.ErrInvalidRequest
	}
	return nil
}
func (f *fakeBinding) Open(ctx context.Context, r *provider.Route, req provider.Request, id, attempt string) (*http.Response, error) {
	if err := f.Validate(r, req); err != nil {
		return nil, err
	}
	f.calls.Add(1)
	if f.failure {
		return nil, execution.ErrUpstream
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("synthetic"))}, nil
}
func (f *fakeBinding) Close() { f.closed.Add(1) }
func testPool(t *testing.T, cfg PoolConfig) (*Pool, *[]*fakeBinding) {
	t.Helper()
	created := []*fakeBinding{}
	p, err := newPool(context.Background(), cfg, func(_ context.Context, r *provider.Route, _ int) (ownedBinding, error) {
		f := &fakeBinding{identity: r.ExecutionIdentity()}
		created = append(created, f)
		return f, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p, &created
}
func publish(t *testing.T, p *Pool, v uint64, routes ...*provider.Route) {
	t.Helper()
	if err := p.Publish(context.Background(), v, routes); err != nil {
		t.Fatal(err)
	}
}
func openPool(t *testing.T, p *Pool, r *provider.Route) *http.Response {
	t.Helper()
	resp, err := p.Open(context.Background(), r, poolRequest(r), "r", "a")
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
func TestPoolRotationDrainsAndBoundsResources(t *testing.T) {
	p, created := testPool(t, PoolConfig{MaxBindings: 2, MaxConcurrent: 2})
	old, newer, third := poolRoute(t, "synthetic-one", 0), poolRoute(t, "synthetic-two", 0), poolRoute(t, "synthetic-three", 0)
	publish(t, p, 1, old)
	body := openPool(t, p, old)
	publish(t, p, 2, newer)
	if p.Stats().Draining != 1 || (*created)[0].closed.Load() != 0 {
		t.Fatal("rotation interrupted existing lease")
	}
	if p.Validate(old, poolRequest(old)) == nil {
		t.Fatal("retired credential admitted new work")
	}
	fresh := openPool(t, p, newer)
	if _, err := p.Open(context.Background(), newer, poolRequest(newer), "r2", "a2"); !errors.Is(err, execution.ErrOverloaded) {
		t.Fatal("global capacity exceeded")
	}
	if err := p.Publish(context.Background(), 3, []*provider.Route{third}); !errors.Is(err, execution.ErrOverloaded) || p.Stats().Version != 2 {
		t.Fatal("draining resources escaped bound")
	}
	if _, err := io.ReadAll(body.Body); err != nil {
		t.Fatal(err)
	}
	body.Body.Close()
	body.Body.Close()
	if (*created)[0].closed.Load() != 1 || p.Stats().Draining != 0 || p.Stats().Active != 1 {
		t.Fatal("EOF did not reclaim retired lease exactly once")
	}
	fresh.Body.Close()
	publish(t, p, 3, third)
	if err := p.Publish(context.Background(), 2, []*provider.Route{old}); !errors.Is(err, ErrStalePublication) {
		t.Fatal("stale publish rolled credentials back")
	}
	publish(t, p, 4)
	if p.Stats().Current != 0 || p.Stats().Draining != 0 || p.Validate(third, poolRequest(third)) == nil {
		t.Fatal("route removal retained admission/resources")
	}
	for _, e := range *created {
		if e.closed.Load() != 1 {
			t.Fatal("instance not closed exactly once")
		}
	}
}
func TestPoolReusesSnapshotsAndPublicationFailureIsAtomic(t *testing.T) {
	p, created := testPool(t, PoolConfig{MaxBindings: 4, MaxConcurrent: 1})
	old := poolRoute(t, "synthetic-one", 0)
	publish(t, p, 1, old)
	publish(t, p, 2, old)
	if len(*created) != 1 {
		t.Fatal("identical snapshot recreated instance")
	}
	staged := []*fakeBinding{}
	calls := 0
	p.factory = func(_ context.Context, r *provider.Route, _ int) (ownedBinding, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("sensitive synthetic provider error")
		}
		e := &fakeBinding{identity: r.ExecutionIdentity()}
		staged = append(staged, e)
		return e, nil
	}
	err := p.Publish(context.Background(), 3, []*provider.Route{poolRoute(t, "synthetic-new", 0), poolRoute(t, "synthetic-new", 1)})
	if !errors.Is(err, execution.ErrUpstream) || strings.Contains(err.Error(), "sensitive") || p.Stats().Version != 2 || p.Stats().Building != 0 {
		t.Fatal("failed batch exposed partial publication")
	}
	if len(staged) != 1 || staged[0].closed.Load() != 1 {
		t.Fatal("failed publication leaked staged engine")
	}
	resp := openPool(t, p, old)
	resp.Body.Close()
	if p.Publish(context.Background(), 3, []*provider.Route{old, old}) == nil {
		t.Fatal("duplicate aliases accepted")
	}
}
func TestPoolCancellationFailureAndShutdownReleaseLeases(t *testing.T) {
	p, created := testPool(t, PoolConfig{MaxBindings: 2, MaxConcurrent: 1})
	r := poolRoute(t, "synthetic", 0)
	publish(t, p, 1, r)
	(*created)[0].failure = true
	if _, err := p.Open(context.Background(), r, poolRequest(r), "r", "a"); err == nil || p.Stats().Active != 0 {
		t.Fatal("open failure leaked lease")
	}
	(*created)[0].failure = false
	ctx, cancel := context.WithCancel(context.Background())
	resp, err := p.Open(ctx, r, poolRequest(r), "r", "a")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	waitPoolIdle(t, p)
	resp.Body.Close()
	_ = openPool(t, p, r) // Owner shutdown must reclaim even an unread body.
	done := make(chan struct{})
	go func() { p.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown blocked on unread body")
	}
	p.Close()
	if s := p.Stats(); !s.Closed || s.Active != 0 || s.Current != 0 || (*created)[0].closed.Load() != 1 {
		t.Fatal("shutdown leaked resources")
	}
	if p.Validate(r, poolRequest(r)) != execution.ErrClosed || p.Publish(context.Background(), 2, nil) != execution.ErrClosed {
		t.Fatal("closed pool admitted work")
	}
}
func waitPoolIdle(t *testing.T, p *Pool) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for p.Stats().Active != 0 {
		select {
		case <-deadline.C:
			t.Fatal("lease not reclaimed")
		case <-tick.C:
		}
	}
}
func TestPoolCanceledBuildNeverPublishes(t *testing.T) {
	started := make(chan struct{})
	p, err := newPool(context.Background(), PoolConfig{2, 1}, func(ctx context.Context, _ *provider.Route, _ int) (ownedBinding, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	done := make(chan error, 1)
	go func() { done <- p.Publish(context.Background(), 1, []*provider.Route{poolRoute(t, "synthetic", 0)}) }()
	<-started
	p.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled build published")
		}
	case <-time.After(time.Second):
		t.Fatal("publisher did not stop")
	}
	if s := p.Stats(); s.Version != 0 || s.Building != 0 || s.Current != 0 {
		t.Fatal("canceled publication leaked state")
	}
}
func TestPoolConcurrentAcquisitionAndPublication(t *testing.T) {
	p, err := newPool(context.Background(), PoolConfig{MaxBindings: 8, MaxConcurrent: 8}, func(_ context.Context, r *provider.Route, _ int) (ownedBinding, error) {
		return &fakeBinding{identity: r.ExecutionIdentity()}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	r := poolRoute(t, "synthetic", 0)
	publish(t, p, 1, r)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				resp, err := p.Open(context.Background(), r, poolRequest(r), "r", "a")
				if err == nil {
					io.ReadAll(resp.Body)
					resp.Body.Close()
				} else if !errors.Is(err, execution.ErrOverloaded) {
					t.Error(err)
				}
			}
		}()
	}
	for v := uint64(2); v < 52; v++ {
		publish(t, p, v, r)
	}
	wg.Wait()
	if s := p.Stats(); s.Active != 0 || s.Current != 1 || s.Draining != 0 {
		t.Fatal("concurrent access leaked resources")
	}
}
func TestPoolRealSupplierRotationKeepsInFlightCredential(t *testing.T) {
	var oldCalls, newCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer synthetic-old":
			oldCalls.Add(1)
		case "Bearer synthetic-new":
			newCalls.Add(1)
		default:
			t.Error("incorrect credential")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"synthetic"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	}))
	defer server.Close()
	p, err := newPool(context.Background(), PoolConfig{2, 2}, func(ctx context.Context, r *provider.Route, n int) (ownedBinding, error) {
		return newSupplier(ctx, r, n, func(ctx context.Context, cfg adapter.Config) (execution.Executor, error) {
			cfg.BaseURL = server.URL
			cfg.TestLoopback = true
			return adapter.New(ctx, cfg)
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	old, newer := poolRoute(t, "synthetic-old", 3), poolRoute(t, "synthetic-new", 3)
	publish(t, p, 1, old)
	before := openPool(t, p, old)
	publish(t, p, 2, newer)
	after := openPool(t, p, newer)
	for _, r := range []*http.Response{before, after} {
		b, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil || !strings.Contains(string(b), `"total_tokens":5`) {
			t.Fatal("rotation truncated validated response")
		}
	}
	if oldCalls.Load() != 1 || newCalls.Load() != 1 || p.Stats().Draining != 0 || p.Stats().Active != 0 {
		t.Fatal("rotation duplicated dispatch or leaked old instance")
	}
}

func TestPoolQueuedPublicationHonorsCancellation(t *testing.T) {
	started := make(chan struct{})
	r := poolRoute(t, "synthetic", 0)
	p, err := newPool(context.Background(), PoolConfig{2, 1}, func(ctx context.Context, _ *provider.Route, _ int) (ownedBinding, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	done := make(chan error, 1)
	go func() { done <- p.Publish(context.Background(), 1, []*provider.Route{r}) }()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	queued := make(chan error, 1)
	go func() { queued <- p.Publish(ctx, 2, []*provider.Route{r}) }()
	select {
	case err := <-queued:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled publisher blocked behind build")
	}
	p.Close()
	<-done
}
