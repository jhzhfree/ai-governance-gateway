CREATE TABLE IF NOT EXISTS organizations (
    id text PRIMARY KEY,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    period_id text NOT NULL,
    currency text NOT NULL CHECK (currency = 'MOCK'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE IF NOT EXISTS organization_members (
    organization_id text NOT NULL REFERENCES organizations(id),
    issuer text NOT NULL,
    subject text NOT NULL,
    role text NOT NULL CHECK(role IN ('owner','admin','developer','viewer')),
    PRIMARY KEY(organization_id,issuer,subject)
);
CREATE TABLE IF NOT EXISTS projects (
    organization_id text NOT NULL REFERENCES organizations(id),
    id text NOT NULL,
    name text NOT NULL CHECK(length(name) BETWEEN 1 AND 100),
    PRIMARY KEY(organization_id,id)
);
CREATE TABLE IF NOT EXISTS project_members (
    organization_id text NOT NULL,
    project_id text NOT NULL,
    issuer text NOT NULL,
    subject text NOT NULL,
    role text NOT NULL CHECK(role IN ('admin','developer','viewer')),
    PRIMARY KEY(organization_id,project_id,issuer,subject),
    FOREIGN KEY(organization_id,project_id) REFERENCES projects(organization_id,id),
    FOREIGN KEY(organization_id,issuer,subject) REFERENCES organization_members(organization_id,issuer,subject) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS project_models (
    organization_id text NOT NULL,
    project_id text NOT NULL,
    model text NOT NULL CHECK(model='mock-text'),
    PRIMARY KEY(organization_id,project_id,model),
    FOREIGN KEY(organization_id,project_id) REFERENCES projects(organization_id,id)
);
CREATE TABLE IF NOT EXISTS project_keys (
    id text PRIMARY KEY,
    organization_id text NOT NULL,
    project_id text NOT NULL,
    name text NOT NULL CHECK(length(name) BETWEEN 1 AND 100),
    digest bytea NOT NULL UNIQUE CHECK(octet_length(digest)=32),
    prefix text NOT NULL,
    models text[] NOT NULL CHECK(cardinality(models)>0),
    creator_issuer text NOT NULL,
    creator_subject text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    FOREIGN KEY(organization_id,project_id) REFERENCES projects(organization_id,id),
    FOREIGN KEY(organization_id,creator_issuer,creator_subject) REFERENCES organization_members(organization_id,issuer,subject)
);
CREATE INDEX IF NOT EXISTS project_keys_scope_idx ON project_keys(organization_id,project_id,id);
CREATE TABLE IF NOT EXISTS audit_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    organization_id text NOT NULL REFERENCES organizations(id),
    actor_issuer text NOT NULL,
    actor_subject text NOT NULL,
    action text NOT NULL,
    resource_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX IF NOT EXISTS audit_scope_idx ON audit_events(organization_id,id);
