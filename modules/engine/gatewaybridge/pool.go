package gatewaybridge

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/jhzhfree/ai-governance-gateway/internal/gateway"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/modules/engine/execution"
)

var ErrStalePublication = errors.New("execution publication version is not newer")

type PoolConfig struct {
	// MaxBindings includes current, draining and building instances. Leave space
	// for a new snapshot while the old one drains; publication never evicts work.
	MaxBindings int
	// MaxConcurrent bounds all requests, including unread response bodies.
	MaxConcurrent int
}
type ownedBinding interface {
	gateway.ProviderExecutor
	Close()
}
type bindingFactory func(context.Context, *provider.Route, int) (ownedBinding, error)
type poolEntry struct {
	binding  ownedBinding
	identity [32]byte
	refs     int
	retired  bool
	cancel   context.CancelFunc
	once     sync.Once
}

func (e *poolEntry) close() { e.once.Do(func() { e.cancel(); e.binding.Close() }) }

// Pool publishes complete immutable route sets. Its owner supplies monotonically
// increasing publication generations; this is not a database revision watcher.
// Pools own resources, never auth, durable money, retries or routing fallbacks.
type Pool struct {
	mu               sync.Mutex
	publishGate      chan struct{}
	once             sync.Once
	leases           sync.WaitGroup
	ctx              context.Context
	cancel           context.CancelFunc
	config           PoolConfig
	factory          bindingFactory
	version          uint64
	current          map[string]*poolEntry
	all              map[*poolEntry]struct{}
	building, active int
	closed           bool
}

var _ gateway.ProviderExecutor = (*Pool)(nil)

func NewPool(ctx context.Context, cfg PoolConfig) (*Pool, error) {
	return newPool(ctx, cfg, func(ctx context.Context, r *provider.Route, n int) (ownedBinding, error) {
		binding, err := NewSupplier(ctx, r, n)
		if err != nil {
			return nil, err
		}
		return binding, nil
	})
}
func newPool(ctx context.Context, cfg PoolConfig, factory bindingFactory) (*Pool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cfg.MaxBindings < 1 || cfg.MaxBindings > 256 || cfg.MaxConcurrent < 1 || cfg.MaxConcurrent > 1024 || factory == nil {
		return nil, execution.ErrInvalidRequest
	}
	ctx, cancel := context.WithCancel(ctx)
	return &Pool{ctx: ctx, cancel: cancel, config: cfg, factory: factory, publishGate: make(chan struct{}, 1), current: map[string]*poolEntry{}, all: map[*poolEntry]struct{}{}}, nil
}
func (*Pool) String() string   { return "[execution pool; credentials redacted]" }
func (*Pool) GoString() string { return "[execution pool; credentials redacted]" }

type PoolStats struct {
	Version                             uint64
	Current, Draining, Building, Active int
	Closed                              bool
}

func (p *Pool) Stats() PoolStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return PoolStats{p.version, len(p.current), len(p.all) - len(p.current), p.building, p.active, p.closed || p.ctx.Err() != nil}
}

