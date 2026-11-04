CREATE TABLE budget_alert_policies (
 organization_id text NOT NULL REFERENCES organizations(id),
 project_id text NOT NULL,
 enabled boolean NOT NULL,
 thresholds integer[] NOT NULL CHECK(cardinality(thresholds) BETWEEN 1 AND 7 AND thresholds <@ ARRAY[50,60,70,80,90,95,100]),
 revision bigint NOT NULL CHECK(revision>0),
 PRIMARY KEY(organization_id,project_id)
);
CREATE TABLE budget_alerts (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 organization_id text NOT NULL,
 project_id text NOT NULL,
 period_id text NOT NULL,
 currency text NOT NULL,
 threshold integer NOT NULL CHECK(threshold BETWEEN 1 AND 100),
 policy_revision bigint NOT NULL CHECK(policy_revision>0),
 limit_amount bigint NOT NULL CHECK(limit_amount>0),
 spent bigint NOT NULL CHECK(spent>=0),
 held bigint NOT NULL CHECK(held>=0),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 acknowledged_at timestamptz,
 actor_issuer text,
 actor_subject text,
 reason text,
 UNIQUE(organization_id,project_id,period_id,currency,threshold),
 FOREIGN KEY(organization_id,project_id,period_id,currency) REFERENCES budget_accounts(organization_id,project_id,period_id,currency),
 CHECK((acknowledged_at IS NULL AND actor_issuer IS NULL AND actor_subject IS NULL AND reason IS NULL) OR
       (acknowledged_at IS NOT NULL AND actor_issuer IS NOT NULL AND actor_subject IS NOT NULL AND reason IS NOT NULL AND length(reason) BETWEEN 1 AND 256))
);
CREATE INDEX budget_alert_history ON budget_alerts(organization_id,project_id,id DESC);
CREATE TABLE budget_alert_policy_versions (
 organization_id text NOT NULL,
 project_id text NOT NULL,
 revision bigint NOT NULL,
 enabled boolean NOT NULL,
 thresholds integer[] NOT NULL,
 actor_issuer text NOT NULL,
 actor_subject text NOT NULL,
 reason text NOT NULL CHECK(length(reason) BETWEEN 1 AND 256),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,project_id,revision),
 FOREIGN KEY(organization_id,project_id) REFERENCES budget_alert_policies(organization_id,project_id)
);
