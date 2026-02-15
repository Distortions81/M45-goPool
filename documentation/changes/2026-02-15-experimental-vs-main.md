## Experimental vs `main`

- **Scope:** `14 files`, `+100/-2`.

### Runtime / VarDiff
- Added `[mining].reject_no_job_id` (boolean, default `false`).
- When `reject_no_job_id = true`, empty `job_id` in `mining.submit` is rejected with Stratum error `20` (`job id required`).
- When `reject_no_job_id = false`, empty `job_id` continues into stale-job handling and is rejected as `job not found` (Stratum error `21`).

### Saved Workers / Storage / Privacy
- No saved-worker storage schema changes.
- No privacy or identity field changes.

### Saved Workers Runtime
- No saved-worker endpoint/runtime behavior changes.

### Overview / API / Caching
- Added `reject_no_job_id` to generated base config output (`config.toml`) and runtime effective config output.
- Added TOML mapping for `[mining].reject_no_job_id` in load/build paths.

### Admin / Ops
- Added admin runtime setting toggle `reject_no_job_id` in `/admin` settings.
- Admin form parsing now applies `reject_no_job_id` from checkbox state.

### Docs / Tests
- Updated config/example docs (`config.toml.example`, generated config comments, `documentation/operations.md`) with `reject_no_job_id` semantics and default.
- Added tests for parser behavior toggle and admin toggle handling.
