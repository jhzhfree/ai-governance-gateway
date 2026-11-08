-- Preserve old flat Chat/embedding histories; only explicit DeepSeek hit/miss
-- evidence opts into a frozen read tariff. No synthetic write counter is stored.
ALTER TABLE engine_execution_bindings ADD COLUMN cache_read_price bigint;
ALTER TABLE engine_execution_bindings DROP CONSTRAINT engine_execution_bindings_protocol_check;
ALTER TABLE engine_execution_bindings DROP CONSTRAINT engine_execution_bindings_protocol_limits;
ALTER TABLE engine_execution_bindings ADD CONSTRAINT engine_execution_bindings_protocol_check CHECK(protocol IN ('chat_v1','embeddings_v1','deepseek_cache_v1'));
ALTER TABLE engine_execution_bindings ADD CONSTRAINT engine_execution_bindings_protocol_limits CHECK(
 (protocol='chat_v1' AND output_price>0 AND max_output>=16 AND cache_read_price IS NULL) OR
 (protocol='embeddings_v1' AND output_price=0 AND max_output=0 AND NOT stream AND cache_read_price IS NULL) OR
 (protocol='deepseek_cache_v1' AND supplier='deepseek' AND output_price>0 AND max_output>=16 AND cache_read_price IS NOT NULL AND cache_read_price BETWEEN 0 AND 9007199254740991)
);
ALTER TABLE engine_completion_events ADD COLUMN cache_read_tokens bigint;
ALTER TABLE engine_completion_events ADD COLUMN uncached_input_tokens bigint;
ALTER TABLE engine_completion_events ADD CONSTRAINT engine_completion_events_cache_counts CHECK(
 (cache_read_tokens IS NULL AND uncached_input_tokens IS NULL) OR
 (cache_read_tokens IS NOT NULL AND uncached_input_tokens IS NOT NULL AND cache_read_tokens BETWEEN 0 AND input_tokens AND uncached_input_tokens=input_tokens-cache_read_tokens)
);
