-- Input-only protocols reserve and settle zero generated output. Keep zero
-- output price and bound paired, while preserving existing Chat constraints.
ALTER TABLE newapi_execution_bindings DROP CONSTRAINT newapi_execution_bindings_output_price_check;
ALTER TABLE newapi_execution_bindings DROP CONSTRAINT newapi_execution_bindings_max_output_check;
ALTER TABLE newapi_execution_bindings ADD CONSTRAINT newapi_execution_bindings_output_price_check CHECK(output_price BETWEEN 0 AND 9007199254740991);
ALTER TABLE newapi_execution_bindings ADD CONSTRAINT newapi_execution_bindings_max_output_check CHECK(max_output BETWEEN 0 AND 1000000);
ALTER TABLE newapi_execution_bindings ADD CONSTRAINT newapi_execution_bindings_output_pair_check CHECK((output_price=0 AND max_output=0) OR (output_price>0 AND max_output>0));
