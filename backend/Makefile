GO        ?= go
PYTHON    ?= python3
PKG       := ./...
BIN       := bin
IMAGE     ?= zoikotax/ztax-core
TRAIN_APP ?= 0.0.0-dev

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "};{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: tidy
tidy: ## Resolve and pin the module graph
	$(GO) mod tidy

.PHONY: build
build: ## Build all commands
	$(GO) build -o $(BIN)/ ./cmd/...

.PHONY: run
run: ## Run ztax-core with a local development environment
	env $$(cat .env.local | xargs) $(GO) run ./cmd/ztax-core

.PHONY: test
test: ## Tier 1 — unit and property tests (ADR-0018)
	$(GO) test -race -count=1 $(PKG)

.PHONY: test-integration
test-integration: ## Tier 3 — against real PostgreSQL/PostGIS via testcontainers
	$(GO) test -race -count=1 -tags=integration $(PKG)

.PHONY: golden
golden: ## Tier 2 — golden vectors, exact assertions, hermetic
	$(GO) test -count=1 -run 'TestGolden' $(PKG)

.PHONY: golden-crosscheck
golden-crosscheck: ## ADR-0002 c2 — the same vectors under Python decimal, no Go involved
	$(PYTHON) tools/decimalcrosscheck/crosscheck.py testdata/golden/decimal

.PHONY: golden-register
golden-register: ## Re-baseline vector digests. A reviewed act, not a test fix (ADR-0002 §6)
	$(PYTHON) tools/decimalcrosscheck/register.py testdata/golden/decimal

.PHONY: lint
lint: ## Architectural controls as lint (ADR-0007 §2.5, ADR-0006 §2.6)
	golangci-lint run

.PHONY: vet
vet: ## Standard go vet
	$(GO) vet $(PKG)

.PHONY: fiscalfloat
fiscalfloat: ## ADR-0001 control 1 — float64 unreachable from Money/Rate/Quantity
	cd tools/fiscalfloat && $(GO) build -o ../../$(BIN)/fiscalfloat ./cmd/fiscalfloat
	./$(BIN)/fiscalfloat ./...

.PHONY: fiscalfloat-test
fiscalfloat-test: ## Prove the analyzer works before trusting it as a gate
	cd tools/fiscalfloat && $(GO) test ./...

.PHONY: vendor
vendor: ## ADR-0001 control 6 — vendor apd so the source survives upstream
	$(GO) mod vendor

.PHONY: vendor-verify
vendor-verify: ## Fail if vendor/ has drifted or been patched in place
	$(GO) mod vendor
	git diff --exit-code -- vendor/ || \
		(echo "vendor/ is out of date or locally patched — see ADR-0001 §9.2" && exit 1)

.PHONY: cover
cover: ## Coverage report. Reported, never gated (ADR-0018 §2.9)
	$(GO) test -coverprofile=coverage.out $(PKG)
	$(GO) tool cover -func=coverage.out | tail -1

.PHONY: docker-build
docker-build: ## Build the ztax-core image
	docker build --target ztax-core -t $(IMAGE):$(TRAIN_APP) .

.PHONY: docker-release
docker-release: ## Build with SBOM and provenance attached (W1 lane B)
	docker buildx build --target ztax-core \
		--sbom=true --provenance=mode=max \
		-t $(IMAGE):$(TRAIN_APP) .
	@echo "NOTE: buildx emits SPDX. Build Plan §7 requires CycloneDX —"
	@echo "      conversion and signing are lane B work, not done here."

.PHONY: up
up: ## Start the local cell — postgres + ztax-core + ztax-web
	docker compose -f ../docker-compose.yml up --build -d
	@echo "ztax-core on http://localhost:8080/healthz, ztax-web on http://localhost:3000"

.PHONY: down
down: ## Stop the local cell, keeping the database volume
	docker compose -f ../docker-compose.yml down

.PHONY: down-clean
down-clean: ## Stop the local cell and destroy the database volume
	docker compose -f ../docker-compose.yml down -v

.PHONY: logs
logs: ## Follow ztax-core logs
	docker compose -f ../docker-compose.yml logs -f ztax-core

.PHONY: check
check: vet fiscalfloat lint test golden-crosscheck ## Everything that gates a commit

.PHONY: clean
clean: ## Remove build output
	rm -rf $(BIN) coverage.out
