package testutil

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
)

// RevertEmbeddingMigration restores only the new v28 layer in disposable tests.
func RevertEmbeddingMigration(t testing.TB, pool *pgxpool.Pool) {
	t.Helper()
	RevertCacheTariffMigration(t, pool)
	_, err := pool.Exec(context.Background(), `ALTER TABLE newapi_execution_bindings DROP CONSTRAINT newapi_execution_bindings_output_pair_check;
 ALTER TABLE newapi_execution_bindings DROP CONSTRAINT newapi_execution_bindings_output_price_check;
 ALTER TABLE newapi_execution_bindings DROP CONSTRAINT newapi_execution_bindings_max_output_check;
 ALTER TABLE newapi_execution_bindings ADD CONSTRAINT newapi_execution_bindings_output_price_check CHECK(output_price>0 AND output_price<=9007199254740991);
 ALTER TABLE newapi_execution_bindings ADD CONSTRAINT newapi_execution_bindings_max_output_check CHECK(max_output BETWEEN 1 AND 1000000);
 DELETE FROM aigw_schema_migrations WHERE version=28;`)
	if err != nil {
		t.Fatal(err)
	}
}
