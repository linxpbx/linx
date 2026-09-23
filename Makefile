# Linx — common tasks. Run `make help` for the list.
# Output is kept quiet on purpose: only failures and summaries are printed.

SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X linxpbx.com/linx/internal/version.Version=$(VERSION) -X linxpbx.com/linx/internal/version.Commit=$(COMMIT)
GO_BINS := cmd/linx services/control-plane services/certd

.PHONY: help
help: ## Show available commands
	@grep -hE '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*## "}{printf "  %-14s %s\n", $$1, $$2}'

.PHONY: setup-dev
setup-dev: ## Install web dependencies (exact versions from the lockfile)
	@cd web && npm ci --silent --no-fund --no-audit

.PHONY: tokens
tokens: ## Regenerate web CSS + iOS colours from design/tokens.json
	@go run ./tools/tokengen
	@echo "tokens: generated"

.PHONY: lint
lint: ## Check formatting, vet Go code, verify tokens, type-check web
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	@go vet ./...
	@go run ./tools/tokengen -check
	@cd web && npm run --silent typecheck
	@echo "lint: ok"

.PHONY: test
test: test-go test-web ## Run all tests

.PHONY: test-go
test-go:
	@if out=$$(go test -count=1 ./... 2>&1); then echo "go tests: ok"; \
	else echo "$$out" | grep -Ev '^(ok|\?) '; echo "go tests: FAILED"; exit 1; fi

.PHONY: test-web
test-web:
	@cd web && npm run --silent test

.PHONY: build
build: ## Build Go binaries into bin/ and the web app into web/dist/
	@mkdir -p bin
	@for d in $(GO_BINS); do CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$$(basename $$d) ./$$d; done
	@cd web && npm run --silent build >/dev/null
	@echo "build: bin/ and web/dist/ ready ($(VERSION))"

.PHONY: clean
clean: ## Remove build output
	@rm -rf bin web/dist
