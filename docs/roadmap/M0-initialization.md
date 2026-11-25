# M0 — Initialization

Status: **complete**.

## Objective

Create the public repository, define the v1 contract, and prove the Go toolchain with a minimal buildable binary and CI.

## Deliverables

- Public repository `github.com/codeasier/go-router` with MIT license and 9router attribution.
- Goals and non-goals in [GOALS.md](../GOALS.md).
- Architecture in [ARCHITECTURE.md](../ARCHITECTURE.md).
- Roadmap and milestone plans in [ROADMAP.md](../ROADMAP.md) and `docs/roadmap/`.
- Porting scope and invariants in [PORTING.md](../PORTING.md).
- ADRs: [0001-use-go](../adr/0001-use-go.md), [0002-v1-scope](../adr/0002-v1-scope.md).
- Minimal Go module with `cmd/go-router`, `/healthz`, `/v1/models` skeleton, version metadata, and graceful shutdown.
- CI: gofmt, vet, `go test -race`, build.

## Verification

- Local: `gofmt -l .`, `go vet ./...`, `go test -race ./...`, `go build ./...`.
- GitHub Actions: the `ci` workflow passes on `main` (run `45637361400` at initialization).
- Smoke: `go-router -addr 127.0.0.1:20129`, `GET /healthz` and `GET /v1/models` return valid JSON.

## Exit criterion

Met: CI is green and the goals/architecture/roadmap/porting documents are committed.

## Follow-up

Proceed to [M1 — Core HTTP data plane](M1-core-data-plane.md).
