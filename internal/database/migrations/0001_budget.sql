-- Immutable after release. Append a new numbered migration for changes.
CREATE TABLE budget_accounts (
    organization_id text NOT NULL,
    project_id text NOT NULL, -- empty string denotes the organization account
    period_id text NOT NULL,
    currency text NOT NULL,
    limit_amount bigint NOT NULL CHECK (limit_amount >= 0),
    spent bigint NOT NULL DEFAULT 0 CHECK (spent >= 0),
    held bigint NOT NULL DEFAULT 0 CHECK (held >= 0),
    PRIMARY KEY (organization_id, project_id, period_id, currency),
    CHECK (spent <= limit_amount AND held <= limit_amount - spent)
);

CREATE TABLE budget_reservations (
    request_id text PRIMARY KEY,
    organization_id text NOT NULL,
    project_id text NOT NULL CHECK (project_id <> ''),
    period_id text NOT NULL,
    currency text NOT NULL,
    price_version text NOT NULL,
    reserved bigint NOT NULL CHECK (reserved > 0),
    actual bigint,
    state text NOT NULL CHECK (state IN ('reserved','dispatched','uncertain','settled','released','estimated_final')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (organization_id, project_id, period_id, currency)
        REFERENCES budget_accounts (organization_id, project_id, period_id, currency),
    CHECK (actual IS NULL OR (actual >= 0 AND actual <= reserved)),
    CHECK ((state IN ('settled','released','estimated_final')) = (actual IS NOT NULL)),
    CHECK (state <> 'released' OR actual = 0)
);

CREATE TABLE budget_outbox (
    event_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    request_id text NOT NULL REFERENCES budget_reservations(request_id),
    event_type text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (request_id, event_type)
);
