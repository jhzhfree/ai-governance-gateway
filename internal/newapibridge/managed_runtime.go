package newapibridge

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
)

// ManagedRuntime shares one execution limiter and one recovery worker across
// configuration versions. In-flight requests retain their immutable Route.
type ManagedRuntime struct {
	*Executor
	manager *ChannelManager
	checked atomic.Int64
	cancel  context.CancelFunc
	done    chan struct{}
}

func NewManagedRuntime(ctx context.Context, cfg Config, manager *ChannelManager) (*ManagedRuntime, error) {
	if manager == nil || cfg.Journal == nil {
		return nil, ErrInvalid
	}
	cfg.ManagedDirectory = true
	cfg.RequireInitialUsage, cfg.RequireTariff, cfg.RequireTariffV2, cfg.RequireEmbeddings = true, true, true, true
	executor, err := New(cfg)
	if err != nil {
		return nil, err
	}
	worker, cancel := context.WithCancel(ctx)
	r := &ManagedRuntime{Executor: executor, manager: manager, cancel: cancel, done: make(chan struct{})}
	r.checked.Store(-1)
	r.refresh(worker)
	go func() {
		defer close(r.done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-worker.Done():
				return
			case <-ticker.C:
				r.refresh(worker)
			}
		}
	}()
	return r, nil
}
func (r *ManagedRuntime) refresh(ctx context.Context) {
	poll, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// Recovery runs even when no directory is published or a channel has drifted.
	if err := r.ReconcileOnce(poll); err != nil {
		r.checked.Store(-1)
		return
	}
	cfg, revision, err := r.manager.read(poll, r.manager.pool)
	if err != nil || revision == 0 {
		r.checked.Store(-1)
		return
	}
	catalog, err := provider.NewGoverned(cfg)
	if err != nil {
		r.checked.Store(-1)
		return
	}
	revisions := map[int]string{}
	for _, route := range catalog.ExecutionRoutes() {
		id, rev := route.GovernanceChannelID(), route.GovernanceChannelRevision()
		if previous, ok := revisions[id]; ok && previous != rev {
			r.checked.Store(-1)
			return
		}
		revisions[id] = rev
	}
	if err := r.checkChannelRevisions(poll, revisions); err != nil {
		r.checked.Store(-1)
		return
	}
	r.checked.Store(revision)
}
func (r *ManagedRuntime) snapshot(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) *provider.Catalog {
	cfg, revision, err := r.manager.read(ctx, q)
	if err != nil || revision < 1 || r.checked.Load() != revision {
		return nil
	}
	catalog, err := provider.NewGoverned(cfg)
	if err != nil {
		return nil
	}
	return catalog
}
func (r *ManagedRuntime) Find(id string) (*provider.Route, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return r.snapshot(ctx, r.manager.pool).Find(id)
}
func (r *ManagedRuntime) Models() []provider.ModelInfo {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return r.snapshot(ctx, r.manager.pool).Models()
}
func (r *ManagedRuntime) ModelsIn(ctx context.Context, tx pgx.Tx) []provider.ModelInfo {
	if tx == nil {
		return r.Models()
	}
	return r.snapshot(ctx, tx).Models()
}
func (r *ManagedRuntime) Ready() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return r.snapshot(ctx, r.manager.pool).Ready()
}
func (r *ManagedRuntime) Close() { r.cancel(); <-r.done; r.Executor.Close(); r.checked.Store(-1) }
