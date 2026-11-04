CREATE TABLE supplier_circuits (
    route_key text PRIMARY KEY,
    state text NOT NULL CHECK(state IN ('closed','open','half_open')),
    generation bigint NOT NULL DEFAULT 0 CHECK(generation>=0),
    failures integer NOT NULL DEFAULT 0 CHECK(failures BETWEEN 0 AND 5),
    retry_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK((state='closed' AND retry_at IS NULL) OR (state<>'closed' AND retry_at IS NOT NULL))
);
