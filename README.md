# go-router

**Status: early M1 and local fleet pilot.** By default the gateway exposes health/version, an empty model list and no Chat route. An [opt-in synthetic Chat rehearsal](docs/synthetic-chat.md) exercises the binary and durable journal without a model provider. A separate [read-only Dashboard](cmd/go-router-dashboard/README.md) can observe local nodes. Neither is production provider parity or a replacement for the two existing gateways.

go-router is a lightweight, multi-instance AI gateway written in Go. Lightweight gateway nodes proxy model requests; one separately running Dashboard and control plane manage multiple nodes. Both are developed in this repository. The gateway independently implements the in-scope 9router data-plane behavior.

## Why

The current 9router connector is a large JavaScript/TypeScript codebase. go-router keeps the client-facing protocol and provider behavior, but moves the data plane to Go for:

- a single static binary with no Node runtime
- predictable memory use under many concurrent SSE streams
- explicit execution facts: no hidden retries, clear cancellation and unknown outcomes
- a smaller, testable core for routing, fallback and usage accounting

## Scope (v1)

- OpenAI-compatible `/v1/chat/completions` (streaming and non-streaming), `/v1/models`, and the existing client protocol adapters used by the active providers.
- Provider registry, API-key and OAuth connections, token refresh, routing/fallback/combo, usage accounting.
- Phase 1 providers: HTTP/API-key and OAuth chat providers (see [docs/PORTING.md](docs/PORTING.md)).
- The [two-instance pilot](docs/roadmap/fleet-pilot.md) includes a minimal unified Dashboard for versioned model changes, node health, usage, quota and traffic controls. Browser/web-cookie and advanced Dashboard features are later.

## Non-goals (v1)

- Browser automation / web-cookie providers such as `deepseek-web` (phase 2).
- Full Dashboard feature parity or an embedded Dashboard on every gateway node.
- Async provider task APIs, video generation, multi-node exactly-once budgets, and MITM.

## Quick start

```bash
go run ./cmd/go-router -addr :20128
curl http://127.0.0.1:20128/healthz
```

## Documentation

- [Single-instance generic Chat pilot](docs/generic-chat-pilot.md)
- [Goals and success criteria](docs/GOALS.md)
- [Architecture](docs/ARCHITECTURE.md)
- [Roadmap](docs/ROADMAP.md)
- [Two-instance operations pilot](docs/roadmap/fleet-pilot.md)
- [Porting scope and invariants](docs/PORTING.md)
- [ADRs](docs/adr/)

## License

MIT. See [LICENSE](LICENSE) and [NOTICE](NOTICE). go-router is an independent implementation; it is not a redistribution of 9router source code.
