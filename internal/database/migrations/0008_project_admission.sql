CREATE TABLE project_limits (
    organization_id text NOT NULL,
    project_id text NOT NULL,
    requests_per_minute integer NOT NULL CHECK(requests_per_minute BETWEEN 1 AND 10000),
    max_concurrent integer NOT NULL CHECK(max_concurrent BETWEEN 1 AND 64),
    revision bigint NOT NULL DEFAULT 0 CHECK(revision>=0),
    PRIMARY KEY(organization_id,project_id),
    FOREIGN KEY(organization_id,project_id) REFERENCES projects(organization_id,id)
);
CREATE TABLE admission_events (
    request_id text PRIMARY KEY,
    organization_id text NOT NULL,
    project_id text NOT NULL,
    admitted_at timestamptz NOT NULL,
    FOREIGN KEY(organization_id,project_id) REFERENCES projects(organization_id,id)
);
CREATE INDEX admission_events_window_idx ON admission_events(organization_id,project_id,admitted_at DESC);
CREATE TABLE admission_leases (
    lease_id text PRIMARY KEY,
    request_id text NOT NULL UNIQUE,
    organization_id text NOT NULL,
    project_id text NOT NULL,
    expires_at timestamptz NOT NULL,
    FOREIGN KEY(organization_id,project_id) REFERENCES projects(organization_id,id)
);
CREATE INDEX admission_leases_active_idx ON admission_leases(organization_id,project_id,expires_at);
