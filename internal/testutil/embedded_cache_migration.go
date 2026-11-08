package testutil

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
)

// RevertEmbeddedCacheMigration constructs only disposable released-v33 schemas.
func RevertEmbeddedCacheMigration(t testing.TB, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `ALTER TABLE engine_execution_bindings DROP CONSTRAINT engine_execution_bindings_protocol_check;
 ALTER TABLE engine_execution_bindings DROP CONSTRAINT engine_execution_bindings_protocol_limits;
 ALTER TABLE engine_execution_bindings DROP COLUMN cache_read_price;
 ALTER TABLE engine_execution_bindings ADD CONSTRAINT engine_execution_bindings_protocol_check CHECK(protocol IN ('chat_v1','embeddings_v1'));
 ALTER TABLE engine_execution_bindings ADD CONSTRAINT engine_execution_bindings_protocol_limits CHECK((protocol='chat_v1' AND output_price>0 AND max_output>=16) OR (protocol='embeddings_v1' AND output_price=0 AND max_output=0 AND NOT stream));
 ALTER TABLE engine_completion_events DROP CONSTRAINT engine_completion_events_cache_counts;
 ALTER TABLE engine_completion_events DROP COLUMN cache_read_tokens, DROP COLUMN uncached_input_tokens;
 DELETE FROM aigw_schema_migrations WHERE version=34`)
	if err != nil {
		t.Fatal(err)
	}
}
