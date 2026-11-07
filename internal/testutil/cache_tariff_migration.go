package testutil

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
)

// RevertCacheTariffMigration restores only v29 in isolated legacy-schema tests.
func RevertCacheTariffMigration(t testing.TB, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `ALTER TABLE newapi_execution_bindings DROP CONSTRAINT newapi_cache_tariff_check;
 ALTER TABLE newapi_execution_bindings DROP COLUMN cache_read_price,DROP COLUMN cache_write_price;
 ALTER TABLE newapi_execution_receipts DROP CONSTRAINT newapi_cache_evidence_check;
 ALTER TABLE newapi_execution_receipts DROP COLUMN cache_details_version,DROP COLUMN cache_read_tokens,DROP COLUMN cache_write_tokens;
 DELETE FROM aigw_schema_migrations WHERE version=29;`)
	if err != nil {
		t.Fatal(err)
	}
}
