# M3 — Persistence, Admin API, and Operations

Status: **planned**.

## Objective

Make go-router operable as a long-running service: persistent connections and configuration, a minimal admin API, observability, packaging, and a security review.

## Issues

| Issue | Title | Depends on |
|---|---|---|
| M3.1 | SQLite driver ADR, schema, and migrations | M1.1 |
| M3.2 | Repositories and read-only 9router import | M3.1 |
| M3.3 | Minimal admin API | M3.2 |
| M3.4 | Observability and operations | M1.3, M3.1 |
| M3.5 | Packaging and security review | M3.3, M3.4 |

## M3.1 — SQLite driver ADR, schema, and migrations

Scope:

- Choose pure-Go `modernc.org/sqlite` or CGO `mattn/go-sqlite3` with benchmark evidence.
- Schema for connections, provider nodes, aliases, combos, settings, usage, and dispatch records.
- Migration framework and schema versioning.
- WAL mode, busy timeout, and single-writer discipline.

Acceptance criteria:

- ADR records the driver choice and consequences.
- Empty database and upgrade migrations are tested.
- Concurrent read/write benchmark is documented.
- Schema contains no plaintext secrets.

## M3.2 — Repositories and read-only 9router import

Scope:

- Repository APIs for connections, provider nodes, aliases, combos, and settings.
- Read-only import from a 9router SQLite snapshot.
- Validation and conflict reporting during import.
- Dry-run mode and a summary of imported/skipped records.

Acceptance criteria:

- Import is idempotent and read-only.
- Production `DATA_DIR` is never opened without explicit approval.
- Legacy IDs such as `xiaomi-tokenplan-cn` are mapped and reported.
- Tests cover missing tables, malformed rows, and duplicate IDs.

## M3.3 — Minimal admin API

Scope:

- Authenticated read/write endpoints for status, providers, connections, and usage summaries.
- Secret-safe responses; write operations require explicit authorization.
- Keep the M3 admin endpoints API-first. This does not defer the separately deployed [fleet-pilot Dashboard](fleet-pilot.md) or its initial management contract.

Acceptance criteria:

- Admin API auth tests pass; service keys cannot mutate configuration.
- Responses mask secrets and never include decrypted credentials.
- Connection CRUD and usage summary tests pass.

## M3.4 — Observability and operations

Scope:

- Prometheus metrics: requests, attempts, retry classes, SSE streams, bytes, latency, provider health.
- Structured JSON logs with request/attempt IDs.
- Optional pprof, disabled by default.
- Liveness vs readiness.
- Graceful shutdown and drain metrics.

Acceptance criteria:

- Metrics endpoint is covered by tests.
- Logs contain no bodies or secrets.
- Readiness reflects missing/invalid provider configuration.
- Shutdown test proves no truncated response within the drain deadline.

## M3.5 — Packaging and security review

Scope:

- Docker image (non-root, minimal runtime, read-only root filesystem where possible).
- systemd unit example.
- Secret redaction tests and log review.
- SSRF checks for user-supplied URLs.
- Strict proxy behavior: proxy failure does not fall back to direct.
- No-secrets review of docs and fixtures.

Acceptance criteria:

- Container runs as non-root and passes a smoke test.
- Security checklist is committed and signed off.
- Proxy failure and SSRF tests pass.
- No secret material in repository history or fixtures.

## Exit criteria

- Import from a 9router snapshot is reproducible.
- Admin API and metrics pass tests.
- Container image runs as non-root.
- Security review is complete with no open critical findings.

## Risks

- Schema coupling to 9router may slow evolution; keep import read-only and versioned.
- Admin API becomes a security surface; keep it minimal and authenticated.
