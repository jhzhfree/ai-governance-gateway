CREATE TABLE content_audit_policies (
 organization_id text NOT NULL, project_id text NOT NULL,
 enabled boolean NOT NULL DEFAULT false,
 retention_days integer NOT NULL CHECK(retention_days BETWEEN 1 AND 90),
 max_bytes integer NOT NULL CHECK(max_bytes BETWEEN 1024 AND 262144),
 consent_ref text NOT NULL,
 revision bigint NOT NULL CHECK(revision>=1),
 PRIMARY KEY(organization_id,project_id),
 FOREIGN KEY(organization_id,project_id) REFERENCES projects(organization_id,id)
);
CREATE TABLE content_audit_auditors (
 organization_id text NOT NULL, project_id text NOT NULL,
 issuer text NOT NULL, subject text NOT NULL, active boolean NOT NULL,
 PRIMARY KEY(organization_id,project_id,issuer,subject),
 FOREIGN KEY(organization_id,project_id) REFERENCES projects(organization_id,id)
);
CREATE TABLE content_audit_deks (
 organization_id text NOT NULL REFERENCES organizations(id), key_id text NOT NULL,
 wrapped_dek bytea NOT NULL, nonce bytea NOT NULL CHECK(octet_length(nonce)=12),
 PRIMARY KEY(organization_id,key_id)
);
CREATE TABLE content_audit_records (
 request_id text PRIMARY KEY,
 organization_id text NOT NULL, project_id text NOT NULL,
 model text NOT NULL, key_prefix text NOT NULL, policy_revision bigint NOT NULL,
 key_id text NOT NULL, max_bytes integer NOT NULL,
 consent_ref text NOT NULL, retention_days integer NOT NULL,
 state text NOT NULL CHECK(state IN ('capturing','complete','incomplete','canceled','expired')),
 status integer NOT NULL DEFAULT 0 CHECK(status BETWEEN 0 AND 599),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(), expires_at timestamptz NOT NULL,
 finished_at timestamptz,
 UNIQUE(request_id,created_at),
 FOREIGN KEY(organization_id,project_id) REFERENCES projects(organization_id,id),
 FOREIGN KEY(organization_id,key_id) REFERENCES content_audit_deks(organization_id,key_id)
);
CREATE INDEX content_audit_project_idx ON content_audit_records(organization_id,project_id,created_at DESC,request_id DESC);
CREATE INDEX content_audit_expiry_idx ON content_audit_records(expires_at,organization_id,request_id) WHERE state<>'expired';
CREATE TABLE content_audit_payloads (
 request_id text NOT NULL, direction text NOT NULL CHECK(direction IN ('request','response')),
 created_at timestamptz NOT NULL,
 ciphertext bytea NOT NULL CHECK(octet_length(ciphertext)<=262160),
 nonce bytea NOT NULL CHECK(octet_length(nonce)=12),
 sha256 bytea NOT NULL CHECK(octet_length(sha256)=32),
 observed_bytes bigint NOT NULL CHECK(observed_bytes>=0),
 truncated boolean NOT NULL,
 PRIMARY KEY(request_id,direction,created_at),
 FOREIGN KEY(request_id,created_at) REFERENCES content_audit_records(request_id,created_at)
) PARTITION BY RANGE(created_at);
CREATE TABLE content_audit_payloads_default PARTITION OF content_audit_payloads DEFAULT;
CREATE TABLE content_audit_heads (
 organization_id text PRIMARY KEY REFERENCES organizations(id),
 sequence bigint NOT NULL CHECK(sequence>=0), hash bytea NOT NULL CHECK(octet_length(hash)=32)
);
CREATE TABLE content_audit_chain (
 organization_id text NOT NULL REFERENCES organizations(id), sequence bigint NOT NULL,
 request_id text NOT NULL, event text NOT NULL CHECK(event IN ('start','seal','purge')),
 digest bytea NOT NULL CHECK(octet_length(digest)=32),
 previous bytea NOT NULL CHECK(octet_length(previous)=32),
 hash bytea NOT NULL CHECK(octet_length(hash)=32),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,sequence)
);
CREATE TABLE content_audit_access_log (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 organization_id text NOT NULL, project_id text NOT NULL, request_id text NOT NULL,
 actor_issuer text NOT NULL, actor_subject text NOT NULL, reason text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(organization_id,project_id) REFERENCES projects(organization_id,id)
);
