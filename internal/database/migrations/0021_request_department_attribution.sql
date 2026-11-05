-- Existing reservations deliberately have no attribution row: historical unknown.
CREATE TABLE request_department_attributions (
 request_id text PRIMARY KEY REFERENCES budget_reservations(request_id),
 configuration_revision bigint NOT NULL CHECK(configuration_revision>=0),
 department_path jsonb NOT NULL CHECK(jsonb_typeof(department_path)='array' AND jsonb_array_length(department_path)<=8),
 captured_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
