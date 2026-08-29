BIN := output/mitia-ops
PKG := ./cmd/server

.PHONY: all build run test fmt vet clean install

all: build

build:
	go build -o $(BIN) $(PKG)

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