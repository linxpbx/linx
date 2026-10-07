# Linx — common tasks. Run `make help` for the list.
# Output is kept quiet on purpose: only failures and summaries are printed.

SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X linxpbx.com/linx/internal/version.Version=$(VERSION) -X linxpbx.com/linx/internal/version.Commit=$(COMMIT)
GO_BINS := cmd/linx cmd/linx-firewall-sync cmd/linx-backup-agent cmd/linx-ops-agent services/control-plane services/certd services/asterisk-entrypoint services/coturn-entrypoint services/wireguard

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

.PHONY: test-install
test-install: ## Web install suite: install page + first certificate with Pebble, through port 443 and by DNS-01 on port 8443 (needs make image SERVICE=control-plane and SERVICE=certd)
	@if out=$$(LINX_DOCKER_TESTS=1 go test -p 1 -count=1 -v -timeout 20m -run "TestInstallModeDocker|TestWebCertificateInstall" ./internal/install/ ./internal/installer/ 2>&1); then \
		if echo "$$out" | grep -q -- "--- SKIP"; then echo "$$out" | grep -A2 -- "--- SKIP"; echo "install suite: SKIPPED"; exit 1; fi; \
		echo "install suite: ok"; \
	else echo "$$out" | grep -Ev '^(ok|\?|=== RUN) ' | tail -80; echo "install suite: FAILED"; exit 1; fi

.PHONY: test-calls
test-calls: ## Phone engine call suite only: Asterisk + SIPp over TLS/SRTP (needs make image SERVICE=asterisk and SERVICE=wireguard)
	@if out=$$(LINX_DOCKER_TESTS=1 go test -count=1 -v -run "TestCallsDocker|TestTrunksDocker|TestWireGuardDocker" ./internal/calltest/ 2>&1); then \
		if echo "$$out" | grep -q -- "--- SKIP"; then echo "$$out" | grep -A2 -- "--- SKIP"; echo "call suite: SKIPPED"; exit 1; fi; \
		echo "call suite: ok"; \
	else echo "$$out" | grep -v '^=== ' | tail -80; \
		echo "--- the test's own lines (service logs left out):"; echo "$$out" | grep -v '^=== ' | grep -Ev '^ {8}' | tail -40; \
		echo "call suite: FAILED"; exit 1; fi

.PHONY: test-browser
test-browser: ## Browser call suite: the real stack + two headless Chromiums (needs make image for control-plane, asterisk, coturn)
	@if out=$$(LINX_BROWSER_TESTS=1 go test -count=1 -v -timeout 20m -run TestBrowserCallsDocker ./internal/browsertest/ 2>&1); then \
		if echo "$$out" | grep -q -- "--- SKIP"; then echo "$$out" | grep -A2 -- "--- SKIP"; echo "browser suite: SKIPPED"; exit 1; fi; \
		echo "browser suite: ok"; \
	else echo "$$out" | grep -v '^=== ' | tail -120; \
		echo "--- the test's own lines (service logs left out):"; echo "$$out" | grep -v '^=== ' | grep -Ev '^ {8}([^|]|$$)' | tail -100; \
		echo "browser suite: FAILED"; exit 1; fi

.PHONY: prompts
prompts: ## Make the call messages from tools/prompts/prompts.tsv (kristin voice) into deploy/docker/asterisk/prompts
	@docker build -q -t linx-prompts-tool tools/prompts >/dev/null
	@docker run --rm --network none -v "$(CURDIR)/tools/prompts:/in:ro" -v "$(CURDIR)/deploy/docker/asterisk/prompts:/out" linx-prompts-tool

.PHONY: screens
screens: ## Screenshots of every web screen against a stand-in server, into web/e2e/screenshots
	@cd web && npx playwright test >/dev/null && echo "screens: web/e2e/screenshots/"

.PHONY: test-web
test-web:
	@cd web && npm run --silent test

# The iPhone/iPad app (docs/PHASE2.md). These need Xcode; they are skipped on Linux.
IOS_PROJECT := ios/Linx.xcodeproj
IOS_DERIVED := ios/build/dd
IOS_SIM_DEVICE ?= iPhone 17

.PHONY: ios-lint
ios-lint: ## Check the app's Swift formatting (swift-format, settings in ios/.swift-format)
	@xcrun swift-format lint --recursive --strict ios/Linx ios/LinxTests && echo "ios lint: ok"

.PHONY: ios-deps
ios-deps: ## Fetch Google's WebRTC for the app (pinned version, checked against its SHA-256)
	@ios/tools/webrtc.sh

.PHONY: ios-build
ios-build: ios-deps ## Build the iPhone/iPad app for the simulator (no signing, no Apple account)
	@if out=$$(xcodebuild build -project $(IOS_PROJECT) -scheme Linx -configuration Debug \
		-destination 'generic/platform=iOS Simulator' -derivedDataPath $(IOS_DERIVED) \
		CODE_SIGNING_ALLOWED=NO 2>&1); then echo "ios build: ok"; \
	else echo "$$out" | grep -E "error:|warning: .*deprecat" | sort -u | head -40; echo "ios build: FAILED"; exit 1; fi

