// gateway-engine adds an explicitly selected execution engine to the shared app.
package main

import (
	"context"
	"github.com/jhzhfree/ai-governance-gateway/internal/gatewayapp"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/modules/engine/gatewaybridge"
)

func createEngine(ctx context.Context, manager *provider.Manager, cfg gatewayapp.EngineConfig) (gatewayapp.RuntimeEngine, error) {
	engine, err := gatewaybridge.NewManaged(ctx, manager, gatewaybridge.PoolConfig{MaxBindings: cfg.MaxBindings, MaxConcurrent: cfg.MaxConcurrent})
	if err != nil {
		return nil, err
	}
	return engine, nil
}
func main() { gatewayapp.Main(createEngine) }
