BIN := output/mitia-ops
PKG := ./cmd/server
# Version stamped into the binary (`--version`). Prefer the nearest git tag
# (e.g. v1.3.0), else a describe-like string, else "dev".
VERSION := $(shell git describe --tags --exact-match 2>/dev/null || git describe --tags 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: all build run test fmt vet clean install

all: build

build:
	go build -ldflags "$(LDFLAGS) -s -w" -o $(BIN) $(PKG)

run: build
	./$(BIN)

# Install mitia-ops as a systemd service that starts at boot (run as root:
# 'make build && sudo make install').
install:
	./scripts/install.sh

test:
	go test ./...

fmt:
	go fmt ./...

vet:
	go vet ./...

clean:
	rm -f $(BIN) $(BIN).exe