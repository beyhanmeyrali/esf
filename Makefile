# Factory development entry points.
#
# The upstream Machinist project uses `just` (see Justfile); this Makefile adds
# the factory-specific workflow on top and does not replace it.
#
# Everything here is runnable from a clean checkout on this host.

SHELL := /bin/bash
.DEFAULT_GOAL := help

# Go may be managed by mise; if `go` is not on PATH, fall back to the installed
# mise Go so `make` works from a bare shell.
GO ?= $(shell command -v go 2>/dev/null || echo $(HOME)/.local/share/mise/installs/go/1.25.5/bin/go)

BIN_DIR    := bin
FACTORY    := $(BIN_DIR)/factory
TEMPORAL_DIR := deployments/dev/temporal
COMPOSE    := docker compose --project-directory $(TEMPORAL_DIR)

# Sandbox connection settings, matching the discovered deployment.
export CUBE_API_URL        ?= http://127.0.0.1:4000
export CUBE_TEMPLATE_ID    ?=
export CUBE_PROXY_NODE_IP  ?=
export CUBE_PROXY_PORT_HTTP ?= 80

# Path to the pinned standalone opencode2 binary.
OPENCODE_BINARY ?= $(HOME)/.npm-global/lib/node_modules/@opencode/cli/bin/opencode.exe

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

# ── Build and test ──────────────────────────────────────────────────────────

.PHONY: build
build: ## Build the factory and discovery binaries into ./bin
	@mkdir -p $(BIN_DIR)
	$(GO) build -o $(FACTORY) ./cmd/factory
	$(GO) build -o $(BIN_DIR)/cube-smoke ./tools/cube-smoke
	$(GO) build -o $(BIN_DIR)/cube-netprobe ./tools/cube-netprobe
	$(GO) build -o $(BIN_DIR)/cube-agent-spike ./tools/cube-agent-spike
	@echo "built: $(FACTORY) $(BIN_DIR)/cube-smoke $(BIN_DIR)/cube-netprobe $(BIN_DIR)/cube-agent-spike"

.PHONY: test
test: ## Run unit tests (no CubeSandbox or Temporal required)
	$(GO) test ./... -count=1

.PHONY: test-unit
test-unit: ## Run unit tests only (fast)
	$(GO) test ./internal/... ./cmd/... -count=1

.PHONY: test-integration
test-integration: ## Run tests that need the live CubeSandbox (tagged integration)
	$(GO) test -tags=integration ./... -count=1 -v

.PHONY: lint
lint: ## gofmt check, go vet and shellcheck
	@out=$$($(GO) fmt ./...); if [ -n "$$out" ]; then echo "gofmt rewrote:"; echo "$$out"; fi
	$(GO) vet ./...
	@if command -v shellcheck >/dev/null 2>&1; then \
		shellcheck deployments/dev/temporal/scripts/*.sh scripts/*.sh 2>/dev/null || true; \
	else echo "shellcheck not installed; skipping"; fi

.PHONY: fmt
fmt: ## Format all Go code
	$(GO) fmt ./...

# ── Temporal ────────────────────────────────────────────────────────────────

.PHONY: temporal-up
temporal-up: ## Start the Temporal development stack and wait for health
	$(COMPOSE) up -d --wait
	@echo "Temporal gRPC: 127.0.0.1:7233"
	@echo "Temporal UI:   http://127.0.0.1:8233"

.PHONY: temporal-down
temporal-down: ## Stop the Temporal development stack (data volume preserved)
	$(COMPOSE) down

.PHONY: temporal-clean
temporal-clean: ## Stop Temporal AND delete its data volume
	$(COMPOSE) down -v

.PHONY: temporal-status
temporal-status: ## Show Temporal container and namespace status
	@$(COMPOSE) ps
	@echo
	@docker exec factory-temporal-create-namespace \
		temporal operator namespace list --address temporal:7233 2>/dev/null \
		|| echo "(namespace listing unavailable)"

.PHONY: temporal-logs
temporal-logs: ## Tail Temporal server logs
	$(COMPOSE) logs -f temporal

.PHONY: temporal-hello
temporal-hello: ## Prove a workflow can execute end to end
	$(GO) test ./internal/factory -run TestTemporalHelloWorkflow -count=1 -v

# ── CubeSandbox ─────────────────────────────────────────────────────────────

.PHONY: cube-smoke
cube-smoke: ## Prove the factory host can use the existing CubeSandbox
	$(GO) run ./tools/cube-smoke

.PHONY: cube-netprobe
cube-netprobe: ## Discover the effective egress policy of this deployment
	$(GO) run ./tools/cube-netprobe

.PHONY: cube-agent-spike
cube-agent-spike: ## Prove the coding agent runs inside a Cube microVM
	$(GO) run ./tools/cube-agent-spike -e2e -opencode-binary $(OPENCODE_BINARY)

.PHONY: cube-sandboxes
cube-sandboxes: build ## List live CubeSandboxes (leak check)
	$(FACTORY) sandboxes

# ── Factory ─────────────────────────────────────────────────────────────────

.PHONY: factory-doctor
factory-doctor: build ## Check configuration, Cube and Temporal
	$(FACTORY) doctor

.PHONY: factory-worker
factory-worker: build ## Run the Temporal worker in the foreground
	$(FACTORY) worker

.PHONY: e2e-repo
e2e-repo: ## Materialise the disposable E2E fixture repository
	./scripts/make-e2e-repo.sh

.PHONY: factory-run-demo
factory-run-demo: build e2e-repo ## Run the acceptance test: one task in, one verified patch out
	./scripts/factory-run-demo.sh

.PHONY: integration-test
integration-test: cube-smoke temporal-up ## Run the full integration suite
	$(GO) test -tags=integration ./internal/... -count=1 -v

.PHONY: verify
verify: lint test cube-smoke ## Run everything that does not need Temporal

.PHONY: clean
clean: ## Remove build output and local factory state
	rm -rf $(BIN_DIR) .factory
