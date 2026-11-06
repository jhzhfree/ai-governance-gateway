# Product engineering rules

- Read `docs/delivery.md` and the relevant ADR before changes. Preserve unrelated repositories and existing work.
- Bifrost imports belong only in `adapters/bifrost`. Domain packages must not expose provider SDK types.
- A test passing against a synthetic provider does not prove real provider compatibility, billing completeness, HA, or production readiness.
- Never use real paid providers in the default suite. Never store keys, user prompts, cookies, private keys, or environment files in the repository.
- Run `bash scripts/verify.sh` with the pinned compiler; report failures, skipped cases, and unverified claims separately.
- Do not weaken checksum, TLS, auth, budget or audit guarantees to make a test pass. Fix behavior or record a blocking acceptance gap.
- Keep physical attempts distinct from business requests. Do not add overlapping retry loops. Missing usage is unknown, never automatically free.
- Use append-only numbered database migrations when persistence is introduced. No implicit production schema change on gateway startup.
- Update the delivery matrix with measured evidence and remaining gaps. Do not claim a milestone is complete while mandatory acceptance cases are pending.
- Do not publish, push, deploy, migrate production data, or call external model services as part of routine validation without the corresponding task authorization.
