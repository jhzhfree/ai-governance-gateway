package provider

import (
	"context"
	"github.com/jackc/pgx/v5"
	"sort"
)

// ExecutionSnapshot reads configuration and credentials from one committed
// database snapshot. Returned routes are immutable and never API responses.
func (m *Manager) ExecutionSnapshot(ctx context.Context) (*Catalog, error) {
	var catalog *Catalog
	err := pgx.BeginTxFunc(ctx, m.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT configuration FROM provider_configuration WHERE singleton=true`).Scan(&raw); err != nil {
			return ErrConfigurationInvalid
		}
		cfg, err := DecodeConfig(raw)
		if err != nil {
			return ErrConfigurationInvalid
		}
		getter, _, err := m.credentials(ctx, tx)
		if err != nil {
			return ErrCredentialStorageUnavailable
		}
		catalog, err = New(cfg, getter, m.transport)
		if err != nil {
			return ErrConfigurationInvalid
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return catalog, nil
}

// ExecutionRoutes exposes only in-process immutable references in stable order.
// Credentials remain private; callers must not serialize execution snapshots.
func (c *Catalog) ExecutionRoutes() []*Route {
	routes := []*Route{}
	if c == nil {
		return routes
	}
	ids := make([]string, 0, len(c.routes))
	for id := range c.routes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	// Include expired entries so atomic publication rejects the complete invalid
	// snapshot instead of silently publishing a filtered subset.
	for _, id := range ids {
		routes = append(routes, c.routes[id])
	}
	return routes
}
