package testutil

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
)

// RevertChannelManagementMigration is limited to disposable upgrade fixtures.
func RevertChannelManagementMigration(t testing.TB, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `DROP TABLE newapi_channel_operations,newapi_model_configuration,newapi_model_configuration_events; DELETE FROM aigw_schema_migrations WHERE version=31`); err != nil {
		t.Fatal(err)
	}
}
