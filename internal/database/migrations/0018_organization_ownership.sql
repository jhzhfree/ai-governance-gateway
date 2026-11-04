CREATE UNIQUE INDEX organization_single_owner ON organization_members(organization_id) WHERE role='owner';
CREATE TABLE organization_ownership_transfers (
 id text PRIMARY KEY,
 organization_id text NOT NULL REFERENCES organizations(id),
 owner_issuer text NOT NULL,
 owner_subject text NOT NULL,
 target_issuer text NOT NULL,
 target_subject text NOT NULL,
 target_role text NOT NULL CHECK(target_role IN ('admin','developer','viewer')),
 reason text NOT NULL CHECK(length(reason) BETWEEN 1 AND 256),
 state text NOT NULL CHECK(state IN ('pending','accepted','canceled','expired')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 expires_at timestamptz NOT NULL DEFAULT clock_timestamp()+interval '24 hours',
 finished_at timestamptz,
 FOREIGN KEY(organization_id,owner_issuer,owner_subject) REFERENCES organization_members(organization_id,issuer,subject),
 FOREIGN KEY(organization_id,target_issuer,target_subject) REFERENCES organization_members(organization_id,issuer,subject),
 CHECK((owner_issuer,owner_subject)<>(target_issuer,target_subject)),
 CHECK((state='pending')=(finished_at IS NULL))
);
CREATE UNIQUE INDEX organization_pending_ownership ON organization_ownership_transfers(organization_id) WHERE state='pending';
