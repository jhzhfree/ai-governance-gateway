CREATE TABLE local_users (
 id text PRIMARY KEY,
 username text NOT NULL UNIQUE CHECK(username ~ '^[a-z][a-z0-9._-]{2,63}$'),
 password_salt bytea NOT NULL CHECK(octet_length(password_salt)=16),
 password_hash bytea NOT NULL CHECK(octet_length(password_hash)=32),
 enabled boolean NOT NULL DEFAULT true,
 administrator boolean NOT NULL DEFAULT false,
 must_change_password boolean NOT NULL DEFAULT true,
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE local_access_tokens (
 digest bytea PRIMARY KEY CHECK(octet_length(digest)=32),
 user_id text NOT NULL REFERENCES local_users(id),
 expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX local_access_token_expiry ON local_access_tokens(expires_at);
CREATE TABLE local_auth_throttle (
 digest bytea PRIMARY KEY CHECK(octet_length(digest)=32),
 window_start timestamptz NOT NULL,
 attempts integer NOT NULL CHECK(attempts BETWEEN 1 AND 1000000)
);
CREATE TABLE local_security_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 actor_issuer text NOT NULL,
 actor_subject text NOT NULL,
 action text NOT NULL,
 target_id text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
