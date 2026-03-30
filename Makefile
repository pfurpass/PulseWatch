AGENT      := pulsewatch-agent
SERVER     := pulsewatch-server
VERSION    := 0.1.0
BUILD_DIR  := dist
LDFLAGS    := -ldflags "-s -w -X main.version=$(VERSION)"

.PHONY: all build test clean lint cross run-agent run-server

## Build beider Binaries für das aktuelle System
build:
	go build $(LDFLAGS) -o $(AGENT)  ./cmd/agent/
	go build $(LDFLAGS) -o $(SERVER) ./cmd/server/
	@echo "Built: $(AGENT)  $(SERVER)"

## Tests ausführen
test:
	go test -v -race ./...

## Tests mit Coverage
coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

## Cross-Compilation für Linux, macOS, Windows
cross:
	mkdir -p $(BUILD_DIR)
	GOOS=linux   GOARCH=amd64  go build $(LDFLAGS) -o $(BUILD_DIR)/$(AGENT)-linux-amd64    ./cmd/agent/
	GOOS=linux   GOARCH=arm64  go build $(LDFLAGS) -o $(BUILD_DIR)/$(AGENT)-linux-arm64    ./cmd/agent/
	GOOS=darwin  GOARCH=arm64  go build $(LDFLAGS) -o $(BUILD_DIR)/$(AGENT)-darwin-arm64   ./cmd/agent/
	GOOS=linux   GOARCH=amd64  go build $(LDFLAGS) -o $(BUILD_DIR)/$(SERVER)-linux-amd64   ./cmd/server/
	GOOS=linux   GOARCH=arm64  go build $(LDFLAGS) -o $(BUILD_DIR)/$(SERVER)-linux-arm64   ./cmd/server/
	GOOS=darwin  GOARCH=arm64  go build $(LDFLAGS) -o $(BUILD_DIR)/$(SERVER)-darwin-arm64  ./cmd/server/
	@echo ""; ls -lh $(BUILD_DIR)/

## Agent lokal starten
run-agent:
	go run ./cmd/agent/ -listen :19998 -log debug

## Server lokal starten (Agent auf localhost erwartet)
run-server:
	go run ./cmd/server/ -listen :19999 -agents http://localhost:19998 -data ./data -log debug

## Alles in einem (Agent + Server im Hintergrund)
run-all:
	@echo "Starting agent on :19998 and server on :19999 ..."
	go run ./cmd/agent/ -listen :19998 &
	sleep 1
	go run ./cmd/server/ -listen :19999 -agents http://localhost:19998 -data ./data
	@echo "Open: http://localhost:19999"

## go vet
lint:
	go vet ./...

## Aufräumen
clean:
	rm -f $(AGENT) $(SERVER) coverage.out coverage.html
	rm -rf $(BUILD_DIR) data

## Hilfe
help:
	@grep -E '^[a-zA-Z_-]+:' Makefile | grep -v '^\.PHONY' | sed 's/:.*//' | xargs -I{} echo "  make {}"
