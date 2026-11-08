package testutil

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
)

// RevertEmbeddedEmbeddingsMigration constructs only disposable released-v32 schemas.
func RevertEmbeddedEmbeddingsMigration(t testing.TB, pool *pgxpool.Pool) {
	t.Helper()
	RevertEmbeddedCacheMigration(t, pool)
	_, err := pool.Exec(context.Background(), `ALTER TABLE engine_execution_bindings DROP CONSTRAINT engine_execution_bindings_protocol_limits;
 ALTER TABLE engine_execution_bindings DROP CONSTRAINT engine_execution_bindings_protocol_check;
 ALTER TABLE engine_execution_bindings DROP CONSTRAINT engine_execution_bindings_output_price_check;
 ALTER TABLE engine_execution_bindings DROP CONSTRAINT engine_execution_bindings_max_output_check;
 ALTER TABLE engine_execution_bindings ADD CONSTRAINT engine_execution_bindings_protocol_check CHECK(protocol='chat_v1');
 ALTER TABLE engine_execution_bindings ADD CONSTRAINT engine_execution_bindings_output_price_check CHECK(output_price>0 AND output_price<=9007199254740991);
 ALTER TABLE engine_execution_bindings ADD CONSTRAINT engine_execution_bindings_max_output_check CHECK(max_output BETWEEN 16 AND 1000000);
 DELETE FROM aigw_schema_migrations WHERE version=33`)
	if err != nil {
		t.Fatal(err)
	}
}
