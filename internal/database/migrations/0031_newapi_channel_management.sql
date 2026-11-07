-- Non-secret administration history; credentials exist only in transit to new-api.
CREATE TABLE newapi_channel_operations (
  id text PRIMARY KEY,
  actor_issuer text NOT NULL,
  actor_subject text NOT NULL,
  data_plane_origin text NOT NULL,
  channel_id integer NOT NULL CHECK (channel_id >= 0),
  expected_revision text NOT NULL,
  request_digest text NOT NULL CHECK (length(request_digest) = 64),
  state text NOT NULL CHECK (state IN ('pending','applied','rejected')),
  receipt jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX newapi_channel_operations_recent ON newapi_channel_operations(created_at DESC,id);
CREATE TABLE newapi_model_configuration (
  singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
  revision bigint NOT NULL CHECK (revision >= 1),
  data_plane_origin text NOT NULL,
  configuration jsonb NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE newapi_model_configuration_events (
  revision bigint PRIMARY KEY,
  actor_issuer text NOT NULL,
  actor_subject text NOT NULL,
  configuration jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
