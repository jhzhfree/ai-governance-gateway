-- Single-process evidence is independent of the legacy new-api outbox.
CREATE TABLE engine_execution_bindings (
 attempt_id text PRIMARY KEY REFERENCES inference_attempts(attempt_id),
 request_id text NOT NULL UNIQUE REFERENCES budget_reservations(request_id),
 engine text NOT NULL CHECK(engine='bifrost'),
 protocol text NOT NULL CHECK(protocol='chat_v1'),
 model text NOT NULL,
 upstream_model text NOT NULL,
 supplier text NOT NULL,
 route_digest bytea NOT NULL CHECK(octet_length(route_digest)=32),
 price_version text NOT NULL,
 input_price bigint NOT NULL CHECK(input_price>0 AND input_price<=9007199254740991),
 output_price bigint NOT NULL CHECK(output_price>0 AND output_price<=9007199254740991),
 max_input bigint NOT NULL CHECK(max_input BETWEEN 1 AND 2000000),
 max_output bigint NOT NULL CHECK(max_output BETWEEN 16 AND 1000000),
 stream boolean NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE engine_completion_events (
 attempt_id text PRIMARY KEY REFERENCES engine_execution_bindings(attempt_id),
 raw_usage bytea NOT NULL CHECK(octet_length(raw_usage) BETWEEN 2 AND 8192),
 usage_sha256 bytea NOT NULL CHECK(octet_length(usage_sha256)=32),
 input_tokens bigint NOT NULL CHECK(input_tokens BETWEEN 1 AND 2000000),
 output_tokens bigint NOT NULL CHECK(output_tokens BETWEEN 0 AND 1000000),
 reconciled boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 recovery_checked_at timestamptz NOT NULL DEFAULT 'epoch'
);
CREATE INDEX engine_completion_recovery ON engine_completion_events(recovery_checked_at,created_at,attempt_id) WHERE NOT reconciled;
CREATE TABLE engine_completion_receipts (
 attempt_id text PRIMARY KEY REFERENCES engine_completion_events(attempt_id),
 actual bigint NOT NULL CHECK(actual BETWEEN 0 AND 9007199254740991),
 usage_sha256 bytea NOT NULL CHECK(octet_length(usage_sha256)=32),
 received_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
