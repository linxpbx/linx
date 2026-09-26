# Linx — common tasks. Run `make help` for the list.
# Output is kept quiet on purpose: only failures and summaries are printed.

SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X linxpbx.com/linx/internal/version.Version=$(VERSION) -X linxpbx.com/linx/internal/version.Commit=$(COMMIT)
GO_BINS := cmd/linx services/control-plane services/certd services/asterisk-entrypoint services/coturn-entrypoint services/wireguard

.PHONY: help
help: ## Show available commands
	@grep -hE '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*## "}{printf "  %-14s %s\n", $$1, $$2}'

.PHONY: setup-dev
setup-dev: ## Install web dependencies (exact versions from the lockfiles)
	@cd web && npm ci --silent --no-fund --no-audit
	@cd tools/openapi-ts && npm ci --silent --no-fund --no-audit

.PHONY: tokens
tokens: ## Regenerate web CSS + iOS colours from design/tokens.json
	@go run ./tools/tokengen
	@echo "tokens: generated"

.PHONY: api
api: ## Regenerate the Go API server and the web client's API types from api/openapi.yaml
	@go tool oapi-codegen -config api/oapi-codegen-config.yaml api/openapi.yaml
	@cd tools/openapi-ts && npx --no-install openapi-typescript ../../api/openapi.yaml -o ../../web/src/api/schema.d.ts --silent >/dev/null
	@echo "api: generated"

.PHONY: lint
lint: ## Check formatting, vet Go code, verify tokens/API codegen, type-check web
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	@go vet ./...
	@go run ./tools/tokengen -check
	@tmp=$$(mktemp -d) && trap "rm -rf $$tmp" EXIT \
		&& cp services/control-plane/api/gen.go $$tmp/gen.go && cp web/src/api/schema.d.ts $$tmp/schema.d.ts \
		&& $(MAKE) api >/dev/null \
		&& if ! diff -q $$tmp/gen.go services/control-plane/api/gen.go >/dev/null \
			|| ! diff -q $$tmp/schema.d.ts web/src/api/schema.d.ts >/dev/null; then \
			echo "api: out of date, run \`make api\`"; \
			cp $$tmp/gen.go services/control-plane/api/gen.go; cp $$tmp/schema.d.ts web/src/api/schema.d.ts; \
			exit 1; \
		fi
	@cd web && npm run --silent typecheck
	@echo "lint: ok"

.PHONY: test
test: test-go test-web ## Run all tests

.PHONY: test-go
test-go:
	@if out=$$(go test -count=1 ./... 2>&1); then echo "go tests: ok"; \
	else echo "$$out" | grep -Ev '^(ok|\?) '; echo "go tests: FAILED"; exit 1; fi

.PHONY: test-docker
test-docker: ## Run tests that need Docker (internal CA, real Postgres)
	@if out=$$(LINX_DOCKER_TESTS=1 go test -count=1 -run Docker ./internal/... 2>&1); then echo "docker tests: ok"; \
	else echo "$$out" | grep -Ev '^(ok|\?) '; echo "docker tests: FAILED"; exit 1; fi

.PHONY: test-calls
test-calls: ## Phone engine call suite only: Asterisk + SIPp over TLS/SRTP (needs make image SERVICE=asterisk and SERVICE=wireguard)
	@if out=$$(LINX_DOCKER_TESTS=1 go test -count=1 -v -run "TestCallsDocker|TestTrunksDocker|TestWireGuardDocker" ./internal/calltest/ 2>&1); then \
		if echo "$$out" | grep -q -- "--- SKIP"; then echo "$$out" | grep -A2 -- "--- SKIP"; echo "call suite: SKIPPED"; exit 1; fi; \
		echo "call suite: ok"; \
	else echo "$$out" | grep -v '^=== ' | tail -80; echo "call suite: FAILED"; exit 1; fi

.PHONY: test-browser
test-browser: ## Browser call suite: the real stack + two headless Chromiums (needs make image for control-plane, asterisk, coturn)
	@if out=$$(LINX_BROWSER_TESTS=1 go test -count=1 -v -timeout 20m -run TestBrowserCallsDocker ./internal/browsertest/ 2>&1); then \
		if echo "$$out" | grep -q -- "--- SKIP"; then echo "$$out" | grep -A2 -- "--- SKIP"; echo "browser suite: SKIPPED"; exit 1; fi; \
		echo "browser suite: ok"; \
	else echo "$$out" | grep -v '^=== ' | tail -120; echo "browser suite: FAILED"; exit 1; fi

.PHONY: screens
screens: ## Screenshots of every web screen against a stand-in server, into web/e2e/screenshots
	@cd web && npx playwright test >/dev/null && echo "screens: web/e2e/screenshots/"

.PHONY: test-web
test-web:
	@cd web && npm run --silent test

GOVULNCHECK_VERSION := v1.8.0

.PHONY: security
security: ## Known-vulnerability scan (Go + npm) and licence allowlist
	@if out=$$(go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./... 2>&1); then echo "govulncheck: ok"; \
	else echo "$$out" | tail -50; exit 1; fi
	@cd web && npm audit --audit-level=high >/dev/null 2>&1 && echo "npm audit: ok" \
		|| (npm audit --audit-level=high | tail -50; exit 1)
	@go run ./tools/licensecheck
	@go run ./tools/licensecheck -lock tools/openapi-ts/package-lock.json

.PHONY: image
image: ## Build a local image, e.g. make image SERVICE=control-plane (or certd, asterisk, coturn)
	@if [ "$(SERVICE)" = "asterisk" ] || [ "$(SERVICE)" = "coturn" ] || [ "$(SERVICE)" = "control-plane" ]; then \
		docker buildx build -q -f deploy/docker/$(SERVICE).Dockerfile \
			--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --load -t linx-$(SERVICE):dev . >/dev/null; \
	else \
		docker buildx build -q -f deploy/docker/go-service.Dockerfile --build-arg SERVICE=$(SERVICE) \
			--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --load -t linx-$(SERVICE):dev . >/dev/null; \
	fi
	@echo "image: linx-$(SERVICE):dev"

.PHONY: build
build: ## Build Go binaries into bin/ and the web app into web/dist/
	@mkdir -p bin
	@for d in $(GO_BINS); do CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$$(basename $$d) ./$$d; done
	@cd web && npm run --silent build >/dev/null
	@echo "build: bin/ and web/dist/ ready ($(VERSION))"

.PHONY: clean
clean: ## Remove build output
	@rm -rf bin web/dist
