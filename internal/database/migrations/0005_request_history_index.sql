-- Accounting metadata history, newest first with a deterministic tie breaker.
CREATE INDEX budget_project_history_idx
    ON budget_reservations (organization_id, project_id, created_at DESC, request_id DESC);
