package newapibridge

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
)

// Runtime owns an immutable credential-free directory and the recovery worker.
// Configuration changes require a controlled restart in this first version.
type Runtime struct {
	*Executor
	directory *provider.Catalog
	cancel    context.CancelFunc
	done      chan struct{}
	healthy   atomic.Bool
}

func NewRuntime(ctx context.Context, cfg Config, directory *provider.Catalog) (*Runtime, error) {
	if cfg.Journal == nil || directory == nil || !directory.Ready() {
		return nil, ErrInvalid
	}
	cfg.Models = make(map[string]string)
	cfg.Channels = make(map[string]int)
	cfg.Revisions = make(map[int]string)
	for _, route := range directory.ExecutionRoutes() {
		alias, model := route.ExecutionModel()
		if route.RequiresCacheEvidence() {
			cfg.RequireTariff = true
		}
		if route.API() == "embeddings" {
			cfg.RequireEmbeddings = true
		}
		cfg.Models[alias] = model
		if route.GovernanceChannelID() <= 0 {
			return nil, ErrInvalid
		}
		cfg.Channels[alias] = route.GovernanceChannelID()
		id, revision := route.GovernanceChannelID(), route.GovernanceChannelRevision()
		if previous, ok := cfg.Revisions[id]; ok && previous != revision {
			return nil, ErrInvalid
		}
		cfg.Revisions[id] = revision
	}
	executor, err := New(cfg)
	if err != nil {
		return nil, err
	}
	setup, cancel := context.WithTimeout(ctx, 15*time.Second)
	err = executor.ReconcileOnce(setup)
	cancel()
	if err != nil && !errors.Is(err, ErrRecoveryPending) {
		executor.Close()
		return nil, ErrUnavailable
	}
	worker, stop := context.WithCancel(ctx)
	runtime := &Runtime{Executor: executor, directory: directory, cancel: stop, done: make(chan struct{})}
	review, reviewCancel := context.WithTimeout(ctx, 10*time.Second)
	revisionErr := executor.CheckChannelRevisions(review)
	reviewCancel()
	runtime.healthy.Store(err == nil && revisionErr == nil)
	go func() {
		defer close(runtime.done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-worker.Done():
				return
			case <-ticker.C:
				poll, cancel := context.WithTimeout(worker, 10*time.Second)
				err := executor.ReconcileOnce(poll)
				revisionErr := executor.CheckChannelRevisions(poll)
				cancel()
				runtime.healthy.Store(err == nil && revisionErr == nil)
			}
		}
	}()
	return runtime, nil
}
func (r *Runtime) Find(id string) (*provider.Route, bool)                { return r.directory.Find(id) }
func (r *Runtime) Models() []provider.ModelInfo                          { return r.directory.Models() }
func (r *Runtime) ModelsIn(context.Context, pgx.Tx) []provider.ModelInfo { return r.Models() }
func (r *Runtime) Ready() bool                                           { return r.healthy.Load() && r.directory.Ready() }
func (r *Runtime) Close()                                                { r.cancel(); <-r.done; r.Executor.Close(); r.healthy.Store(false) }
