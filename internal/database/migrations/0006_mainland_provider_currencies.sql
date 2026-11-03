-- Existing MOCK accounts remain unchanged. CNY_MICRO is a separate unit and
-- cannot be selected by mutating an existing organization through the API.
ALTER TABLE organizations DROP CONSTRAINT organizations_currency_check;
ALTER TABLE organizations ADD CONSTRAINT organizations_currency_check
    CHECK (currency IN ('MOCK', 'CNY_MICRO'));
ALTER TABLE project_models DROP CONSTRAINT project_models_model_check;
ALTER TABLE project_models ADD CONSTRAINT project_models_model_check
    CHECK (length(model) BETWEEN 1 AND 128);
