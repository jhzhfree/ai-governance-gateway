// gateway-engine is the single-process product entrypoint.
package main

import (
	"context"
	"github.com/jhzhfree/ai-governance-gateway/internal/gatewayapp"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/modules/engine/gatewaybridge"
)

func createEngine(ctx context.Context, manager *provider.Manager, cfg gatewayapp.EngineConfig) (gatewayapp.RuntimeEngine, error) {
	engine, err := gatewaybridge.NewDurableManaged(ctx, manager, gatewaybridge.PoolConfig{MaxBindings: cfg.MaxBindings, MaxConcurrent: cfg.MaxConcurrent}, cfg.Completions)
	if err != nil {
		return nil, err
	}
	return engine, nil
}
func main() { gatewayapp.MainEmbedded(createEngine) }
