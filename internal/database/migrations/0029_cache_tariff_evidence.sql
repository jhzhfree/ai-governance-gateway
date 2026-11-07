-- Reviewed cache tariffs are immutable per execution. Existing uniform tariffs
-- remain NULL and must never be retrospectively repriced.
ALTER TABLE newapi_execution_bindings ADD COLUMN cache_read_price bigint;
ALTER TABLE newapi_execution_bindings ADD COLUMN cache_write_price bigint;
ALTER TABLE newapi_execution_bindings ADD CONSTRAINT newapi_cache_tariff_check CHECK (
 (cache_read_price IS NULL AND cache_write_price IS NULL) OR
 (cache_read_price IS NOT NULL AND cache_write_price IS NOT NULL AND
  cache_read_price BETWEEN 0 AND 9007199254740991 AND cache_write_price BETWEEN 0 AND 9007199254740991 AND max_output>0)
);
ALTER TABLE newapi_execution_receipts ADD COLUMN cache_details_version smallint NOT NULL DEFAULT 0;
ALTER TABLE newapi_execution_receipts ADD COLUMN cache_read_tokens bigint NOT NULL DEFAULT 0;
ALTER TABLE newapi_execution_receipts ADD COLUMN cache_write_tokens bigint NOT NULL DEFAULT 0;
ALTER TABLE newapi_execution_receipts ADD CONSTRAINT newapi_cache_evidence_check CHECK (
 cache_details_version IN (0,1) AND cache_read_tokens BETWEEN 0 AND input_tokens AND
 cache_write_tokens BETWEEN 0 AND input_tokens-cache_read_tokens AND
 (cache_details_version=1 OR (cache_read_tokens=0 AND cache_write_tokens=0))
);
