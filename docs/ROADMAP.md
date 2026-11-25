# go-router Roadmap

Status: **M0 complete; M1 next**.

This roadmap is the execution plan for [GOALS.md](GOALS.md) and [PORTING.md](PORTING.md). It is tracked with GitHub milestones and issues. Each issue is a unit of reviewable work; a milestone closes only when its exit criteria and documentation are complete.

## Principles

1. Data plane first, with fleet operation interfaces defined alongside M1. The first two-instance pilot includes a minimal Dashboard; browser automation is later.
2. No silent retries: execution facts are part of the design, not an afterthought.
3. Benchmark-driven: every performance claim has a reproducible command and a 9router baseline.
4. Small PRs: one issue, one PR, one reviewable change; no mixed refactors.
5. Public repo hygiene: synthetic fixtures only; no secrets, production data, or full request bodies.
6. Backward compatibility: existing clients keep their protocols and error envelopes.

## Milestone overview

| Milestone | Goal | Exit criterion | Detailed plan |
|---|---|---|---|
| M0 | Initialize repo, goals, architecture, ADRs, CI | CI green; goals reviewed | [M0](roadmap/M0-initialization.md) |
| M1 | Core HTTP data plane + first API-key providers | Phase 1 API-key parity; benchmark report; no silent retries | [M1](roadmap/M1-core-data-plane.md) |
| M2 | OAuth providers + routing + protocol adapters | All Phase 1 providers pass protocol/cancellation/usage tests | [M2](roadmap/M2-oauth-routing.md) |
| M3 | SQLite, admin API, observability, packaging | Import reproducible; security review; non-root container | [M3](roadmap/M3-persistence-ops.md) |
| M4 | Browser/web-cookie providers + embeddings/media | Phase 2 tests pass with isolation; no bypass | [M4](roadmap/M4-phase2-data-plane.md) |
| M5 | Advanced Dashboard, full parity, soak, release | Full release requirements met; release notes published | [M5](roadmap/M5-dashboard-release.md) |

## Dependency and critical path

```text
M0 (done)
  |
  v
M1 core data plane
  |\
  | +--> M3 persistence/admin/ops (after M1.1 and M1.2)
  v
M2 OAuth + routing
  |
  v
M4 phase 2 data plane (browser/embeddings/media)
  |
  v
M5 advanced dashboard + full parity + release
```

M1 remains the critical path for the proxy. In parallel, define the [versioned management contract and two-instance pilot](roadmap/fleet-pilot.md). A minimal Dashboard, rollback and safe ingress control are required for the pilot, not dependent on M4 or on M5 advanced UI. M3 storage work can begin once required interfaces stabilize. The minimal durable attempt journal must exist before M1 sends inference; the rest of SQLite/admin/import remains in M3. Existing GitHub issue numbers remain unchanged; the pilot document defines the cross-milestone scope.

## Cross-cutting workstreams

- **Performance**: benchmark harness in M1; continuous comparison in M2/M5; no optimization without a measured baseline.
- **Reliability**: dispatch intent, retry classes, cancellation, crash/fault injection from M1 onward.
- **Security**: secret redaction, SSRF, strict proxy, and a no-secrets review at M3 and before release.
- **Documentation**: ADRs for irreversible decisions; a provider matrix per adapter; cutover runbook.
- **Testing**: golden fixtures, upstream simulator, race detector, fuzz for parsers, parity suite.

## Definition of Done

Every issue must satisfy:

- [ ] Acceptance criteria in the issue are met.
- [ ] Unit/integration tests cover the new behavior; golden fixtures updated if protocol behavior changes.
- [ ] `gofmt`, `go vet`, `go test -race ./...`, and `go build ./...` pass.
- [ ] No secrets, production data, or full request/response bodies in code, tests, logs, or fixtures.
- [ ] Execution semantics documented: what is dispatched, what may have executed, and what is retried.
- [ ] Performance-affecting changes include a benchmark or a justification for not running one.
- [ ] Documentation or ADR updated.
- [ ] One PR linked to the issue; no mixed unrelated changes.

## Issue tracking

- GitHub milestones: `M0` … `M5`.
- Labels: `kind:*`, `area:*`, `priority:*`.
- Offline issue map: [roadmap/ISSUES.md](roadmap/ISSUES.md).
- Each milestone document lists its issues and acceptance criteria. GitHub issues are the source of truth for status; the docs are the plan.

## Risk register

| Risk | Impact | Mitigation |
|---|---|---|
| Browser automation is complex and fragile | M4 delay | Keep it out of v1; ADR and isolation first |
| OAuth/provider APIs drift | M2 parity failures | Golden fixtures; live smoke tests only with approved test accounts |
| 9router and go-router diverge | Migration errors | Parity harness; dual-run; normalized comparisons |
| Performance targets are wrong | Rework | M1 benchmark first; targets revised with evidence |
| Secret leakage in a public repo | Security incident | Synthetic fixtures; redaction tests; review gate |
| Scope creep from inactive providers | Missed milestones | In-scope list is explicit; out-of-scope stays on 9router |

## Related documents

- [Goals and success criteria](GOALS.md)
- [Architecture](ARCHITECTURE.md)
- [Porting scope and invariants](PORTING.md)
- [ADR index](adr/)
