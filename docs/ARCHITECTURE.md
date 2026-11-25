# go-router Architecture

Status: **M0 complete; M1 next**. Implementation package names and interfaces may change through an ADR.

## 1. High-level shape

```text
client
  |
  v
[ HTTP ingress ]
  |  auth, request limits, protocol detection
  v
[ Router ] -- alias / combo / fallback / smart selection
  |
  v
[ Provider adapter ] -- request build, auth, transport selection
  |
  v
[ Dispatcher ] -- dispatch intent, wire attempt, retry class, cancellation
  |
  v
upstream provider
  |
  v
[ Stream translator ] -- SSE framing, protocol projection
  |
  v
[ Settlement ] -- usage, result, unknown range, request close
  |
  v
client
```

The JS 9router engine remains the reference implementation and the fallback for out-of-scope providers. go-router runs separately from 9router.

The Dashboard and fleet controller are hosted in this repository but deployed separately from gateway nodes. Inference never traverses either. Nodes expose a separately authenticated management surface for stable identity, capabilities, health, applied revision and telemetry. The controller validates and reconciles versioned desired configuration and manages ingress policy through a separate adapter. See [ADR 0003](adr/0003-fleet-control-plane.md).

## 2. Packages

```text
cmd/go-router/          process entry point, flags, signals
internal/server/        HTTP server, routing table, middleware, health
internal/config/        file + env configuration, validation, redaction
internal/auth/          service keys, provider credential resolution
internal/registry/      provider metadata and model catalog
internal/router/        aliases, combos, fallback, account selection
internal/provider/      provider adapters
internal/dispatch/      wire attempts, retry classes, barriers, cancellation
internal/stream/        SSE parsing/framing and client protocol projection
internal/usage/         usage normalization and quality/coverage
internal/store/         SQLite persistence and migrations
internal/import9r/      read-only 9router import (later)
internal/observability/ logs, metrics, tracing, pprof
internal/version/       build metadata
```

Rules:

- `internal/dispatch` is the only package allowed to call the upstream HTTP transport for inference.
- Provider adapters build requests and parse responses; they do not retry on their own.
- Control operations (token refresh, catalog, upload, download, poll) are declared explicitly and never counted as inference calls.
- `open-sse`-style provider behavior is reimplemented, not imported.

## 3. Request lifecycle

1. **Ingress**: accept HTTP, authenticate the service key, assign a request ID, enforce body/rate limits.
2. **Normalize**: detect the client protocol (OpenAI Chat, Anthropic Messages, OpenAI Responses), parse the request, preserve unknown fields where the protocol allows.
3. **Route**: resolve alias/combo, choose provider and connection, apply fallback order, check capacity.
4. **Prepare**: refresh credentials if needed, build the upstream request, record a dispatch intent.
5. **Dispatch**: send the request through the dispatcher. On network/status failure, classify the result before deciding whether a retry is allowed.
6. **Stream**: translate upstream SSE chunks to the client protocol. Backpressure is applied by the consumer; cancellation closes the upstream body.
7. **Settle**: record the final result and usage snapshot; release capacity and connection resources.
8. **Deliver**: finish the client response, flush, and close.

## 4. Execution facts

Every inference attempt produces a durable record with at least:

- request ID, attempt ID, provider, model, connection ID, operation type
- dispatch intent timestamp and body digest
- result (`NOT_SENT`, `REJECTED_WITHOUT_EXECUTION`, `EXECUTED`, `UNKNOWN`)
- last known usage and its quality/coverage
- retry decision and the receipt for any resend

The dispatcher must fail closed when a required record cannot be committed. A client-visible `UNKNOWN` must never be silently converted into a retry.

## 5. Provider adapter contract

The exact interface is not frozen yet. The intended responsibilities are:

```go
type Adapter interface {
    ID() string
    Prepare(ctx context.Context, req *Request) (*UpstreamRequest, error)
    Decode(resp *http.Response) (*Response, error)
    Stream(ctx context.Context, resp *http.Response, sink StreamSink) error
}
```

Adapters may use a shared base implementation for:

- JSON body construction and field normalization
- auth header application
- transport/format selection
- SSE error detection
- tool-call and usage extraction

## 6. Storage

v1 starts with SQLite so a 9router snapshot can be imported read-only. Candidate schema areas:

- provider connections (secrets encrypted)
- provider nodes and custom OpenAI-compatible providers
- aliases, combos, and routing settings
- service keys and access policy
- usage events and request summaries
- dispatch/attempt records

Open decision: pure-Go `modernc.org/sqlite` for a single static binary versus CGO `mattn/go-sqlite3` for speed. The choice must be benchmarked with concurrent reads/writes.

## 7. Configuration

- One config file (YAML is the default proposal) plus environment overrides.
- Secrets can reference files or environment variables; exported config redacts them.
- Provider/model catalogs can be loaded from the config or a JSON catalog file.
- Validation happens at startup; invalid config fails fast with actionable errors.

## 8. Observability

- Structured JSON logs with request/attempt IDs; no bodies or secrets by default.
- Prometheus metrics: requests, attempts, retry classes, SSE streams, bytes, latency histograms, provider/connection health.
- Optional pprof, disabled by default.
- Health endpoints distinguish process liveness from upstream readiness.

## 9. Testing

- Unit tests with `httptest` upstream simulators.
- Golden fixtures for protocol translation and SSE ordering.
- Table-driven provider matrices for aliases, headers, body normalization, and error mapping.
- Race detector on concurrent stream tests.
- Fuzz tests for SSE and JSON parsers.
- Parity harness: replay normalized requests against 9router and go-router, compare responses, usage, and error envelopes.
- Load/soak harness for G3 targets.

## 10. Deployment

- Single binary, static by default where dependencies allow.
- Docker image (distroless or minimal) with non-root user and read-only root filesystem.
- systemd unit example.
- Configuration and data directories are explicit; no writes to the 9router production data directory.

## 11. Open decisions

- SQLite driver and migration strategy.
- Config format and secret provider.
- Admin API surface and authentication.
- Browser automation approach for Phase 2.
- Dashboard runtime, management authentication, and ingress adapter details for the first fleet pilot.
- Whether to reuse the 9router provider catalog JSON or generate a Go-native catalog.
