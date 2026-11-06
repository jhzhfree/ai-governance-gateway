package testutil

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
)

// RevertDeepSeekMigration reconstructs v23 only in disposable upgrade fixtures.
// It must fail if a fixture already contains DeepSeek records.
func RevertDeepSeekMigration(t testing.TB, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `ALTER TABLE inference_attempts DROP CONSTRAINT inference_attempts_provider_check; ALTER TABLE inference_attempts ADD CONSTRAINT inference_attempts_provider_check CHECK(provider IN ('mock','aliyun','volcengine','zhipu'));
ALTER TABLE billing_imports DROP CONSTRAINT billing_imports_provider_check; ALTER TABLE billing_imports ADD CONSTRAINT billing_imports_provider_check CHECK(provider IN ('aliyun','volcengine','zhipu'));
ALTER TABLE billing_records DROP CONSTRAINT billing_records_provider_check; ALTER TABLE billing_records ADD CONSTRAINT billing_records_provider_check CHECK(provider IN ('aliyun','volcengine','zhipu'));
ALTER TABLE provider_credentials DROP CONSTRAINT provider_credentials_provider_check; ALTER TABLE provider_credentials ADD CONSTRAINT provider_credentials_provider_check CHECK(provider IN ('aliyun','volcengine','zhipu'));
ALTER TABLE provider_credential_events DROP CONSTRAINT provider_credential_events_provider_check; ALTER TABLE provider_credential_events ADD CONSTRAINT provider_credential_events_provider_check CHECK(provider IN ('aliyun','volcengine','zhipu'));
DELETE FROM aigw_schema_migrations WHERE version=24;`)
	if err != nil {
		t.Fatal(err)
	}
}
