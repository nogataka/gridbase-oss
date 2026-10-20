ROOT := $(abspath .)
GOCACHE ?= $(ROOT)/.gocache
GOMODCACHE ?= $(ROOT)/.gomodcache
BIN_DIR := $(ROOT)/bin

VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X github.com/nogataka/gridbase-oss/tunnel/pkg/buildinfo.Version=$(VERSION) \
	-X github.com/nogataka/gridbase-oss/tunnel/pkg/buildinfo.CommitSHA=$(COMMIT) \
	-X github.com/nogataka/gridbase-oss/tunnel/pkg/buildinfo.BuildTime=$(BUILD_TIME)

export GOCACHE
export GOMODCACHE

.PHONY: fmt vet test build build-server build-client build-version clean

fmt:
	gofmt -w ./client ./pkg ./protocol ./server ./cmd

vet:
	go vet ./...

test:
	go test ./...

build: fmt vet test build-server build-client build-version

build-server:
	mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/server ./server

build-client:
	mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/client ./client

build-version:
	mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/version ./cmd/version

clean:
	rm -rf $(BIN_DIR)
