ALTER TABLE organization_members ADD COLUMN enabled boolean NOT NULL DEFAULT true;
ALTER TABLE organization_members ADD COLUMN lifecycle_revision bigint NOT NULL DEFAULT 0 CHECK(lifecycle_revision>=0);
ALTER TABLE organization_members ADD CONSTRAINT organization_owner_enabled CHECK(role<>'owner' OR enabled);
CREATE INDEX project_keys_creator_live ON project_keys(organization_id,creator_issuer,creator_subject) WHERE revoked_at IS NULL;
CREATE TABLE organization_member_lifecycle (
 organization_id text NOT NULL,
 issuer text NOT NULL,
 subject text NOT NULL,
 revision bigint NOT NULL CHECK(revision>0),
 enabled boolean NOT NULL,
 previous_role text NOT NULL,
 new_role text NOT NULL,
 reason text NOT NULL CHECK(length(reason) BETWEEN 1 AND 256),
 actor_issuer text NOT NULL,
 actor_subject text NOT NULL,
 revoked_keys bigint NOT NULL CHECK(revoked_keys>=0),
 removed_project_grants bigint NOT NULL CHECK(removed_project_grants>=0),
 revoked_auditors bigint NOT NULL CHECK(revoked_auditors>=0),
 canceled_transfers bigint NOT NULL CHECK(canceled_transfers>=0),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,issuer,subject,revision),
 FOREIGN KEY(organization_id,issuer,subject) REFERENCES organization_members(organization_id,issuer,subject)
);
