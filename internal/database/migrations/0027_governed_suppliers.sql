-- Preserve supplier attribution for reviewed new-api directories. Native
-- credential/configuration suppliers keep their separate fixed allowlist.
ALTER TABLE inference_attempts DROP CONSTRAINT inference_attempts_provider_check;
ALTER TABLE inference_attempts ADD CONSTRAINT inference_attempts_provider_check CHECK(provider = 'mock' OR provider ~ '^[a-z][a-z0-9_-]{0,63}$');
ALTER TABLE billing_imports DROP CONSTRAINT billing_imports_provider_check;
ALTER TABLE billing_imports ADD CONSTRAINT billing_imports_provider_check CHECK(provider <> 'mock' AND provider ~ '^[a-z][a-z0-9_-]{0,63}$');
ALTER TABLE billing_records DROP CONSTRAINT billing_records_provider_check;
ALTER TABLE billing_records ADD CONSTRAINT billing_records_provider_check CHECK(provider <> 'mock' AND provider ~ '^[a-z][a-z0-9_-]{0,63}$');
