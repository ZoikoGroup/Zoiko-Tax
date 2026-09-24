# The Go SDK's gates. A separate module from the backend: nothing here imports
# it, and nothing that imports this module inherits the backend's graph.

GO                   ?= go
GOLANGCI_LINT        ?= golangci-lint
MODULE               := github.com/zoikogroup/zoikotax/sdk/go
CONTRACT             := ../../contracts/openapi/export/ztax.v1.3.1.yaml
GENERATED            := types.gen.go

# Pinned, and run with `go run …@version` so the generator never enters go.mod.
OAPI_CODEGEN_VERSION := v2.8.0
OAPI_CODEGEN         := $(GO) run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@$(OAPI_CODEGEN_VERSION)

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "};{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: generate
generate: ## Regenerate the models from the 3.1 export of the contract
	$(OAPI_CODEGEN) -config oapi-codegen.yaml -o $(GENERATED) $(CONTRACT)

# Regenerates into a temporary file and compares, rather than regenerating in
# place and asking git. It needs no git, leaves the tree untouched, and fails on
# an uncommitted hand edit as well as on a committed one.
.PHONY: generate-check
generate-check: ## ADR-0010 §2.1 — fail if the committed models are not what the contract generates
	@tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	$(OAPI_CODEGEN) -config oapi-codegen.yaml -o "$$tmp/$(GENERATED)" $(CONTRACT) && \
	diff -u $(GENERATED) "$$tmp/$(GENERATED)" || \
	{ echo "$(GENERATED) is not what the contract generates; run 'make generate'"; exit 1; }

.PHONY: fmt-check
fmt-check: ## Fail if anything is not gofmt-clean
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "not gofmt-clean:"; echo "$$out"; exit 1; fi

.PHONY: vet
vet: ## Standard go vet
	$(GO) vet ./...

.PHONY: tidy-check
tidy-check: ## Fail if go.mod is not tidy
	$(GO) mod tidy -diff

# The module graph is this module and nothing else. A dependency here is a
# dependency of every integration, and of its replay horizon (ADR-0001 §3.3).
.PHONY: deps-check
deps-check: ## Fail if the module has any dependency outside the standard library
	@deps=$$($(GO) list -m all | grep -v '^$(MODULE)$$'); \
	if [ -n "$$deps" ]; then echo "the SDK must depend on the standard library only, found:"; echo "$$deps"; exit 1; fi

.PHONY: test
test: ## Unit tests, with the race detector
	$(GO) test -race -count=1 ./...

.PHONY: lint
lint: ## golangci-lint, with this module's own configuration
	$(GOLANGCI_LINT) run ./...

.PHONY: check
check: fmt-check vet tidy-check deps-check generate-check test lint ## Everything that gates a commit
