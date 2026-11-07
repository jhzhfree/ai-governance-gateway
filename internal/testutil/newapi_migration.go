package testutil

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
)

// RevertNewAPIMigration builds an isolated pre-v26 upgrade fixture only.
func RevertNewAPIMigration(t testing.TB, pool *pgxpool.Pool) {
	t.Helper()
	RevertGovernedSuppliersMigration(t, pool)
	if _, err := pool.Exec(context.Background(), `DROP TABLE newapi_execution_receipts,newapi_execution_bindings; DELETE FROM aigw_schema_migrations WHERE version=26`); err != nil {
		t.Fatal(err)
	}
}
