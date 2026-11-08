-- Extend the released completion journal without rewriting Chat history.
ALTER TABLE engine_execution_bindings DROP CONSTRAINT engine_execution_bindings_protocol_check;
ALTER TABLE engine_execution_bindings DROP CONSTRAINT engine_execution_bindings_output_price_check;
ALTER TABLE engine_execution_bindings DROP CONSTRAINT engine_execution_bindings_max_output_check;
ALTER TABLE engine_execution_bindings ADD CONSTRAINT engine_execution_bindings_protocol_check CHECK(protocol IN ('chat_v1','embeddings_v1'));
ALTER TABLE engine_execution_bindings ADD CONSTRAINT engine_execution_bindings_output_price_check CHECK(output_price>=0 AND output_price<=9007199254740991);
ALTER TABLE engine_execution_bindings ADD CONSTRAINT engine_execution_bindings_max_output_check CHECK(max_output BETWEEN 0 AND 1000000);
ALTER TABLE engine_execution_bindings ADD CONSTRAINT engine_execution_bindings_protocol_limits CHECK(
 (protocol='chat_v1' AND output_price>0 AND max_output>=16) OR
 (protocol='embeddings_v1' AND output_price=0 AND max_output=0 AND NOT stream)
);
