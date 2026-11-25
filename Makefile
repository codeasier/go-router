.PHONY: build test vet fmt run clean

VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X github.com/codeasier/go-router/internal/version.Version=$(VERSION) \
	-X github.com/codeasier/go-router/internal/version.Commit=$(COMMIT) \
	-X github.com/codeasier/go-router/internal/version.Date=$(DATE)

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/go-router ./cmd/go-router

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

run:
	go run ./cmd/go-router

clean:
	rm -rf bin
