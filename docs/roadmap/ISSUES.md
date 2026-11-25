# Issue Map

Status: generated from the GitHub milestones and issues on 2026-11-25.

GitHub issues are the source of truth for status. This table maps each issue to its milestone and labels so the plan can be read offline.

| Issue | Milestone | Title | Labels |
|---|---|---|---|
| [#1](https://github.com/codeasier/go-router/issues/1) | M0 — Initialization | M0: initialize repository, goals, architecture, ADRs, and CI | `kind:task`, `area:core`, `priority:p0` |
| [#2](https://github.com/codeasier/go-router/issues/2) | M1 — Core HTTP data plane | M1.1: config, auth, and HTTP server skeleton | `kind:feature`, `area:core`, `priority:p0` |
| [#3](https://github.com/codeasier/go-router/issues/3) | M1 — Core HTTP data plane | M1.2: provider registry and model catalog | `kind:feature`, `area:core`, `priority:p0` |
| [#4](https://github.com/codeasier/go-router/issues/4) | M1 — Core HTTP data plane | M1.3: dispatcher with execution facts and retry classes | `kind:feature`, `area:core`, `priority:p0` |
| [#5](https://github.com/codeasier/go-router/issues/5) | M1 — Core HTTP data plane | M1.4: OpenAI-compatible chat completions and models endpoints | `kind:feature`, `area:core`, `priority:p0` |
| [#6](https://github.com/codeasier/go-router/issues/6) | M1 — Core HTTP data plane | M1.5: first providers — volceapi, openrouter, opencode-go, custom OpenAI-compatible | `kind:feature`, `area:provider`, `priority:p0` |
| [#7](https://github.com/codeasier/go-router/issues/7) | M1 — Core HTTP data plane | M1.6: golden tests, upstream simulator, benchmark harness, 9router baseline | `kind:test`, `kind:benchmark`, `area:core`, `priority:p1` |
| [#8](https://github.com/codeasier/go-router/issues/8) | M2 — OAuth providers and routing | M2.1: OAuth connection model and token refresh framework | `kind:feature`, `area:oauth`, `priority:p0` |
| [#9](https://github.com/codeasier/go-router/issues/9) | M2 — OAuth providers and routing | M2.2: codex and codex-backup providers | `kind:feature`, `area:provider`, `area:oauth`, `priority:p0` |
| [#10](https://github.com/codeasier/go-router/issues/10) | M2 — OAuth providers and routing | M2.3: antigravity provider | `kind:feature`, `area:provider`, `area:oauth`, `priority:p1` |
| [#11](https://github.com/codeasier/go-router/issues/11) | M2 — OAuth providers and routing | M2.4: cursor, xai, and xiaomi-tokenplan providers | `kind:feature`, `area:provider`, `area:oauth`, `priority:p1` |
| [#12](https://github.com/codeasier/go-router/issues/12) | M2 — OAuth providers and routing | M2.5: grok-cli, minimax-code, trae, and trae-enterprise providers | `kind:feature`, `area:provider`, `area:oauth`, `priority:p1` |
| [#13](https://github.com/codeasier/go-router/issues/13) | M2 — OAuth providers and routing | M2.6: routing, combos, fallback, and protocol adapters | `kind:feature`, `area:routing`, `priority:p0` |
| [#14](https://github.com/codeasier/go-router/issues/14) | M3 — Persistence, admin API, and operations | M3.1: SQLite driver ADR, schema, and migrations | `kind:adr`, `area:storage`, `priority:p0` |
| [#15](https://github.com/codeasier/go-router/issues/15) | M3 — Persistence, admin API, and operations | M3.2: repositories and read-only 9router import | `kind:feature`, `area:storage`, `priority:p0` |
| [#16](https://github.com/codeasier/go-router/issues/16) | M3 — Persistence, admin API, and operations | M3.3: minimal admin API | `kind:feature`, `area:admin`, `priority:p1` |
| [#17](https://github.com/codeasier/go-router/issues/17) | M3 — Persistence, admin API, and operations | M3.4: observability and operations | `kind:feature`, `area:observability`, `priority:p1` |
| [#18](https://github.com/codeasier/go-router/issues/18) | M3 — Persistence, admin API, and operations | M3.5: packaging and security review | `kind:task`, `area:security`, `area:release`, `priority:p1` |
| [#19](https://github.com/codeasier/go-router/issues/19) | M4 — Phase 2 data plane | M4.1: browser automation ADR and isolation | `kind:adr`, `area:browser`, `priority:p1` |
| [#20](https://github.com/codeasier/go-router/issues/20) | M4 — Phase 2 data plane | M4.2: deepseek-web provider | `kind:feature`, `area:browser`, `area:provider`, `priority:p1` |
| [#21](https://github.com/codeasier/go-router/issues/21) | M4 — Phase 2 data plane | M4.3: custom embeddings and image/media endpoints | `kind:feature`, `area:provider`, `priority:p2` |
| [#22](https://github.com/codeasier/go-router/issues/22) | M4 — Phase 2 data plane | M4.4: browser/download fault injection and parity | `kind:test`, `area:browser`, `priority:p2` |
| [#23](https://github.com/codeasier/go-router/issues/23) | M5 — Dashboard, parity, and release | M5.1: dashboard ADR and MVP | `kind:adr`, `kind:feature`, `area:admin`, `priority:p2` |
| [#24](https://github.com/codeasier/go-router/issues/24) | M5 — Dashboard, parity, and release | M5.2: full parity suite against 9router | `kind:test`, `area:core`, `priority:p0` |
| [#25](https://github.com/codeasier/go-router/issues/25) | M5 — Dashboard, parity, and release | M5.3: 7-day soak and load test | `kind:test`, `kind:benchmark`, `area:core`, `priority:p0` |
| [#26](https://github.com/codeasier/go-router/issues/26) | M5 — Dashboard, parity, and release | M5.4: cutover/rollback runbook and release | `kind:docs`, `area:release`, `priority:p0` |
