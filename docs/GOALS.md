# go-router Goals

Status: **M0 complete; M1 next**. This document is the v1 contract. The fleet operations decision is recorded in [ADR 0003](adr/0003-fleet-control-plane.md).

## 1. Vision

go-router is a lightweight AI gateway designed to replace in-scope 9router traffic while eliminating duplicated multi-instance operations. One separately deployed Dashboard and control plane, hosted in this repository, manage a fleet of light gateway nodes. It must be a drop-in for existing clients, while making execution facts explicit: what was dispatched, what may have executed, what was retried, and what is safe to retry.

## 2. Language decision

Go was selected over Rust for v1. The dominant cost in an AI gateway is upstream latency, not local CPU. Go provides goroutine-based concurrency, simple deployment, fast iteration, and a mature HTTP/2/SSE ecosystem. A Rust hot path can be added later if benchmarks prove a local bottleneck.

## 3. Primary goals

### G1. Functional parity for the in-scope data plane

Acceptance criteria:

- OpenAI-compatible `POST /v1/chat/completions` with streaming and non-streaming responses.
- `GET /v1/models` exposes the configured provider/model catalog.
- The existing client protocol adapters used by in-scope providers: OpenAI Chat, Anthropic Messages, and OpenAI Responses, including SSE framing and error envelopes.
- Provider registry: provider metadata, model catalog, aliases, transport selection.
- Connections: API keys, OAuth access/refresh tokens, priority, active state, proxy pools.
- Credential lifecycle: refresh before use; rotated credentials must be saved successfully before the new credential is used.
- Routing: alias resolution, combos, fallback/account selection, request-scoped routing, and smart routing where required by parity.
- Usage accounting: actual provider usage when available; `null` plus quality/coverage when missing; never invent zero-valued usage.
- Control operations (token refresh, catalog/probe, upload, download, poll) are distinguishable from model inference calls.

### G2. Execution correctness

Acceptance criteria:

- No silent retries. Every upstream dispatch is preceded by a recorded dispatch intent.
- Retry classes are explicit: `NOT_SENT`, `REJECTED_WITHOUT_EXECUTION`, `EXECUTED`, and `UNKNOWN`.
- A response that may have reached the provider is never replayed without a new attempt receipt.
- Client cancellation propagates to upstream request/response bodies; resources are released only after the body is closed.
- Credential refresh and rotation preserve the original account/connection ownership.
- Idempotency and replay semantics for requests with explicit idempotency keys are documented and tested.
- Missing or failed journal/commit operations fail closed: no new upstream dispatch without a durable record.

### G3. Performance

These are initial targets to validate in M1. They may be revised only with benchmark evidence.

Measured on a 4 vCPU / 8 GiB Linux host, loopback upstream, keep-alive, no TLS between go-router and the benchmark upstream:

| Metric | Target |
|---|---|
| Startup time | < 100 ms |
| Idle RSS | < 50 MiB |
| Stripped binary | < 30 MiB |
| Added p99 latency, non-streaming 1 KiB response, 1,000 concurrent | < 5 ms |
| Added p99 TTFB, SSE, 1,000 concurrent streams | < 2 ms |
| Throughput, small non-streaming requests | >= 10,000 req/s |
| 10,000 concurrent idle SSE streams | < 1 GiB RSS |
| Graceful shutdown drain | no truncated upstream response; bounded by config |

Every performance claim must include the benchmark command, host, Go version, upstream simulator, and comparison against the current 9router JS baseline on the same host.

### G4. Operability

Acceptance criteria:

- One static binary per gateway node; no Node.js runtime on the node. The Dashboard/control plane is deployed separately.
- Configuration from file + environment variables; secrets may be supplied by file or env and are never printed by config export.
- Structured JSON logs, Prometheus metrics, pprof (opt-in), health endpoints.
- Graceful shutdown with bounded drain.
- Docker image and systemd example.
- Linux/amd64, Linux/arm64, macOS/arm64 builds.

### G4a. Unified fleet operations

