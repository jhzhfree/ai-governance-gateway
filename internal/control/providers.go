package control

import (
	"context"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
)

type ProviderManagement interface {
	Status(context.Context, identity.Principal) (provider.ConfigurationStatus, error)
	Publish(context.Context, identity.Principal, int64, provider.Config) error
	SetCredential(context.Context, identity.Principal, string, int64, string) error
}

func (s *Store) WithProviderManager(m ProviderManagement) *Store { s.providers = m; return s }
func (s *Store) ProviderConfiguration(ctx context.Context, p identity.Principal) (provider.ConfigurationStatus, error) {
	if s.providers != nil {
		return s.providers.Status(ctx, p)
	}
	suppliers := []provider.SupplierStatus{}
	for _, v := range provider.Profiles() {
		suppliers = append(suppliers, provider.SupplierStatus{ID: v.ID, Region: v.Region, Endpoint: v.Endpoint, KeyEnvironment: v.KeyEnvironment})
	}
	return provider.ConfigurationStatus{Suppliers: suppliers, Models: s.Models(), Ready: s.catalog != nil && len(s.Models()) > 0}, nil
}
func (s *Store) PublishProviders(ctx context.Context, p identity.Principal, revision int64, cfg provider.Config) error {
	if s.providers == nil {
		return identity.ErrForbidden
	}
	return s.providers.Publish(ctx, p, revision, cfg)
}

func (s *Store) SetProviderCredential(ctx context.Context, p identity.Principal, id string, expected int64, secret string) error {
	if s.providers == nil {
		return identity.ErrForbidden
	}
	return s.providers.SetCredential(ctx, p, id, expected, secret)
}
