# ADR 0002: v1 scope — data plane plus HTTP/OAuth providers first

- Status: Accepted
- Date: 2026-11-25

## Context

The production 9router instance supports a large provider registry, but only a small set is actually configured and used. The browser/web-cookie providers and the dashboard add substantial complexity. The new project should deliver a high-performance data plane without waiting for full product parity.

## Decision

v1 is a data-plane rewrite in Go:

1. Start with the HTTP/API-key and OAuth chat providers listed in `docs/GOALS.md`.
2. Include the client protocol adapters, provider registry, connections, token refresh, routing/fallback/combo, usage accounting, and minimal admin API.
3. Defer browser/web-cookie providers (`deepseek-web` and others), custom embeddings, image/media endpoints, and the dashboard to later phases.
4. Keep out-of-scope providers on the existing 9router deployment. They do not receive new connector acceptance work.
5. Create the repository as `codeasier/go-router`, public, with the goals committed before implementation.

## Consequences

Positive:

- A smaller, testable core with a clear performance target.
- Faster path to a usable replacement for the majority of production traffic.
- The existing 9router remains a working fallback during the transition.

Negative:

- Functional parity is intentionally incomplete until later phases.
- Two processes must be operated during migration.
- `deepseek-web` remains dependent on the JS implementation for now.

## Follow-ups

- Confirm the G3 performance targets after the M1 benchmark.
- Record ADRs for SQLite driver, config format, admin API, browser automation, and dashboard.
- Revisit scope if a new provider becomes active in production.