Acceptance criteria: register two nodes; publish a model change once and read back each node's applied revision; show partial failures; aggregate usage and deduplicate shared-account quota; manage traffic eligibility, weight and drain through the ingress; restore a previous configuration revision. Nodes keep serving the last valid local configuration when management is unavailable. See the [two-instance pilot](roadmap/fleet-pilot.md).

### G5. Security

Acceptance criteria:

- Service API-key authentication for ingress.
- Provider credentials are encrypted at rest and masked in logs, API responses, and diagnostics.
- Strict outbound proxy mode: proxy failure does not silently fall back to direct connections.
- SSRF protections for user-supplied URLs.
- No request/response bodies or secrets in default logs; debug capture is explicit, bounded, and redacted.

### G6. Migration and parity

Acceptance criteria:

- Runs alongside 9router on a different port.
- Import of provider connections, aliases, combos, and settings from 9router SQLite is read-only by default; writes require explicit confirmation.
- Golden protocol tests replay captured client requests against both implementations and compare normalized responses, SSE events, usage, and error envelopes.
- A documented cutover and rollback procedure.

## 4. In-scope providers

### Phase 1 — chat data plane (HTTP/API-key and OAuth)

API-key / generic HTTP:

- `volceapi`
- `openrouter`
- `xai` (API-key mode)
- `xiaomi-tokenplan` (legacy id `xiaomi-tokenplan-cn` maps here)
- custom OpenAI-compatible provider nodes
- `opencode-go`

OAuth / specialized HTTP:

- `codex`
- `codex-backup`
- `antigravity`
- `cursor`
- `grok-cli`
- `minimax-code`
- `trae`
- `trae-enterprise`
- `xai` (OAuth mode)

### Phase 2 — additional data plane

- `deepseek-web` (web-cookie; remains on the JS 9router until this phase)
- other browser/web-cookie providers only if explicitly added later
- custom embedding provider nodes
- image/media endpoints for in-scope providers (for example, Antigravity image generation)

### Out of scope for v1

- Advanced Dashboard features beyond the two-instance operations pilot.
- Providers not listed above. They remain on the existing 9router deployment and must not receive new connector acceptance work.
- Test fixtures and stale connections (`kimchi-nope`, bulk `openai-compatible-*` test rows).
- Async provider task APIs, video generation, and background task polling.
- Multi-node global exactly-once budgets and cross-node account pooling.
- MITM.
- User management, budgets, pricing, or billing ledgers. go-router is a trusted-caller data plane.

## 5. Success criteria

A v1 release candidate is ready only when:

1. All Phase 1 providers pass protocol parity tests for streaming, non-streaming, tools, errors, cancellation, and usage.
2. No silent retry path exists in any Phase 1 provider adapter; the execution-fact test suite proves dispatch intent and settlement.
3. The benchmark suite meets or beats the G3 targets against the current JS baseline.
4. A 7-day soak test with concurrent SSE streams shows no goroutine, memory, or file-descriptor leak.
5. Graceful shutdown, cancellation, and credential rotation pass fault-injection tests.
6. Migration from a 9router SQLite snapshot is documented and tested read-only.
7. Security review finds no secret leakage in logs, metrics, API responses, or error paths.
8. The two-instance fleet pilot passes its operations and recovery checks. Phase 2 providers and advanced UI are not prerequisites.

## 6. Constraints

- Do not access the production 9router `DATA_DIR` without explicit approval. Read-only snapshots are the default.
- Never commit provider credentials, OAuth tokens, API keys, captured personal data, or full request/response bodies.
- Do not copy 9router source code. Reimplement behavior; 9router is MIT-licensed and is attributed in `NOTICE`.
- Public repository: all examples and fixtures must be synthetic or redacted.
- Keep the existing 9router deployment running as the fallback until a documented cutover.

## 7. Open decisions

- SQLite driver: pure-Go (`modernc.org/sqlite`) versus CGO (`mattn/go-sqlite3`).
- Configuration format: YAML versus TOML.
- Admin API authentication and exposure model.
- Browser automation approach for Phase 2: in-process CDP versus a sidecar.
- Dashboard technology and the versioned control protocol (see ADR 0003).
- Final confirmation of G3 performance targets after the M1 benchmark.
