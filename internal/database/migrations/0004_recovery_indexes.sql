-- Immutable after release. Append a new numbered migration for changes.
CREATE INDEX budget_recovery_idx ON budget_reservations(updated_at, request_id)
    WHERE state = 'dispatched';
CREATE INDEX browser_login_flows_expiry_idx ON browser_login_flows(expires_at);
