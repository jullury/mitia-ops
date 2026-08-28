BIN := output/mitia-ops
PKG := ./cmd/server

.PHONY: all build run test fmt vet clean

all: build

build:
	go build -o $(BIN) $(PKG)

run: build
	./$(BIN)

test:
	go test ./...

fmt:
	go fmt ./...

vet:
	go vet ./...

clean:
	rm -f $(BIN) $(BIN).exe