-- One explicit attempt per budget-backed request. Retries/fallback are disabled.
CREATE TABLE inference_attempts (
    attempt_id text PRIMARY KEY,
    request_id text NOT NULL UNIQUE REFERENCES budget_reservations(request_id),
    model text NOT NULL CHECK(length(model) BETWEEN 1 AND 128),
    provider text NOT NULL CHECK(provider IN ('mock','aliyun','volcengine','zhipu')),
    region text NOT NULL CHECK(region IN ('local','cn-beijing','cn-mainland')),
    stream boolean NOT NULL,
    outcome text NOT NULL DEFAULT 'unfinished' CHECK(outcome IN ('unfinished','completed','failed','canceled')),
    error_code text NOT NULL DEFAULT '' CHECK(error_code = '' OR error_code ~ '^[a-z][a-z0-9_]{0,63}$'),
    upstream_status integer CHECK(upstream_status BETWEEN 100 AND 599),
    input_tokens bigint CHECK(input_tokens >= 0),
    output_tokens bigint CHECK(output_tokens >= 0),
    started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at timestamptz,
    duration_ms bigint CHECK(duration_ms >= 0),
    CHECK((outcome='unfinished') = (finished_at IS NULL AND duration_ms IS NULL)),
    CHECK((finished_at IS NULL) = (duration_ms IS NULL)),
    CHECK(finished_at IS NULL OR finished_at >= started_at),
    CHECK((outcome IN ('unfinished','completed')) = (error_code='')),
    CHECK(outcome <> 'completed' OR (upstream_status IS NOT NULL AND upstream_status=200 AND input_tokens IS NOT NULL)),
    CHECK((input_tokens IS NULL) = (output_tokens IS NULL))
);
CREATE INDEX inference_attempts_outcome_idx ON inference_attempts(outcome,request_id);
