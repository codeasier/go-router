# go-router

**Status: M0 — initialization.** Goals are defined in [docs/GOALS.md](docs/GOALS.md). The API is not implemented yet.

go-router is a high-performance, single-binary AI gateway written in Go. It is an independent reimplementation of the 9router connector data plane, focused on the providers and behavior that are actually used in production.

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
- Browser/web-cookie providers and the dashboard are later phases.

## Non-goals (v1)

- Browser automation / web-cookie providers such as `deepseek-web` (phase 2).
- Dashboard UI (phase 3).
- Async provider task APIs, video generation, multi-node exactly-once budgets, and MITM.

## Quick start

```bash
go run ./cmd/go-router -addr :20128
curl http://127.0.0.1:20128/healthz
```

## Documentation

- [Goals and success criteria](docs/GOALS.md)
- [Architecture](docs/ARCHITECTURE.md)
- [Roadmap](docs/ROADMAP.md)
- [Porting scope and invariants](docs/PORTING.md)
- [ADRs](docs/adr/)

## License

MIT. See [LICENSE](LICENSE) and [NOTICE](NOTICE). go-router is an independent implementation; it is not a redistribution of 9router source code.
