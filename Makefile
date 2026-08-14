GO      ?= go
BINARY  := proxy
BIN_DIR := bin

.PHONY: build run test vet lint fmt tidy clean

build:
	$(GO) build -o $(BIN_DIR)/$(BINARY) ./cmd/proxy

run:
	$(GO) run ./cmd/proxy

test:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

lint:
	golangci-lint run

fmt:
	gofmt -s -w .
	$(GO) mod tidy

tidy:
	$(GO) mod tidy

clean:
	rm -rf $(BIN_DIR) .sessions
