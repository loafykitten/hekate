VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILT   ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
PKG      = github.com/loafykitten/hekate/service/internal/buildinfo
LDFLAGS  = -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT) -X $(PKG).BuiltAt=$(BUILT)

.PHONY: build test lint run

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/hekate ./cmd/hekate

test:
	go test -race ./...

lint:
	golangci-lint run ./...

run:
	HEKATE_DATA_DIR=./data HEKATE_WEB_DIR=../app/dist HEKATE_ASSETS_DIR=../assets/renamon-nukorio \
		go run -ldflags "$(LDFLAGS)" ./cmd/hekate
