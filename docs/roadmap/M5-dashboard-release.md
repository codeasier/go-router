# M5 — Dashboard, Parity, and Release

Status: **planned**.

## Objective

Expand the product surface after the [two-instance operations pilot](fleet-pilot.md), prove full-scope parity and load behavior, and ship the expanded release with documented cutover and rollback.

The **core v1 release candidate** is a separate gate defined in [GOALS.md](../GOALS.md#5-success-criteria): Phase 1 parity, the two-instance pilot, seven-day soak, security review, and staged cutover and rollback rehearsal, along with the other core criteria. Run those checks on core scope without waiting for M4 or the expanded Dashboard. The M4 dependencies below apply to validation of the expanded release, not to core qualification.

## Issues

| Issue | Title | Depends on |
|---|---|---|
| M5.1 | Expand the Dashboard beyond the fleet pilot | Fleet pilot, M3.3 |
| M5.2 | Full parity suite against 9router | M2, M4 |
| M5.3 | 7-day soak and load test | M2, M3, M4 |
| M5.4 | Cutover/rollback runbook and release | M5.1–M5.3 |

## M5.1 — Expand the Dashboard beyond the fleet pilot

Scope:

- Extend the separately deployed Dashboard and control API established in the pilot. The pilot chooses its initial technology and authentication, not M5.
- Provider/connection status, usage summaries, and request diagnostics.
- Secret-safe views; no decrypted credentials in the browser.
- Migration/import status.

Acceptance criteria:

- ADR records the choice and consequences.
- Dashboard auth and authorization tests pass.
- No secret or full request body appears in the UI or API.

## M5.2 — Full parity suite against 9router

Scope:

- Normalized request replay against 9router and go-router.
- Compare responses, SSE events, usage, errors, and cancellation.
- Provider matrix for all supported providers and client protocols.
- Documented known differences.

Acceptance criteria:

- All supported providers pass parity tests or have an approved, documented difference.
- No silent retry or usage-zeroing difference remains.
- CI runs the parity suite on synthetic fixtures.

## M5.3 — 7-day soak and load test

Scope:

- Concurrent SSE streams, disconnects, cancellations, and reconnects.
- Memory, goroutine, file-descriptor, and connection leak detection.
- G3 performance targets and graceful shutdown under load.
- Failure injection for upstream latency and proxy failures.

Acceptance criteria:

- 7-day soak shows no unbounded growth.
- G3 targets are met or the report documents the gap and approved follow-up.
- Shutdown under load does not truncate completed responses.

## M5.4 — Cutover/rollback runbook and release

Scope:

- Dual-run procedure and traffic cutover.
- Rollback to 9router.
- Release binaries and container images for Linux/amd64, Linux/arm64, and macOS/arm64.
- Release notes, known limitations, and support matrix.

Acceptance criteria:

- Runbook is tested on a staging deployment.
- Rollback is verified.
- Release artifacts are signed/checksummed and reproducible.
- The core release criteria in [GOALS.md](../GOALS.md#5-success-criteria) remain satisfied, and the expanded release meets the additional M5 criteria.

## Exit criteria

- All core release gates remain satisfied; parity, soak and staged rollback are verified for the expanded scope.
- Release notes and known limitations are published.
- Cutover and rollback are proven.
