CREATE TABLE provider_credentials (
  provider text PRIMARY KEY CHECK (provider IN ('aliyun','volcengine','zhipu')),
  revision bigint NOT NULL CHECK (revision >= 1),
  key_id text NOT NULL,
  nonce bytea NOT NULL CHECK (octet_length(nonce)=12),
  ciphertext bytea NOT NULL CHECK (octet_length(ciphertext) BETWEEN 17 AND 8208),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE provider_credential_events (
  provider text NOT NULL CHECK (provider IN ('aliyun','volcengine','zhipu')),
  revision bigint NOT NULL CHECK (revision >= 1),
  actor_issuer text NOT NULL,
  actor_subject text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(provider,revision)
);
