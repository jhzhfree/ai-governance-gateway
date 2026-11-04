-- Platform-wide configuration; contains no credentials, only approved route metadata.
CREATE TABLE provider_configuration (
  singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
  revision bigint NOT NULL CHECK (revision >= 1),
  configuration jsonb NOT NULL CHECK (jsonb_typeof(configuration) = 'object'),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE provider_configuration_events (
  revision bigint PRIMARY KEY CHECK (revision >= 1),
  actor_issuer text NOT NULL,
  actor_subject text NOT NULL,
  digest bytea NOT NULL CHECK (octet_length(digest) = 32),
  configuration jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
