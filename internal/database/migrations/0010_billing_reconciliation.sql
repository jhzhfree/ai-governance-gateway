CREATE TABLE billing_imports (
 id text PRIMARY KEY,
 organization_id text NOT NULL,
 project_id text NOT NULL,
 provider text NOT NULL CHECK(provider IN ('aliyun','volcengine','zhipu')),
 account_ref text NOT NULL,
 evidence_ref text NOT NULL,
 digest text NOT NULL,
 importer_issuer text NOT NULL,
 importer_subject text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(organization_id,project_id,digest),
 UNIQUE(id,organization_id,project_id),
 FOREIGN KEY(organization_id,project_id) REFERENCES projects(organization_id,id)
);
CREATE TABLE billing_records (
 id text PRIMARY KEY,
 import_id text NOT NULL,
 organization_id text NOT NULL,
 project_id text NOT NULL,
 provider text NOT NULL CHECK(provider IN ('aliyun','volcengine','zhipu')),
 account_ref text NOT NULL,
 record_id text NOT NULL,
 suggested_request_id text NOT NULL,
 period_id text NOT NULL,
 currency text NOT NULL CHECK(currency='CNY_MICRO'),
 amount bigint NOT NULL CHECK(amount BETWEEN 0 AND 9007199254740991),
 input_tokens bigint NOT NULL CHECK(input_tokens BETWEEN 0 AND 2000000),
 output_tokens bigint NOT NULL CHECK(output_tokens BETWEEN 0 AND 1000000),
 decision text NOT NULL DEFAULT 'pending' CHECK(decision IN ('pending','approved','rejected')),
 revision integer NOT NULL DEFAULT 0 CHECK(revision BETWEEN 0 AND 1),
 resolved_request_id text REFERENCES budget_reservations(request_id),
 reviewer_issuer text,
 reviewer_subject text,
 reason text,
 reviewed_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(organization_id,provider,account_ref,record_id),
 FOREIGN KEY(import_id,organization_id,project_id) REFERENCES billing_imports(id,organization_id,project_id),
 CHECK((decision='pending' AND revision=0 AND reviewer_issuer IS NULL AND reviewer_subject IS NULL AND reason IS NULL AND reviewed_at IS NULL AND resolved_request_id IS NULL) OR (decision<>'pending' AND revision=1 AND reviewer_issuer IS NOT NULL AND reviewer_subject IS NOT NULL AND reason IS NOT NULL AND reviewed_at IS NOT NULL)),
 CHECK((decision='approved')=(resolved_request_id IS NOT NULL))
);
CREATE UNIQUE INDEX billing_approved_request_idx ON billing_records(resolved_request_id) WHERE decision='approved';
CREATE INDEX billing_project_history_idx ON billing_records(organization_id,project_id,created_at DESC,id DESC);
