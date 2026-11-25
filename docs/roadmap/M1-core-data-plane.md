# M1 — Core HTTP Data Plane

Status: **next**.

## Objective

Deliver a usable, benchmarked OpenAI-compatible data plane for the first API-key/generic HTTP providers, with explicit execution facts and no silent retries.

## Issues

| Issue | Title | Depends on |
|---|---|---|
| M1.1 | Config, auth, and HTTP server skeleton | M0 |
| M1.2 | Provider registry and model catalog | M1.1 |
| M1.3 | Dispatcher with execution facts and retry classes | M1.1 |
| M1.4 | OpenAI-compatible chat completions and models endpoints | M1.2, M1.3 |
| M1.5 | First providers: volceapi, openrouter, opencode-go, custom OpenAI-compatible | M1.4 |
| M1.6 | Golden tests, upstream simulator, benchmark harness, 9router baseline | M1.5 |

## M1.1 — Config, auth, and HTTP server skeleton

Scope:

- Config file (YAML) and environment overrides.
- Startup validation with actionable errors.
- Secret references by file/env; redacted config export.
- Service API-key authentication for ingress.
- Request IDs, body limits, and rate limits.
- `/healthz` and readiness endpoints.
- Graceful shutdown with bounded drain.

Acceptance criteria:

- Invalid config fails fast with a non-zero exit and a redacted error.
- Unauthorized requests are rejected consistently.
- Request IDs appear in logs and error envelopes.
- Shutdown drains in-flight requests until the deadline, then cancels.

## M1.2 — Provider registry and model catalog

Scope:

- Go-native provider metadata: ID, alias, display name, category, transport, models.
- Model catalog loading from config or JSON.
- Alias resolution and model metadata lookup.
- Transport/format selection per provider and model.
- Registry validation (duplicate IDs, missing transports).

Acceptance criteria:

- The in-scope phase 1 providers are represented with aliases and models.
- Unknown provider/model requests fail with a clear error.
- Registry tests cover duplicate, missing, and malformed entries.

## M1.3 — Dispatcher with execution facts and retry classes

Scope:

- Single upstream transport boundary for inference calls.
- Durable dispatch intent before sending.
- Attempt record with provider, model, connection, operation type, and body digest.
- Retry classes: `NOT_SENT`, `REJECTED_WITHOUT_EXECUTION`, `EXECUTED`, `UNKNOWN`.
- Cancellation propagation and context deadlines.
- Fail-closed behavior when a required record cannot be committed.

Acceptance criteria:

- A request that may have reached the provider is never replayed without a new receipt.
- Client cancellation closes the upstream body.
- Tests prove each retry class and the fail-closed path.
- No provider adapter calls the upstream transport directly.

## M1.4 — OpenAI-compatible chat completions and models endpoints

Scope:

- `GET /v1/models`.
- `POST /v1/chat/completions`, streaming and non-streaming.
- Tool calls, image inputs, and unknown-field preservation where supported.
- SSE framing, terminal frames, and error events.
- Client error envelopes and status codes.
- Usage extraction and normalization.

Acceptance criteria:

- Non-streaming and streaming golden tests pass.
- Tool calls and usage match the reference behavior.
- Errors preserve the client protocol envelope.
- Cancellation and disconnect tests pass.

## M1.5 — First providers

Scope:

- Adapter base: request build, auth headers, transport, SSE error detection, usage.
- `volceapi` (OpenAI/Claude/Responses transports).
- `openrouter`.
- `opencode-go`.
- Custom OpenAI-compatible provider nodes.

Acceptance criteria:

- Each provider has a protocol fixture and a streaming/non-streaming test.
- Auth header and base URL behavior match the reference.
- No provider-specific retry bypasses the dispatcher.
- Usage quality/coverage is recorded; missing usage is not zero-filled.

## M1.6 — Golden tests, upstream simulator, benchmark harness, 9router baseline

Scope:

- `httptest` upstream simulator with scripted SSE and JSON responses.
- Golden fixtures for each M1 provider and client protocol.
- Benchmark harness: startup, idle RSS, binary size, p99 overhead, SSE TTFB, throughput, concurrent streams.
- Comparison against the current 9router JS baseline on the same host.

Acceptance criteria:

- `go test -race ./...` passes.
- Benchmark commands and raw results are committed under `benchmarks/` or `docs/benchmarks/`.
- The report compares go-router and 9router and states any missed G3 target.

## Exit criteria

- M1.1–M1.6 complete.
- Phase 1 API-key providers pass parity tests.
- The benchmark report exists and is reproducible.
- No silent retry path exists.

## Risks

- The reference behavior for custom OpenAI-compatible providers is underspecified; fixtures must be synthetic and explicit.
- Benchmark results can be noisy; pin the host and record the environment.
