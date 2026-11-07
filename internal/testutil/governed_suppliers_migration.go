package testutil

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
)

// RevertGovernedSuppliersMigration reconstructs v26 only in isolated fixtures.
func RevertGovernedSuppliersMigration(t testing.TB, pool *pgxpool.Pool) {
	t.Helper()
	RevertEmbeddingMigration(t, pool)
	_, err := pool.Exec(context.Background(), `
 ALTER TABLE inference_attempts DROP CONSTRAINT inference_attempts_provider_check;
 ALTER TABLE inference_attempts ADD CONSTRAINT inference_attempts_provider_check CHECK(provider IN ('mock','aliyun','volcengine','zhipu','deepseek'));
 ALTER TABLE billing_imports DROP CONSTRAINT billing_imports_provider_check;
 ALTER TABLE billing_imports ADD CONSTRAINT billing_imports_provider_check CHECK(provider IN ('aliyun','volcengine','zhipu','deepseek'));
 ALTER TABLE billing_records DROP CONSTRAINT billing_records_provider_check;
 ALTER TABLE billing_records ADD CONSTRAINT billing_records_provider_check CHECK(provider IN ('aliyun','volcengine','zhipu','deepseek'));
 DELETE FROM aigw_schema_migrations WHERE version=27;
 `)
	if err != nil {
		t.Fatal(err)
	}
}
