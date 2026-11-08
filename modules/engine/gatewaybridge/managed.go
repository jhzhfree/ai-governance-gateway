package gatewaybridge

import (
	"context"
	"github.com/jackc/pgx/v5"
	"net/http"
	"sync"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/completion"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/modules/engine/execution"
)

type SnapshotSource interface {
	ExecutionSnapshot(context.Context) (*provider.Catalog, error)
}

// Managed refreshes the database snapshot on routing acquisition, publishing
// ordered changes to its owned pool. It never serves cached routes on failure.
// Owners must install this as BOTH routing and execution, and call Close.
// No background poller or process entrypoint is installed by this constructor.
type Managed struct {
	pool       *Pool
	source     SnapshotSource
	gate       chan struct{}
	mu         sync.RWMutex
	catalog    *provider.Catalog
	generation uint64
}

func NewManaged(ctx context.Context, source SnapshotSource, cfg PoolConfig) (*Managed, error) {
	if source == nil {
		return nil, execution.ErrInvalidRequest
	}
	pool, err := NewPool(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return newManaged(source, pool), nil
}

// NewDurableManaged is the product path. The journal is required before any
// supplier binding can be published; contract-only constructors stay isolated.
func NewDurableManaged(ctx context.Context, source SnapshotSource, cfg PoolConfig, journal *completion.Store) (*Managed, error) {
	if source == nil || journal == nil {
		return nil, execution.ErrInvalidRequest
	}
	pool, err := newPool(ctx, cfg, func(ctx context.Context, route *provider.Route, n int) (ownedBinding, error) {
		b, err := NewSupplier(ctx, route, n)
		if err != nil {
			return nil, err
		}
		b.binding.journal = journal
		return b, nil
	})
	if err != nil {
		return nil, err
	}
	return newManaged(source, pool), nil
}
func newManaged(source SnapshotSource, pool *Pool) *Managed {
	return &Managed{source: source, pool: pool, gate: make(chan struct{}, 1)}
}
func (*Managed) String() string   { return "[managed execution; credentials redacted]" }
func (*Managed) GoString() string { return "[managed execution; credentials redacted]" }

// Refresh serializes READ plus publication, not only publication: a delayed old
// database read must never replace a newer credential snapshot. Failed reloads
// block new admission; existing leases keep their original lifetime.
func (m *Managed) Refresh(ctx context.Context) error {
	select {
	case m.gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-m.pool.ctx.Done():
		return execution.ErrClosed
	}
	defer func() { <-m.gate }()
	if m.pool.ctx.Err() != nil {
		return execution.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(m.pool.ctx, cancel)
	defer stop()
	catalog, err := m.source.ExecutionSnapshot(ctx)
	if err != nil || catalog == nil || !catalog.Ready() {
		m.mu.Lock()
		m.catalog = nil
		m.mu.Unlock()
		return execution.ErrUpstream
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.generation == ^uint64(0) {
		m.catalog = nil
		return execution.ErrClosed
	}
	next := m.generation + 1
	if err := m.pool.Publish(ctx, next, catalog.ExecutionRoutes()); err != nil {
		m.catalog = nil
		return err
	}
	m.generation = next
	m.catalog = catalog
	return nil
}
func (m *Managed) refresh() error {
	ctx, cancel := context.WithTimeout(m.pool.ctx, 2*time.Second)
	defer cancel()
	return m.Refresh(ctx)
}
func (m *Managed) Find(id string) (*provider.Route, bool) {
	if m.refresh() != nil {
		return nil, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.catalog.Find(id)
}
func (m *Managed) Models() []provider.ModelInfo {
	if m.refresh() != nil {
		return []provider.ModelInfo{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.catalog.Models()
}
func (m *Managed) Ready() bool {
	if m.refresh() != nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.catalog.Ready()
}
func (m *Managed) Validate(route *provider.Route, req provider.Request) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.catalog == nil || route == nil {
		return execution.ErrInvalidRequest
	}
	current, ok := m.catalog.Find(req.Model)
	if !ok || current.ExecutionIdentity() != route.ExecutionIdentity() {
		return execution.ErrInvalidRequest
	}
	return m.pool.Validate(route, req)
}
func (m *Managed) Open(ctx context.Context, route *provider.Route, req provider.Request, id, attempt string) (*http.Response, error) {
	// The same read lock spans admission into the pool, so a failed refresh cannot
	// race an unchecked old snapshot into a physical dispatch.
	m.mu.RLock()
	if m.catalog == nil || route == nil {
		m.mu.RUnlock()
		return nil, execution.ErrInvalidRequest
	}
	current, ok := m.catalog.Find(req.Model)
	if !ok || current.ExecutionIdentity() != route.ExecutionIdentity() {
		m.mu.RUnlock()
		return nil, execution.ErrInvalidRequest
	}
	// Pool admission must be separate from waiting for the first upstream byte.
	// Use the pool's admission hook to release this lock once a lease is acquired.
	return m.pool.open(ctx, route, req, id, attempt, m.mu.RUnlock)
}
func (m *Managed) Close() {
	m.pool.Close()
	// Wait for canceled source reads as well as engine resources.
	m.gate <- struct{}{}
	m.mu.Lock()
	m.catalog = nil
	m.mu.Unlock()
	<-m.gate
}

// ModelsIn must reuse an authorization transaction; it must not acquire another
// connection or publish a potentially uncommitted transactional configuration.
func (m *Managed) ModelsIn(ctx context.Context, tx pgx.Tx) []provider.ModelInfo {
	if m.pool.ctx.Err() != nil {
		return []provider.ModelInfo{}
	}
	if tx == nil {
		return m.Models()
	}
	if source, ok := m.source.(interface {
		ModelsIn(context.Context, pgx.Tx) []provider.ModelInfo
	}); ok {
		return source.ModelsIn(ctx, tx)
	}
	return []provider.ModelInfo{}
}
