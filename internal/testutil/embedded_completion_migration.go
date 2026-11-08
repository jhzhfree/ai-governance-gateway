package testutil

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
)

// RevertEmbeddedCompletionMigration constructs only disposable v31 test schemas.
func RevertEmbeddedCompletionMigration(t testing.TB, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `DROP TABLE engine_completion_receipts,engine_completion_events,engine_execution_bindings; DELETE FROM aigw_schema_migrations WHERE version IN (32,33,34)`); err != nil {
		t.Fatal(err)
	}
}
