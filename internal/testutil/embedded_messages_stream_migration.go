package testutil

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
)

// RevertEmbeddedMessagesStreamMigration constructs disposable v35 fixtures only.
func RevertEmbeddedMessagesStreamMigration(t testing.TB, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `ALTER TABLE engine_execution_bindings DROP CONSTRAINT engine_execution_bindings_protocol_limits;
ALTER TABLE engine_execution_bindings ADD CONSTRAINT engine_execution_bindings_protocol_limits CHECK(
 (protocol='chat_v1' AND output_price>0 AND max_output>=16 AND cache_read_price IS NULL) OR
 (protocol='embeddings_v1' AND output_price=0 AND max_output=0 AND NOT stream AND cache_read_price IS NULL) OR
 (protocol IN ('deepseek_cache_v1','deepseek_messages_v1') AND supplier='deepseek' AND output_price>0 AND max_output>=16 AND cache_read_price IS NOT NULL AND cache_read_price BETWEEN 0 AND 9007199254740991 AND (protocol<>'deepseek_messages_v1' OR NOT stream))
); DELETE FROM aigw_schema_migrations WHERE version=36`)
	if err != nil {
		t.Fatal(err)
	}
}
