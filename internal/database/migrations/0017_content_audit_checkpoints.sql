CREATE TABLE content_audit_checkpoints (
 request_id text PRIMARY KEY REFERENCES content_audit_records(request_id),
 owner_id text NOT NULL CHECK(length(owner_id) BETWEEN 20 AND 64),
 lease_until timestamptz NOT NULL,
 sha256 bytea NOT NULL CHECK(octet_length(sha256)=32),
 observed_bytes bigint NOT NULL CHECK(observed_bytes>=0),
 captured_bytes integer NOT NULL CHECK(captured_bytes BETWEEN 0 AND 262144),
 parts integer NOT NULL CHECK(parts BETWEEN 0 AND 262144),
 head bytea NOT NULL CHECK(octet_length(head)=32),
 proof bytea NOT NULL CHECK(octet_length(proof)=32),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK(captured_bytes<=observed_bytes)
);
CREATE INDEX content_audit_checkpoint_expiry ON content_audit_checkpoints(lease_until,request_id);
CREATE TABLE content_audit_checkpoint_parts (
 request_id text NOT NULL REFERENCES content_audit_checkpoints(request_id) ON DELETE CASCADE,
 sequence integer NOT NULL CHECK(sequence>0),
 ciphertext bytea NOT NULL CHECK(octet_length(ciphertext) BETWEEN 17 AND 262160),
 nonce bytea NOT NULL CHECK(octet_length(nonce)=12),
 PRIMARY KEY(request_id,sequence)
);
