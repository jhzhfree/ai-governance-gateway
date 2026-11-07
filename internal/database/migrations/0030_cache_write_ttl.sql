-- Optional reviewed duration prices. NULL preserves historical aggregate tariffs.
ALTER TABLE newapi_execution_bindings ADD COLUMN cache_write_5m_price bigint;
ALTER TABLE newapi_execution_bindings ADD COLUMN cache_write_1h_price bigint;
ALTER TABLE newapi_execution_bindings ADD CONSTRAINT newapi_cache_ttl_tariff_check CHECK (
 (cache_write_5m_price IS NULL AND cache_write_1h_price IS NULL) OR
 (cache_write_5m_price IS NOT NULL AND cache_write_1h_price IS NOT NULL AND cache_read_price IS NOT NULL AND
 cache_write_5m_price BETWEEN 0 AND 9007199254740991 AND cache_write_1h_price BETWEEN 0 AND 9007199254740991)
);
ALTER TABLE newapi_execution_receipts ADD COLUMN cache_write_5m_tokens bigint NOT NULL DEFAULT 0;
ALTER TABLE newapi_execution_receipts ADD COLUMN cache_write_1h_tokens bigint NOT NULL DEFAULT 0;
ALTER TABLE newapi_execution_receipts DROP CONSTRAINT newapi_cache_evidence_check;
ALTER TABLE newapi_execution_receipts ADD CONSTRAINT newapi_cache_evidence_check CHECK (
 cache_details_version IN (0,1,2) AND cache_read_tokens BETWEEN 0 AND input_tokens AND
 cache_write_tokens BETWEEN 0 AND input_tokens-cache_read_tokens AND
 (cache_details_version>0 OR (cache_read_tokens=0 AND cache_write_tokens=0)) AND
 cache_write_5m_tokens BETWEEN 0 AND cache_write_tokens AND cache_write_1h_tokens BETWEEN 0 AND cache_write_tokens AND
 ((cache_details_version=2 AND cache_write_1h_tokens=cache_write_tokens-cache_write_5m_tokens) OR
 (cache_details_version<>2 AND cache_write_5m_tokens=0 AND cache_write_1h_tokens=0))
);
