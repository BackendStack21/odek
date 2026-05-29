# odek — the fastest, minimalistic autonomous ReAct agent CLI in Go.
# https://github.com/BackendStack21/odek

GO      := go
GOLINT  := $(shell command -v golangci-lint 2>/dev/null)
GIT_TAG := $(shell git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0-dev")
LDFLAGS := -s -w -X main.version=$(GIT_TAG)
COVER   := coverage.out
BINARY  := odek

.PHONY: all
all: test build

# ── Build ──────────────────────────────────────────────────────────────

.PHONY: build
build: ## Build the odek binary
	$(GO) build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/$(BINARY)

.PHONY: install
install: ## Install odek to $GOPATH/bin
	$(GO) install -ldflags "$(LDFLAGS)" ./cmd/$(BINARY)

.PHONY: build-all
build-all: ## Cross-compile for linux, darwin (amd64 + arm64)
	GOOS=linux   GOARCH=amd64 $(GO) build -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-linux-amd64   ./cmd/$(BINARY)
	GOOS=linux   GOARCH=arm64 $(GO) build -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-linux-arm64   ./cmd/$(BINARY)
	GOOS=darwin  GOARCH=amd64 $(GO) build -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-darwin-amd64  ./cmd/$(BINARY)
	GOOS=darwin  GOARCH=arm64 $(GO) build -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-darwin-arm64  ./cmd/$(BINARY)

# ── Test (no LLM) ──────────────────────────────────────────────────────

.PHONY: test
test: ## Run unit tests (fast, no LLM API calls; excludes cmd/odek)
	$(GO) list ./... | grep -v cmd/odek | xargs $(GO) test -short -count=1

.PHONY: test-internal
test-internal: ## Run only internal package tests (excludes cmd/odek)
	$(GO) list ./... | grep -v cmd/odek | xargs $(GO) test -short -count=1

.PHONY: test-cmd
test-cmd: ## Run cmd/odek unit tests (excludes TTY approval and E2E tests)
	$(GO) test -short -count=1 -timeout 300s ./cmd/odek -skip "TestBrowser|TestModel_E2E|TestREPL_E2E|TestSandbox_E2E|TestSecurity_E2E|TestMCP_E2E|TestSubagent_E2E|TestRefs_E2E"

.PHONY: test-race
test-race: ## Run unit tests with race detector (excludes cmd/odek)
	$(GO) list ./... | grep -v cmd/odek | xargs $(GO) test -short -race -count=1

.PHONY: test-verbose
test-verbose: ## Run unit tests with full output (excludes cmd/odek)
	$(GO) list ./... | grep -v cmd/odek | xargs $(GO) test -short -v -count=1

# ── Coverage ───────────────────────────────────────────────────────────

.PHONY: coverage
coverage: ## Generate HTML coverage report (unit tests; excludes cmd/odek)
	$(GO) list ./... | grep -v cmd/odek | xargs $(GO) test -short -coverprofile=$(COVER) -covermode=atomic
	$(GO) tool cover -html=$(COVER) -o coverage.html
	@echo "→ coverage.html"

.PHONY: coverage-func
coverage-func: ## Print per-function coverage summary (excludes cmd/odek)
	$(GO) list ./... | grep -v cmd/odek | xargs $(GO) test -short -coverprofile=$(COVER) -covermode=atomic
	$(GO) tool cover -func=$(COVER)

.PHONY: coverage-total
coverage-total: ## Print total coverage percentage (excludes cmd/odek)
	$(GO) list ./... | grep -v cmd/odek | xargs $(GO) test -short -coverprofile=$(COVER) -covermode=atomic
	@$(GO) tool cover -func=$(COVER) | tail -1 | awk '{print "total " $$3}'

# ── Test (with LLM) ────────────────────────────────────────────────────

.PHONY: test-integration
test-integration: ## Run integration tests (requires ODEK_API_KEY or DEEPSEEK_API_KEY)
	@test -n "$$ODEK_API_KEY$$DEEPSEEK_API_KEY" || { \
		echo "ERROR: ODEK_API_KEY or DEEPSEEK_API_KEY not set"; \
		echo "  export ODEK_API_KEY=sk-..."; \
		exit 1; \
	}
	$(GO) test -tags=integration -count=1 -timeout 300s -run Integration ./...

.PHONY: test-all
test-all: test test-integration ## Run all tests (unit + integration)

# ── Lint & Format ──────────────────────────────────────────────────────

.PHONY: fmt
fmt: ## Format all Go source files
	$(GO) fmt ./...

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: lint
lint: ## Run golangci-lint (if installed)
ifdef GOLINT
	$(GOLINT) run ./...
else
	@echo "golangci-lint not installed — run: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"
	@exit 1
endif

.PHONY: check
check: fmt vet test ## Format, vet, and unit test (CI fast gate)

# ── Docker / Sandbox ───────────────────────────────────────────────────

.PHONY: docker-test
docker-test: build ## Run tests inside Docker sandbox (mimics --sandbox)
	docker run --rm -v "$(PWD):/workspace:ro" -w /workspace golang:1.24-alpine sh -c \
		'go test -short -count=1 ./...'

# ── Clean ──────────────────────────────────────────────────────────────

.PHONY: clean
clean: ## Remove build artifacts and coverage files
	rm -rf bin/ $(COVER) coverage.html

# ── Help ───────────────────────────────────────────────────────────────

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*## "}; {printf "\033[36m%-22s\033[0m %s\n", $$1, $$2}'
