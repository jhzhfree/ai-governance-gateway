package testutil

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
)

// RevertSystemManagementMigration constructs a disposable pre-v25 fixture.
// This is not a supported production downgrade command.
func RevertSystemManagementMigration(t testing.TB, pool *pgxpool.Pool) {
	t.Helper()
	RevertNewAPIMigration(t, pool)
	if _, e := pool.Exec(context.Background(), `DROP TABLE system_management_events; DELETE FROM aigw_schema_migrations WHERE version=25`); e != nil {
		t.Fatal(e)
	}
}
