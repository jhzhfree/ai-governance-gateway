-- Native Messages has a distinct original four-counter contract. Initial
-- rollout is JSON-only; stream admission needs its own evidence acceptance.
ALTER TABLE engine_execution_bindings DROP CONSTRAINT engine_execution_bindings_protocol_check;
ALTER TABLE engine_execution_bindings DROP CONSTRAINT engine_execution_bindings_protocol_limits;
ALTER TABLE engine_execution_bindings ADD CONSTRAINT engine_execution_bindings_protocol_check CHECK(protocol IN ('chat_v1','embeddings_v1','deepseek_cache_v1','deepseek_messages_v1'));
ALTER TABLE engine_execution_bindings ADD CONSTRAINT engine_execution_bindings_protocol_limits CHECK(
 (protocol='chat_v1' AND output_price>0 AND max_output>=16 AND cache_read_price IS NULL) OR
 (protocol='embeddings_v1' AND output_price=0 AND max_output=0 AND NOT stream AND cache_read_price IS NULL) OR
 (protocol IN ('deepseek_cache_v1','deepseek_messages_v1') AND supplier='deepseek' AND output_price>0 AND max_output>=16 AND cache_read_price IS NOT NULL AND cache_read_price BETWEEN 0 AND 9007199254740991 AND (protocol<>'deepseek_messages_v1' OR NOT stream))
);
ALTER TABLE engine_completion_events ADD COLUMN cache_write_tokens bigint;
ALTER TABLE engine_completion_events ADD CONSTRAINT engine_completion_events_cache_writes CHECK(cache_write_tokens IS NULL OR (cache_write_tokens=0 AND cache_read_tokens IS NOT NULL AND uncached_input_tokens IS NOT NULL));
