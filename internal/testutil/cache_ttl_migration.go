package testutil

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
)

// RevertCacheTTLMigration removes only v30 in disposable upgrade fixtures.
func RevertCacheTTLMigration(t testing.TB, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `ALTER TABLE newapi_execution_bindings DROP CONSTRAINT newapi_cache_ttl_tariff_check;
 ALTER TABLE newapi_execution_bindings DROP COLUMN cache_write_5m_price,DROP COLUMN cache_write_1h_price;
 ALTER TABLE newapi_execution_receipts DROP CONSTRAINT newapi_cache_evidence_check;
 ALTER TABLE newapi_execution_receipts DROP COLUMN cache_write_5m_tokens,DROP COLUMN cache_write_1h_tokens;
 ALTER TABLE newapi_execution_receipts ADD CONSTRAINT newapi_cache_evidence_check CHECK (
 cache_details_version IN (0,1) AND cache_read_tokens BETWEEN 0 AND input_tokens AND
 cache_write_tokens BETWEEN 0 AND input_tokens-cache_read_tokens AND
 (cache_details_version=1 OR (cache_read_tokens=0 AND cache_write_tokens=0)));
 DELETE FROM aigw_schema_migrations WHERE version=30;`)
	if err != nil {
		t.Fatal(err)
	}
}