// Publish builds replacements before atomically switching the whole routing set.
// Failure retains the previous version. Removed/changed entries reject new work
// immediately, while acquired leases drain. An empty set disables all routes.
func (p *Pool) Publish(ctx context.Context, version uint64, routes []*provider.Route) error {
	select {
	case p.publishGate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-p.ctx.Done():
		return execution.ErrClosed
	}
	defer func() { <-p.publishGate }()
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	if p.closed || p.ctx.Err() != nil {
		p.mu.Unlock()
		return execution.ErrClosed
	}
	if version <= p.version {
		p.mu.Unlock()
		return ErrStalePublication
	}
	next := make(map[string]*poolEntry, len(routes))
	needed := make(map[string]*provider.Route)
	for _, r := range routes {
		if r == nil || !r.Ready() || (r.API() != "embeddings" && r.ExecutionSettings().MaxOutputTokens < 16) {
			p.mu.Unlock()
			return execution.ErrInvalidRequest
		}
		alias, _ := r.ExecutionModel()
		if _, ok := next[alias]; ok {
			p.mu.Unlock()
			return execution.ErrInvalidRequest
		}
		e := p.current[alias]
		if e == nil || e.identity != r.ExecutionIdentity() {
			e = nil
			needed[alias] = r
		}
		next[alias] = e
	}
	if len(p.all)+len(needed) > p.config.MaxBindings {
		p.mu.Unlock()
		return execution.ErrOverloaded
	}
	p.building = len(needed)
	p.mu.Unlock()
	built := make([]*poolEntry, 0, len(needed))
	committed := false
	defer func() {
		if !committed {
			for _, e := range built {
				e.close()
			}
		}
		p.mu.Lock()
		p.building = 0
		p.mu.Unlock()
	}()
	for alias, r := range needed {
		if err := ctx.Err(); err != nil {
			return err
		}
		lifetime, cancel := context.WithCancel(p.ctx)
		stop := context.AfterFunc(ctx, cancel)
		binding, err := p.factory(lifetime, r, p.config.MaxConcurrent)
		stop()
		if err != nil {
			cancel()
			if binding != nil {
				binding.Close()
			}
			return execution.ErrUpstream
		}
		if binding == nil {
			cancel()
			return execution.ErrUpstream
		}
		e := &poolEntry{binding: binding, identity: r.ExecutionIdentity(), cancel: cancel}
		built = append(built, e)
		next[alias] = e
	}
	p.mu.Lock()
	if p.closed || p.ctx.Err() != nil {
		p.mu.Unlock()
		return execution.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		p.mu.Unlock()
		return err
	}
	// A review may expire while building a batch.
	for _, r := range routes {
		if !r.Ready() {
			p.mu.Unlock()
			return execution.ErrInvalidRequest
		}
	}
	idle := []*poolEntry{}
	for alias, e := range p.current {
		if next[alias] != e {
			e.retired = true
			if e.refs == 0 {
				idle = append(idle, e)
			}
		}
	}
	for _, e := range built {
		p.all[e] = struct{}{}
	}
	p.current = next
	p.version = version
	p.building = 0
	committed = true
	p.mu.Unlock()
	for _, e := range idle {
		p.reclaim(e)
	}
	return nil
}
func (p *Pool) reclaim(e *poolEntry) {
	e.close()
	p.mu.Lock()
	delete(p.all, e)
	p.mu.Unlock()
}
func (p *Pool) entryLocked(route *provider.Route) (*poolEntry, error) {
	if p.closed || p.ctx.Err() != nil {
		return nil, execution.ErrClosed
	}
	if route == nil {
		return nil, execution.ErrInvalidRequest
	}
	alias, _ := route.ExecutionModel()
	e := p.current[alias]
	if e == nil || e.identity != route.ExecutionIdentity() {
		return nil, execution.ErrInvalidRequest
	}
	return e, nil
}
func (p *Pool) validateLocked(route *provider.Route, req provider.Request) (*poolEntry, error) {
	e, err := p.entryLocked(route)
	if err != nil {
		return nil, err
	}
	if err := e.binding.Validate(route, req); err != nil {
		return nil, err
	}
	return e, nil
}
func (p *Pool) Validate(route *provider.Route, req provider.Request) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.validateLocked(route, req)
	return err
}
func (p *Pool) SupportsPublicProtocol(route *provider.Route, req provider.Request, path string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, err := p.entryLocked(route)
	if err != nil {
		return false
	}
	binding, ok := e.binding.(gateway.PublicProtocolExecutor)
	return ok && binding.SupportsPublicProtocol(route, req, path)
}
func (p *Pool) Open(ctx context.Context, route *provider.Route, req provider.Request, id, attempt string) (*http.Response, error) {
	return p.open(ctx, route, req, id, attempt, func() {})
}

// admitted is called exactly once, after lease acquisition or admission failure.
func (p *Pool) open(ctx context.Context, route *provider.Route, req provider.Request, id, attempt string, admitted func()) (*http.Response, error) {
	var admission sync.Once
	releaseAdmission := func() { admission.Do(admitted) }
	defer releaseAdmission()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	e, err := p.validateLocked(route, req)
	if err != nil {
		p.mu.Unlock()
		return nil, err
	}
	if p.active >= p.config.MaxConcurrent {
		p.mu.Unlock()
		return nil, execution.ErrOverloaded
	}
	p.active++
	e.refs++
	p.leases.Add(1)
	p.mu.Unlock()
	releaseAdmission()
	ctx, cancel := context.WithCancel(ctx)
	stopPool := context.AfterFunc(p.ctx, cancel)
	release := func() {
		stopPool()
		cancel()
		p.mu.Lock()
		p.active--
		e.refs--
		retired := e.retired && e.refs == 0
		p.mu.Unlock()
		if retired {
			p.reclaim(e)
		}
		p.leases.Done()
	}
	response, err := e.binding.Open(ctx, route, req, id, attempt)
	if err != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		release()
		return nil, err
	}
	if response == nil || response.Body == nil {
		release()
		return nil, execution.ErrUpstream
	}
	body := &leasedBody{inner: response.Body, release: release}
	body.attachStop(context.AfterFunc(ctx, func() { _ = body.Close() }))
	response.Body = body
	return response, nil
}
func (p *Pool) Close() {
	p.once.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.cancel()
		p.mu.Unlock()
		// A canceled publication must dispose of all staged instances before return.
		p.publishGate <- struct{}{}
		<-p.publishGate
		p.mu.Lock()
		entries := make([]*poolEntry, 0, len(p.all))
		for e := range p.all {
			entries = append(entries, e)
		}
		p.mu.Unlock()
		for _, e := range entries {
			e.close()
		}
		p.leases.Wait()
		p.mu.Lock()
		clear(p.current)
		clear(p.all)
		p.mu.Unlock()
	})
}

type leasedBody struct {
	inner   io.ReadCloser
	release func()
	once    sync.Once
	mu      sync.Mutex
	stop    func() bool
	closed  bool
	err     error
}

func (b *leasedBody) attachStop(stop func() bool) {
	b.mu.Lock()
	b.stop = stop
	closed := b.closed
	b.mu.Unlock()
	if closed {
		stop()
	}
}
func (b *leasedBody) Read(buf []byte) (int, error) {
	n, err := b.inner.Read(buf)
	if err != nil {
		_ = b.Close()
	}
	return n, err
}
func (b *leasedBody) Close() error {
	b.once.Do(func() {
		b.mu.Lock()
		b.closed = true
		stop := b.stop
		b.mu.Unlock()
		if stop != nil {
			stop()
		}
		b.err = b.inner.Close()
		b.release()
	})
	return b.err
}
