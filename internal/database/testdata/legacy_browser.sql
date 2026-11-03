CREATE TABLE IF NOT EXISTS browser_login_flows (
    state_digest bytea PRIMARY KEY CHECK(octet_length(state_digest)=32),
    binding_digest bytea NOT NULL CHECK(octet_length(binding_digest)=32),
    verifier text NOT NULL,
    nonce text NOT NULL,
    expires_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS browser_sessions (
    digest bytea PRIMARY KEY CHECK(octet_length(digest)=32),
    issuer text NOT NULL,
    subject text NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX IF NOT EXISTS browser_sessions_expiry_idx ON browser_sessions(expires_at);
