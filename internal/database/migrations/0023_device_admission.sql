CREATE TABLE terminal_devices (
 id text PRIMARY KEY,
 organization_id text NOT NULL REFERENCES organizations(id),
 owner_issuer text NOT NULL,
 owner_subject text NOT NULL,
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 80),
 public_key text NOT NULL,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','active','revoked')),
 revision bigint NOT NULL DEFAULT 0 CHECK(revision>=0),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(organization_id,public_key),
 UNIQUE(organization_id,id),
 FOREIGN KEY(organization_id,owner_issuer,owner_subject) REFERENCES organization_members(organization_id,issuer,subject)
);
CREATE INDEX terminal_devices_org ON terminal_devices(organization_id,id);
CREATE TABLE device_state_events (
 device_id text NOT NULL REFERENCES terminal_devices(id),
 revision bigint NOT NULL,
 state text NOT NULL CHECK(state IN ('pending','active','revoked')),
 actor_issuer text NOT NULL,
 actor_subject text NOT NULL,
 reason text NOT NULL CHECK(length(reason) BETWEEN 1 AND 256),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(device_id,revision)
);
CREATE TABLE project_device_policies (
 organization_id text NOT NULL,
 project_id text NOT NULL,
 required boolean NOT NULL,
 revision bigint NOT NULL CHECK(revision>0),
 PRIMARY KEY(organization_id,project_id),
 FOREIGN KEY(organization_id,project_id) REFERENCES projects(organization_id,id)
);
CREATE TABLE device_policy_events (
 organization_id text NOT NULL,
 project_id text NOT NULL,
 revision bigint NOT NULL,
 required boolean NOT NULL,
 actor_issuer text NOT NULL,
 actor_subject text NOT NULL,
 reason text NOT NULL CHECK(length(reason) BETWEEN 1 AND 256),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,project_id,revision),
 FOREIGN KEY(organization_id,project_id) REFERENCES projects(organization_id,id)
);
CREATE TABLE device_sessions (
 id text PRIMARY KEY,
 device_id text NOT NULL REFERENCES terminal_devices(id),
 organization_id text NOT NULL,
 project_id text NOT NULL,
 digest bytea NOT NULL UNIQUE,
 models text[] NOT NULL CHECK(cardinality(models) BETWEEN 1 AND 32),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 expires_at timestamptz NOT NULL DEFAULT clock_timestamp()+interval '5 minutes',
 revoked_at timestamptz,
 FOREIGN KEY(organization_id,project_id) REFERENCES projects(organization_id,id),
 FOREIGN KEY(organization_id,device_id) REFERENCES terminal_devices(organization_id,id),
 UNIQUE(id,device_id),
 CHECK(expires_at>created_at)
);
CREATE INDEX device_sessions_device ON device_sessions(device_id,id);
CREATE TABLE device_proof_nonces (
 device_id text NOT NULL REFERENCES terminal_devices(id),
 nonce text NOT NULL,
 expires_at timestamptz NOT NULL,
 PRIMARY KEY(device_id,nonce)
);
CREATE INDEX device_proof_expiry ON device_proof_nonces(expires_at);
CREATE TABLE device_request_bindings (
 request_id text PRIMARY KEY REFERENCES budget_reservations(request_id),
 device_id text NOT NULL REFERENCES terminal_devices(id),
 session_id text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(session_id,device_id) REFERENCES device_sessions(id,device_id)
);
ALTER TABLE organization_member_lifecycle ADD COLUMN revoked_devices bigint NOT NULL DEFAULT 0 CHECK(revoked_devices>=0);
