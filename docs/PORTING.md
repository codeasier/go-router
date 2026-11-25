# Porting Scope and Invariants

Status: **M0 — initial scope**.

go-router is an independent Go implementation. 9router is used as a behavioral reference and is MIT-licensed; go-router does not redistribute 9router source code.

## 1. In-scope providers

### Phase 1 — chat data plane

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

### Phase 2

- `deepseek-web` (web-cookie; remains on 9router until Phase 2)
- explicitly approved browser/web-cookie providers
- custom embedding provider nodes
- image/media endpoints for in-scope providers

### Excluded

- Providers not listed above.
- Test fixtures and stale connections such as `kimchi-nope` and bulk `openai-compatible-*` test rows.
- Async provider task APIs, video, background task polling.
- Multi-node exactly-once budgets and cross-node account pools.
- MITM, user management, budgets, pricing, or billing ledgers.

Out-of-scope providers remain on the existing 9router deployment. They are not part of go-router acceptance.

## 2. Behavioral invariants

The following must be preserved for every in-scope provider:

- Client request and response shapes, including tool calls, images, and unknown-field handling where the protocol allows.
- SSE framing, event order, heartbeat/keepalive behavior, and terminal frames.
- Error envelopes and status codes, including provider-specific errors and the connector error header where applicable.
- Alias, combo, fallback, account/connection selection, and request-scoped routing.
- Usage semantics: actual provider units when available; `null` plus quality/coverage when missing; no invented zero usage.
- Credential lifecycle: refresh before use; save rotated credentials before adopting them; ownership does not cross connections.
- No silent retries. A dispatch that may have reached the provider is `UNKNOWN` and must not be replayed without a new receipt.
- Cancellation closes the upstream body; capacity and connection resources are released only after actual close.
- Control operations (refresh, catalog, probe, upload, download, poll) are distinct from inference calls.
- Strict proxy mode: proxy failure does not fall back to a direct connection.

## 3. Mapping from 9router

| 9router area | go-router package | Notes |
|---|---|---|
| `open-sse/executors/base.js` | `internal/dispatch`, `internal/provider` | Shared request build, retry classification, cancellation |
| `open-sse/providers/registry/*` | `internal/registry` | Provider metadata and model catalog |
| `open-sse/services/tokenRefresh/*` | `internal/auth` / provider adapters | OAuth refresh and rotation |
| `open-sse/handlers/chatCore.js` | `internal/server` + `internal/router` | Request lifecycle and routing |
| `open-sse/services/combo.js` | `internal/router` | Combos and fallback |
| `open-sse/services/usage.js` | `internal/usage` | Usage normalization |
| `src/lib/db/*` | `internal/store` | SQLite schema and migrations |

The JS implementation is the reference for behavior, not a source to copy.

## 4. Migration

1. **Dual run**: run go-router on a separate port while 9router remains the production gateway.
2. **Read-only import**: load connections, aliases, combos, and settings from a 9router SQLite snapshot. Never write to the production data directory without explicit approval.
3. **Parity harness**: replay normalized requests through both implementations and compare responses, SSE events, usage, and error envelopes.
4. **Cutover**: switch clients or the upstream caller to go-router; keep 9router warm for rollback.
5. **Rollback**: documented procedure to restore the previous 9router endpoint and configuration.

## 5. Compatibility evidence

- Golden request/response fixtures for each in-scope provider and client protocol.
- SSE event-level comparison, not only final text.
- Usage and error-envelope comparison.
- Cancellation and disconnect tests.
- Load/soak results against the G3 targets.

## 6. Attribution

9router is MIT-licensed. See `NOTICE` for attribution. go-router reimplements behavior and does not copy source files.
