# bot-bridge Makefile (DEV_SPEC §4, §20).
# Server and CLI build as single static binaries (CGO_ENABLED=0).

GO            ?= go
GOFLAGS       ?=
CGO_ENABLED   ?= 0
BIN_DIR       ?= bin
export CGO_ENABLED

# Reproducible-ish version stamp from git (falls back when not a checkout).
VERSION       ?= $(shell git describe --tags --always --dirty 2>/dev/null || cat VERSION 2>/dev/null || echo dev)
LDFLAGS       := -s -w
# The CLI stamps its version into main.version via -ldflags -X (DEV_SPEC §12.3).
CLI_LDFLAGS   := $(LDFLAGS) -X main.version=$(VERSION)
# The server stamps the same way (DEV_SPEC §12.4) so operators can confirm builds.
SRV_LDFLAGS   := $(LDFLAGS) -X main.version=$(VERSION)

# Release artifacts (reproducible, stripped, static) land in DIST_DIR with a
# matching .sha256 (DEV_SPEC §12.4). Only linux/amd64 is published for the trial.
DIST_DIR      ?= dist

.PHONY: all build server cli test test-short vet fmt fmt-check lint tidy clean help \
	release-cli release-server release

all: build ## Build all binaries (default)

build: server cli ## Build server and CLI binaries

server: ## Build bot-bridge-server
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/bot-bridge-server ./cmd/bot-bridge-server

cli: ## Build bot-bridge-cli
	$(GO) build $(GOFLAGS) -ldflags "$(CLI_LDFLAGS)" -o $(BIN_DIR)/bot-bridge-cli ./cmd/bot-bridge-cli

release-cli: ## Build the stripped, static linux/amd64 CLI release + .sha256 into dist/
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -trimpath \
		-ldflags "$(CLI_LDFLAGS)" \
		-o $(DIST_DIR)/bot-bridge-cli-linux-amd64 ./cmd/bot-bridge-cli
	cd $(DIST_DIR) && sha256sum bot-bridge-cli-linux-amd64 > bot-bridge-cli-linux-amd64.sha256

release-server: ## Build the stripped, static linux/amd64 server release + .sha256 into dist/
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -trimpath \
		-ldflags "$(SRV_LDFLAGS)" \
		-o $(DIST_DIR)/bot-bridge-server-linux-amd64 ./cmd/bot-bridge-server
	cd $(DIST_DIR) && sha256sum bot-bridge-server-linux-amd64 > bot-bridge-server-linux-amd64.sha256

release: release-cli release-server ## Build both linux/amd64 release binaries + checksums

test: ## Run all tests with the race detector (CLAUDE.md rule 4)
	$(GO) test ./... -race

test-short: ## Run tests without the race detector
	$(GO) test ./...

vet: ## Run go vet
	$(GO) vet ./...

fmt: ## Format all Go files
	gofmt -w $(shell find . -name '*.go' -not -path './reference/*')

fmt-check: ## Fail if any Go file is not gofmt-clean
	@out="$$(gofmt -l $$(find . -name '*.go' -not -path './reference/*'))"; \
	if [ -n "$$out" ]; then echo "gofmt needed on:"; echo "$$out"; exit 1; fi

# lint runs the checks that gate every task: formatting, vet, and tests.
# govulncheck / staticcheck are wired into CI and land in milestone M7.
lint: fmt-check vet ## Run fmt-check and vet

tidy: ## Tidy go.mod / go.sum
	$(GO) mod tidy

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)

help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'
