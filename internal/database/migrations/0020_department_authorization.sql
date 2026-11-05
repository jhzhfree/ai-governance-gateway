CREATE TABLE department_configurations (
 organization_id text PRIMARY KEY REFERENCES organizations(id),
 revision bigint NOT NULL CHECK(revision>0)
);
CREATE TABLE departments (
 organization_id text NOT NULL REFERENCES organizations(id),
 id text NOT NULL,
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 80),
 parent_id text,
 PRIMARY KEY(organization_id,id),
 FOREIGN KEY(organization_id,parent_id) REFERENCES departments(organization_id,id) DEFERRABLE INITIALLY DEFERRED,
 CHECK(parent_id IS NULL OR parent_id<>id)
);
CREATE TABLE department_members (
 organization_id text NOT NULL,
 department_id text NOT NULL,
 issuer text NOT NULL,
 subject text NOT NULL,
 role text NOT NULL CHECK(role IN ('admin','developer','viewer')),
 PRIMARY KEY(organization_id,department_id,issuer,subject),
 FOREIGN KEY(organization_id,department_id) REFERENCES departments(organization_id,id),
 FOREIGN KEY(organization_id,issuer,subject) REFERENCES organization_members(organization_id,issuer,subject)
);
CREATE INDEX department_members_identity ON department_members(organization_id,issuer,subject);
CREATE TABLE department_projects (
 organization_id text NOT NULL,
 project_id text NOT NULL,
 department_id text NOT NULL,
 PRIMARY KEY(organization_id,project_id),
 FOREIGN KEY(organization_id,project_id) REFERENCES projects(organization_id,id),
 FOREIGN KEY(organization_id,department_id) REFERENCES departments(organization_id,id)
);
CREATE TABLE department_versions (
 organization_id text NOT NULL REFERENCES organizations(id),
 revision bigint NOT NULL CHECK(revision>0),
 actor_issuer text NOT NULL,
 actor_subject text NOT NULL,
 reason text NOT NULL CHECK(length(reason) BETWEEN 1 AND 256),
 configuration jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,revision)
);
ALTER TABLE organization_member_lifecycle ADD COLUMN removed_department_grants bigint NOT NULL DEFAULT 0 CHECK(removed_department_grants>=0);
