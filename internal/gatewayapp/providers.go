package gatewayapp

import (
	"context"

	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
)

// embeddedProviders retains the manager's authorization and credential writes.
// A valid stored configuration alone does not prove the execution pool is ready.
type embeddedProviders struct {
	control.ProviderManagement
	engine RuntimeEngine
}

func (m embeddedProviders) Status(ctx context.Context, actor identity.Principal) (provider.ConfigurationStatus, error) {
	status, err := m.ProviderManagement.Status(ctx, actor)
	if err != nil {
		return provider.ConfigurationStatus{}, err
	}
	status.ExecutionEngine = "bifrost"
	status.Ready = status.Ready && m.engine.Ready()
	return status, nil
}
