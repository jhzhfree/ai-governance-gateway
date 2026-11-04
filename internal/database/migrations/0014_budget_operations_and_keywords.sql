CREATE TABLE budget_limit_changes (
 organization_id text NOT NULL,
 project_id text NOT NULL,
 period_id text NOT NULL,
 currency text NOT NULL,
 revision bigint NOT NULL CHECK (revision>0),
 previous_limit bigint NOT NULL CHECK(previous_limit>=0),
 new_limit bigint NOT NULL CHECK(new_limit>=0),
 reason text NOT NULL CHECK(length(reason) BETWEEN 1 AND 256),
 actor_issuer text NOT NULL,
 actor_subject text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(organization_id,project_id,period_id,currency,revision),
 FOREIGN KEY(organization_id,project_id,period_id,currency) REFERENCES budget_accounts(organization_id,project_id,period_id,currency)
);
CREATE TABLE budget_cycles (
 organization_id text PRIMARY KEY REFERENCES organizations(id),
 enabled boolean NOT NULL,
 revision bigint NOT NULL CHECK(revision>0)
);
CREATE TABLE budget_period_changes (
 organization_id text NOT NULL REFERENCES organizations(id),
 previous_period text NOT NULL,
 new_period text NOT NULL,
 actor_issuer text NOT NULL,
 actor_subject text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(organization_id,new_period)
);
CREATE TABLE keyword_policies (
 organization_id text NOT NULL,
 project_id text NOT NULL,
 revision bigint NOT NULL CHECK(revision>0),
 rules jsonb NOT NULL CHECK(jsonb_typeof(rules)='array'),
 PRIMARY KEY(organization_id,project_id),
 FOREIGN KEY(organization_id,project_id) REFERENCES projects(organization_id,id)
);
CREATE TABLE keyword_matches (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 organization_id text NOT NULL,
 project_id text NOT NULL,
 request_id text NOT NULL,
 policy_revision bigint NOT NULL,
 rule_id text NOT NULL,
 direction text NOT NULL CHECK(direction IN ('input','output')),
 action text NOT NULL CHECK(action IN ('alert','block','redact')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(request_id,rule_id,direction),
 FOREIGN KEY(organization_id,project_id) REFERENCES projects(organization_id,id)
);
CREATE INDEX keyword_matches_project_history ON keyword_matches(organization_id,project_id,id DESC);
CREATE TABLE keyword_policy_versions (
 organization_id text NOT NULL,
 project_id text NOT NULL,
 revision bigint NOT NULL CHECK(revision>0),
 rules jsonb NOT NULL CHECK(jsonb_typeof(rules)='array'),
 actor_issuer text NOT NULL,
 actor_subject text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(organization_id,project_id,revision),
 FOREIGN KEY(organization_id,project_id) REFERENCES projects(organization_id,id)
);
