package control

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
)

type ModelCatalog interface{ Models() []provider.ModelInfo }

func NewWithCatalog(pool *pgxpool.Pool, catalog ModelCatalog) *Store {
	return &Store{pool: pool, catalog: catalog}
}
func (s *Store) Models() []provider.ModelInfo {
	if s.catalog == nil {
		return []provider.ModelInfo{provider.MockInfo()}
	}
	return s.catalog.Models()
}
func (s *Store) allowsCurrency(currency string) bool {
	for _, m := range s.Models() {
		if m.Currency == currency {
			return true
		}
	}
	return false
}
func (s *Store) validModels(models []string, empty bool) bool {
	if len(models) > 32 || (!empty && len(models) == 0) {
		return false
	}
	enabled := map[string]bool{}
	for _, m := range s.Models() {
		enabled[m.ID] = true
	}
	seen := map[string]bool{}
	for _, m := range models {
		if !enabled[m] || seen[m] {
			return false
		}
		seen[m] = true
	}
	return true
}
func (s *Store) modelCurrency(model string) string {
	for _, m := range s.Models() {
		if m.ID == model {
			return m.Currency
		}
	}
	return ""
}
