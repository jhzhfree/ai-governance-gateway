-- Platform operations are separate from tenant audit and contain no credentials.
-- Intent is committed before contacting the privileged agent; absent completion
-- means unknown, never implicit success or a safe invitation to retry.
CREATE TABLE system_management_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 operation_id text NOT NULL,
 node text NOT NULL,
 actor_issuer text NOT NULL,
 actor_subject text NOT NULL,
 action text NOT NULL CHECK(action IN ('apply','confirm','rollback')),
 phase text NOT NULL CHECK(phase IN ('requested','succeeded','unknown','rejected')),
 reason text NOT NULL CHECK(length(reason) BETWEEN 1 AND 256),
 payload jsonb NOT NULL CHECK(jsonb_typeof(payload)='object'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(operation_id,phase)
);
CREATE INDEX system_management_recent ON system_management_events(id DESC);
