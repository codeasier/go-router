# M4 — Phase 2 Data Plane

Status: **planned**.

## Objective

Extend go-router beyond HTTP/API-key and OAuth chat providers: browser/web-cookie providers, custom embeddings, and image/media endpoints for the in-scope providers.

## Issues

| Issue | Title | Depends on |
|---|---|---|
| M4.1 | Browser automation ADR and isolation | M2 |
| M4.2 | deepseek-web provider | M4.1 |
| M4.3 | Custom embeddings and image/media endpoints | M2, M3 |
| M4.4 | Browser/download fault injection and parity | M4.2, M4.3 |

## M4.1 — Browser automation ADR and isolation

Scope:

- Choose in-process CDP versus a separate browser sidecar.
- Process, network, and resource isolation.
- Per-request browser context and cookie handling.
- Deployment implications (container size, Chrome/Chromium dependency).

Acceptance criteria:

- ADR records the decision and alternatives.
- Isolation tests prove one request cannot access another request's cookies or pages.
- Resource limits and crash recovery are documented.

## M4.2 — deepseek-web provider

Scope:

- Cookie/session authentication.
- HTTP or browser path as required by the reference behavior.
- Streaming and non-streaming parity.
- Cancellation and session cleanup.
- No silent retry after a possible generation.

Acceptance criteria:

- Synthetic protocol fixtures pass.
- Cancellation closes the browser/HTTP resource.
- Session state does not leak across requests or connections.

## M4.3 — Custom embeddings and image/media endpoints

Scope:

- `/v1/embeddings` for custom embedding provider nodes.
- Image/media endpoints for in-scope providers (for example, Antigravity image generation).
- Media unit accounting and download/stream handling.
- Resource release on completion and cancellation.

Acceptance criteria:

- Embedding request/response and usage tests pass.
- Image/media generation, download, and cancellation tests pass.
- Media units are recorded without inventing missing usage.
- No async task API is exposed.

## M4.4 — Browser/download fault injection and parity

Scope:

- Browser crash, network loss, and timeout injection.
- Download interruption and cleanup.
- Parity comparison for Phase 2 providers.
- Soak test for browser contexts and file descriptors.

Acceptance criteria:

- Fault-injection tests pass with no leaked processes or temp files.
- Phase 2 provider parity tests pass.
- No request bypasses the dispatcher.

## Exit criteria

- Browser isolation is approved and documented.
- Phase 2 provider tests pass.
- Embeddings and media endpoints pass parity tests.
- No leaked browser processes, contexts, or temporary files.

## Risks

- Browser automation is the largest reliability risk in the project.
- Chromium/Chrome availability and headless behavior vary by platform.
- Web-cookie providers can change their UI/protocol without notice.
