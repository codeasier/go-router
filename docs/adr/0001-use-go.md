# ADR 0001: Use Go for go-router

- Status: Accepted
- Date: 2026-11-25

## Context

The current 9router connector is implemented in JavaScript/TypeScript. We want a higher-performance, easier-to-operate data plane. Candidate languages were Go, Rust, or a hybrid with a Rust hot path.

The dominant cost in an AI gateway is upstream model latency. Local CPU is usually a small fraction of request time. The implementation must support HTTP/2, SSE streaming, OAuth refresh, many provider quirks, and rapid iteration.

## Decision

Use Go for the v1 implementation.

## Consequences

Positive:

- Single static binary with no Node.js runtime.
- Simple concurrency model for many simultaneous streams.
- Fast build/test cycle and a mature HTTP ecosystem.
- Easier to reimplement the existing provider behavior than in Rust.
- Cross-compilation for Linux and macOS is straightforward.

Negative:

- Less absolute performance and memory efficiency than Rust.
- Garbage collection requires attention under very high stream counts.
- Some provider quirks require careful table-driven tests.

## Alternatives

- **Rust**: stronger performance and memory control, but slower iteration and a more complex async/TLS/browser ecosystem. Reconsider for a measured hot path if benchmarks justify it.
- **Node/TypeScript**: rejected; the current implementation is the problem being replaced.
- **Hybrid**: not selected for v1. A Rust native transport or parser can be added later behind a stable interface.
