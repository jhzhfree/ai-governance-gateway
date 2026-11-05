CREATE TABLE budget_approval_policies (
 organization_id text PRIMARY KEY REFERENCES organizations(id),
 enabled boolean NOT NULL,
 revision bigint NOT NULL CHECK(revision>0)
);
CREATE TABLE budget_approval_requests (
 id text PRIMARY KEY,
 organization_id text NOT NULL,
 project_id text NOT NULL,
 period_id text NOT NULL,
 currency text NOT NULL,
 expected_revision bigint NOT NULL CHECK(expected_revision>=0),
 previous_limit bigint NOT NULL CHECK(previous_limit>=0),
 requested_limit bigint NOT NULL CHECK(requested_limit>=0),
 requester_issuer text NOT NULL,
 requester_subject text NOT NULL,
 reason text NOT NULL CHECK(length(reason) BETWEEN 1 AND 256),
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','approved','rejected','canceled')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 expires_at timestamptz NOT NULL DEFAULT clock_timestamp()+interval '24 hours',
 finished_at timestamptz,
 decider_issuer text,
 decider_subject text,
 decision_reason text,
 applied_revision bigint,
 CHECK(expires_at>created_at),
 CHECK((state='pending')=(finished_at IS NULL)),
 CHECK((state='pending' AND decider_issuer IS NULL AND decider_subject IS NULL AND decision_reason IS NULL) OR (state<>'pending' AND decider_issuer IS NOT NULL AND decider_subject IS NOT NULL AND decision_reason IS NOT NULL AND length(decision_reason) BETWEEN 1 AND 256)),
 CHECK((state='approved')=(applied_revision IS NOT NULL)),
 CHECK(state<>'approved' OR (requester_issuer,requester_subject)<>(decider_issuer,decider_subject)),
 FOREIGN KEY(organization_id,project_id,period_id,currency) REFERENCES budget_accounts(organization_id,project_id,period_id,currency),
 FOREIGN KEY(organization_id,requester_issuer,requester_subject) REFERENCES organization_members(organization_id,issuer,subject)
);
CREATE INDEX budget_approval_scope ON budget_approval_requests(organization_id,project_id,id);
CREATE UNIQUE INDEX budget_one_pending_approval ON budget_approval_requests(organization_id,project_id) WHERE state='pending';
CREATE TABLE budget_approval_policy_versions (
 organization_id text NOT NULL REFERENCES organizations(id),
 revision bigint NOT NULL CHECK(revision>0),
 enabled boolean NOT NULL,
 reason text NOT NULL CHECK(length(reason) BETWEEN 1 AND 256),
 actor_issuer text NOT NULL,
 actor_subject text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,revision)
);
