# Project workflow

- Preserve existing work and keep changes scoped to the current development stage.
- At the user's request, commit and push each completed, validated stage to GitHub before starting the next stage. Never force-push or overwrite remote work. Report the commit and destination, and report a push as complete only after verifying the remote ref.
- Run `bash scripts/verify.sh` for backend stages. It requires the pinned Go toolchain and a dedicated PostgreSQL test database or local Docker. Do not count skipped integration tests as successful validation.
- For Web stages, also run the frozen dependency install, type check, production build and relevant browser tests. Keep frontend dependencies locked.
- The integrated Web check is `AIGW_VERIFY_WEB=1 bash scripts/verify.sh`; install Playwright Chromium or set `PLAYWRIGHT_CHROMIUM_EXECUTABLE` to an existing browser. This runs a real PostgreSQL/OIDC test fixture and must not be replaced with a static mock-page check.
- Never commit credentials, local environment files, generated browser session data or real model payloads. `.env.example` contains requirements and harmless defaults only.
- Keep API contracts, architecture decisions and stage status aligned with the implementation. Distinguish mock integration evidence from real identity-provider/model-provider validation.
- Database changes must append numbered SQL under `internal/database/migrations`; never edit released migrations or legacy compatibility fixtures. Register new managed tables, test upgrade/rollback/readiness, and use `cmd/migrate up|status|check`. Normal server startup must not silently modify schema.
