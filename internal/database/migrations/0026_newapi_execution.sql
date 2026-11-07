-- Bind each physical attempt to its immutable control-plane price snapshot.
CREATE TABLE newapi_execution_bindings (
 attempt_id text PRIMARY KEY REFERENCES inference_attempts(attempt_id),
 request_id text NOT NULL UNIQUE REFERENCES budget_reservations(request_id),
 backend text NOT NULL,
 model text NOT NULL,
 channel_id bigint NOT NULL CHECK(channel_id>0),
 fingerprint text NOT NULL CHECK(length(fingerprint)=64),
 price_version text NOT NULL,
 input_price bigint NOT NULL CHECK(input_price>0 AND input_price<=9007199254740991),
 output_price bigint NOT NULL CHECK(output_price>0 AND output_price<=9007199254740991),
 max_input bigint NOT NULL CHECK(max_input BETWEEN 1 AND 2000000),
 max_output bigint NOT NULL CHECK(max_output BETWEEN 1 AND 1000000),
 recovery_complete boolean NOT NULL DEFAULT false,
 recovery_checked_at timestamptz NOT NULL DEFAULT 'epoch',
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX newapi_recovery_scan ON newapi_execution_bindings(backend,recovery_checked_at,created_at) WHERE NOT recovery_complete;
CREATE TABLE newapi_execution_receipts (
 attempt_id text PRIMARY KEY REFERENCES newapi_execution_bindings(attempt_id),
 state text NOT NULL CHECK(state IN ('completed','cancelled')),
 input_tokens bigint NOT NULL CHECK(input_tokens>=0),
 output_tokens bigint NOT NULL CHECK(output_tokens>=0),
 actual bigint NOT NULL CHECK(actual>=0 AND actual<=9007199254740991),
 received_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
