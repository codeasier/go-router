# M2 — OAuth Providers and Routing

Status: **planned**.

## Objective

Add the OAuth and specialized HTTP providers required by the active production set, plus aliases, combos, fallback/account selection, and the remaining client protocol adapters.

## Issues

| Issue | Title | Depends on |
|---|---|---|
| M2.1 | OAuth connection model and token refresh framework | M1.1, M1.3 |
| M2.2 | codex and codex-backup providers | M2.1 |
| M2.3 | antigravity provider | M2.1 |
| M2.4 | cursor, xai, and xiaomi-tokenplan providers | M2.1 |
| M2.5 | grok-cli, minimax-code, trae, and trae-enterprise providers | M2.1 |
| M2.6 | Routing, combos, fallback, account selection, Anthropic/Responses adapters | M1.4, M2.1 |

## M2.1 — OAuth connection model and token refresh framework

Scope:

- Connection model: provider, account identity, access/refresh token, expiry, priority, active state, proxy pool.
- Refresh before use; rotated credentials saved successfully before adoption.
- Refresh deduplication and per-connection ownership.
- Failure behavior: no blind retry of an ambiguous token exchange.
- Secret encryption at rest and masking in logs.

Acceptance criteria:

- Refresh success, failure, cancellation, and rotation tests pass.
- A failed save never yields a new credential to the caller.
- Concurrent refresh for the same connection is deduplicated.
- Different connections never cross-adopt tokens.

## M2.2 — codex and codex-backup

Scope:

- OAuth login/refresh.
- Responses API transport, sessions, tools, and image inputs.
- `codex` and `codex-backup` as independent connection lanes with shared model behavior.
- Distinct session/cache scopes per provider ID.
- Usage and error mapping.

Acceptance criteria:

- Streaming and non-streaming parity tests for both IDs.
- Session scope does not mix between `codex` and `codex-backup`.
- Cancellation and disconnect tests pass.
- No silent retry after a possible dispatch.

## M2.3 — antigravity

Scope:

- OAuth and identity headers.
- Multi-URL transport, session/thought-signature behavior.
- Quota/error parsing and retry-delay behavior.
- Chat and tool calls.

Acceptance criteria:

- Protocol fixtures cover session, tools, quota errors, and streaming.
- Retry decisions use the provider delay without bypassing the dispatcher.
- Usage quality is recorded; missing usage is not invented.

## M2.4 — cursor, xai, and xiaomi-tokenplan

Scope:

- `cursor`: OAuth and specialized request/stream behavior.
- `xai`: API-key and OAuth modes.
- `xiaomi-tokenplan`: API-key transport and usage.
- Aliases and model catalogs.

Acceptance criteria:

- Each provider has auth, streaming, non-streaming, error, and cancellation tests.
- The legacy `xiaomi-tokenplan-cn` ID maps to `xiaomi-tokenplan`.
- No provider-specific bypass of dispatch accounting.

## M2.5 — grok-cli, minimax-code, trae, and trae-enterprise

Scope:

- `grok-cli`: OAuth and specialized HTTP transport.
- `minimax-code`: OAuth, session/header behavior.
- `trae` and `trae-enterprise`: OAuth and the new SOLO chat protocol.
- Credential refresh and cancellation for each.

Acceptance criteria:

- Protocol fixtures for streaming, errors, and cancellation.
- `trae` and `trae-enterprise` use distinct gateways and do not fall back across tenants.
- Usage and error envelopes match the reference.
- No silent retry after a possible dispatch.

## M2.6 — Routing and protocol adapters

Scope:

- Alias resolution.
- Combos and request-scoped fallback.
- Account/connection selection and priority.
- Anthropic Messages and OpenAI Responses adapters for supported routes.
- Error propagation across fallback attempts.

Acceptance criteria:

- Fallback tests prove a failed attempt is not retried when it may have executed.
- Combo tests cover success, failure, cancellation, and usage aggregation.
- Protocol adapters pass golden tests for streaming and non-streaming.

## Exit criteria

- All phase 1 providers pass streaming, non-streaming, tool, error, cancellation, and usage tests.
- OAuth refresh and rotation tests pass.
- Routing/fallback tests prove no silent retries.
- Provider matrices are documented.

## Risks

- OAuth endpoints and token formats change without notice.
- Specialized provider transports may not be reproducible without synthetic fixtures.
- Trae's protocol is relatively new; isolate changes behind fixtures.
