# go-router Roadmap

Status: **M0 — initialization**.

Each milestone has an exit criterion. A milestone is not complete until its tests and documentation are committed.

## M0 — Initialization

Deliverables:

- Public repository with goals, architecture, roadmap, porting scope, and ADRs.
- Minimal Go module and buildable `go-router` binary.
- `/healthz` endpoint, version metadata, graceful shutdown.
- CI: format check, vet, test, build.

Exit criterion: `go test ./...`, `go vet ./...`, and `go build ./...` pass in CI; goals are reviewed and committed.

## M1 — Core HTTP data plane

Deliverables:

- Config loader and validation.
- OpenAI-compatible `/v1/models` and `/v1/chat/completions`, streaming and non-streaming.
- Dispatcher with dispatch intent, retry classes, cancellation, and bounded shutdown.
- Provider adapter base and the first API-key/generic providers: `volceapi`, `openrouter`, `opencode-go`.
- Request/attempt records and usage normalization.
- Golden tests with `httptest` upstream simulators.
- Benchmark harness and a baseline comparison against 9router.

Exit criterion: Phase 1 API-key providers pass parity tests; benchmark report exists; no silent retry path.

## M2 — OAuth providers and routing

Deliverables:

- OAuth connection model and token refresh.
- Providers: `codex`, `codex-backup`, `antigravity`, `cursor`, `grok-cli`, `minimax-code`, `trae`, `trae-enterprise`, `xai`.
- Aliases, combos, fallback/account selection, request-scoped routing.
- Protocol adapters for Anthropic Messages and OpenAI Responses for the supported routes.
- Credential rotation save-before-use tests.
- Golden parity tests for OAuth and specialized transports.

Exit criterion: All Phase 1 providers pass streaming, non-streaming, tool, error, cancellation, and usage tests.

## M3 — Persistence, admin API, operations

Deliverables:

- SQLite store with schema and migrations (driver decision recorded in an ADR).
- Read-only import from 9router SQLite: connections, aliases, combos, settings.
- Minimal admin API for status, providers, connections, and usage summaries.
- Prometheus metrics, structured logs, optional pprof.
- Docker image and systemd example.
- Graceful shutdown, health/readiness separation.

Exit criterion: Migration from a snapshot is reproducible; metrics and logs pass the no-secrets review; container image runs as non-root.

## M4 — Phase 2 data plane

Deliverables:

- `deepseek-web` and other explicitly approved browser/web-cookie providers.
- Browser automation approach (in-process CDP or sidecar) recorded in an ADR.
- Custom embedding provider nodes.
- Image/media endpoints for in-scope providers.
- Fault-injection tests for browser processes, downloads, and cancellation.

Exit criterion: Phase 2 provider tests pass with the approved browser/network isolation; no request bypasses the dispatcher.

## M5 — Dashboard, parity, and release

Deliverables:

- Dashboard/admin UI (technology chosen by ADR).
- Full parity suite against 9router for the supported surface.
- 7-day soak test with concurrent SSE streams and zero leaks.
- Security review: secrets, SSRF, proxy behavior, redaction.
- Cutover and rollback runbook.
- Release binaries and container images.

Exit criterion: All success criteria in `docs/GOALS.md` are met; release notes and known limitations are published.