.PHONY: ios-build-device
ios-build-device: ios-deps ## Build the app for a real iPhone, Release, no signing (what an archive compiles)
	@if out=$$(xcodebuild build -project $(IOS_PROJECT) -scheme Linx -configuration Release \
		-destination 'generic/platform=iOS' -derivedDataPath ios/build/rel \
		CODE_SIGNING_ALLOWED=NO 2>&1); then echo "ios device build: ok"; \
	else echo "$$out" | grep -E "error:" | sort -u | head -40; echo "ios device build: FAILED"; exit 1; fi

.PHONY: ios-archive
ios-archive: ios-deps ## Archive and export the app for TestFlight (needs the owner's Apple ID in Xcode; docs/ops/APPLE_SIGNING.md)
	@defaults read com.apple.dt.Xcode DVTDeveloperAccountManagerAppleIDLists >/dev/null 2>&1 \
		|| { echo "ios archive: no Apple ID in Xcode on this Mac."; \
		     echo "  Xcode → Settings → Accounts → + and sign in (once), then run this again."; \
		     echo "  docs/ops/APPLE_SIGNING.md has the rest."; exit 1; }
	@# The certificate and the profile don't have to exist yet:
	@# -allowProvisioningUpdates lets Xcode make them from the signed-in
	@# account the first time, which is what happens on a fresh Mac.
	@rm -rf ios/build/archive && mkdir -p ios/build/archive
	@if out=$$(xcodebuild archive -project $(IOS_PROJECT) -scheme Linx -configuration Release \
		-destination 'generic/platform=iOS' -archivePath ios/build/archive/Linx.xcarchive \
		-allowProvisioningUpdates 2>&1); then echo "ios archive: built"; \
	else echo "$$out" | grep -E "error:" | sort -u | head -20; echo "ios archive: FAILED"; exit 1; fi
	@if out=$$(xcodebuild -exportArchive -archivePath ios/build/archive/Linx.xcarchive \
		-exportOptionsPlist ios/ExportOptions.plist -exportPath ios/build/archive/export \
		-allowProvisioningUpdates 2>&1); then \
		echo "ios archive: exported $$(ls ios/build/archive/export/*.ipa)"; \
	else echo "$$out" | grep -E "error:" | sort -u | head -20; echo "ios export: FAILED"; exit 1; fi
	@# A build with no aps-environment cannot register for push, so the phone
	@# would never ring — which is the one thing this build is for. An
	@# archive built without signing exports looking perfectly fine and is
	@# exactly this (docs/ops/APPLE_SIGNING.md).
	@rm -rf ios/build/archive/check && mkdir -p ios/build/archive/check \
		&& unzip -q ios/build/archive/export/Linx.ipa -d ios/build/archive/check \
		&& codesign -d --entitlements :- ios/build/archive/check/Payload/Linx.app 2>/dev/null \
			| grep -q "aps-environment" \
		|| { echo "ios archive: the exported build has no aps-environment — push would be dead."; \
		     echo "  See \"A trap worth knowing\" in docs/ops/APPLE_SIGNING.md."; exit 1; }
	@echo "ios archive: push entitlement present"

.PHONY: ios-upload
ios-upload: ## Upload the exported build to TestFlight (needs LINX_ASC_KEY_ID and LINX_ASC_ISSUER; the owner says when)
	@[ -n "$(LINX_ASC_KEY_ID)" ] && [ -n "$(LINX_ASC_ISSUER)" ] \
		|| { echo "ios upload: set LINX_ASC_KEY_ID and LINX_ASC_ISSUER (App Store Connect API key)."; \
		     echo "  docs/ops/APPLE_SIGNING.md, Part 1 step 5. The .p8 goes in ~/.appstoreconnect/private_keys/."; exit 1; }
	@xcrun altool --upload-app -f $$(ls ios/build/archive/export/*.ipa) -t ios \
		--apiKey $(LINX_ASC_KEY_ID) --apiIssuer $(LINX_ASC_ISSUER) && echo "ios upload: sent to App Store Connect"

.PHONY: ios-test
ios-test: ios-deps ## Run the app's unit tests on a simulator
	@udid=$$(ios/tools/sim.sh "$(IOS_SIM_DEVICE)") \
		&& if out=$$(xcodebuild test -project $(IOS_PROJECT) -scheme Linx -configuration Debug \
			-destination "id=$$udid" -derivedDataPath $(IOS_DERIVED) CODE_SIGNING_ALLOWED=NO 2>&1); then \
			echo "ios tests: ok"; \
		else echo "$$out" | grep -E "error:|failed|Failing tests" | tail -30; echo "ios tests: FAILED"; exit 1; fi

.PHONY: ios-screens
ios-screens: ios-deps ## Screenshots of every app screen, light and dark, into ios/screenshots
	@ios/tools/screens.sh

.PHONY: ios-screens-all
ios-screens-all: ios-deps ## The same screenshots on all three test devices (iPhone, iPad, iPhone Duo folded)
	@ios/tools/screens-all.sh

GOVULNCHECK_VERSION := v1.8.0

.PHONY: security
security: ## Known-vulnerability scan (Go + npm) and licence allowlist
	@if out=$$(go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./... 2>&1); then echo "govulncheck: ok"; \
	else echo "$$out" | tail -50; exit 1; fi
	@cd web && npm audit --audit-level=high >/dev/null 2>&1 && echo "npm audit: ok" \
		|| (npm audit --audit-level=high | tail -50; exit 1)
	@cd site && npm audit --audit-level=high >/dev/null 2>&1 && echo "npm audit (site): ok" \
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
