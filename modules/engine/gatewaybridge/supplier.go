package gatewaybridge

import (
	"context"
	"net/http"
	"sync/atomic"

	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	adapter "github.com/jhzhfree/ai-governance-gateway/modules/engine/adapters/bifrost"
	"github.com/jhzhfree/ai-governance-gateway/modules/engine/execution"
)

// SupplierBinding owns one immutable route/credential snapshot. Pooling and
// publication/rotation orchestration remain the caller's responsibility.
type SupplierBinding struct {
	binding *Binding
	engine  execution.Executor
	closed  atomic.Bool
}

func NewSupplier(ctx context.Context, route *provider.Route, maxConcurrent int) (*SupplierBinding, error) {
	return newSupplier(ctx, route, maxConcurrent, func(ctx context.Context, cfg adapter.Config) (execution.Executor, error) {
		return adapter.New(ctx, cfg)
	})
}
func newSupplier(ctx context.Context, route *provider.Route, maxConcurrent int, create func(context.Context, adapter.Config) (execution.Executor, error)) (*SupplierBinding, error) {
	if route == nil || !route.Ready() {
		return nil, execution.ErrInvalidRequest
	}
	settings := route.ExecutionSettings()
	if settings.MaxOutputTokens < 16 {
		return nil, execution.ErrInvalidRequest
	}
	engine, err := create(ctx, adapter.Config{Supplier: settings.Supplier, Model: settings.Model, APIKey: settings.APIKey, MaxOutputTokens: settings.MaxOutputTokens, MaxConcurrent: maxConcurrent})
	if err != nil {
		return nil, err
	}
	if engine == nil {
		return nil, execution.ErrInvalidRequest
	}
	binding, err := New(route, engine)
	if err != nil {
		engine.Close()
		return nil, err
	}
	return &SupplierBinding{binding: binding, engine: engine}, nil
}
func (b *SupplierBinding) Validate(route *provider.Route, req provider.Request) error {
	if b.closed.Load() {
		return execution.ErrClosed
	}
	return b.binding.Validate(route, req)
}
func (b *SupplierBinding) SupportsPublicProtocol(route *provider.Route, req provider.Request, path string) bool {
	return !b.closed.Load() && b.binding.SupportsPublicProtocol(route, req, path)
}
func (b *SupplierBinding) Open(ctx context.Context, route *provider.Route, req provider.Request, id, attempt string) (*http.Response, error) {
	if err := b.Validate(route, req); err != nil {
		return nil, err
	}
	return b.binding.Open(ctx, route, req, id, attempt)
}
func (b *SupplierBinding) Close() { b.closed.Store(true); b.engine.Close() }

func (*SupplierBinding) String() string   { return "[supplier binding; credentials redacted]" }
func (*SupplierBinding) GoString() string { return "[supplier binding; credentials redacted]" }
