# Makefile for the wiki.
#
# The target names come from AGENTS.md and from the command list the project
# expects to exist. `make check` is what CI runs and what CONTRIBUTING.md tells
# you to run before every push; if you need a fourth thing, add a target rather
# than a paragraph in this comment.
#
# Pinned tool versions live here and nowhere else. CI reads them from this file
# with `make print-<tool>-version`, so a bump is one edit.

SHELL := /bin/sh
.DEFAULT_GOAL := help

MODULE      := github.com/popinjayjohn/dine-and-dash-semiplane
BIN_DIR     := bin
COVERAGE_MIN := 80

GOLANGCI_LINT_VERSION := v2.13.2
TEMPL_VERSION         := v0.3.906

# Everything the build needs is vendored into the module cache rather than
# installed globally, so a clone builds the same way on every machine.
GOBIN ?= $(shell go env GOPATH)/bin

# go test flags. -shuffle=on is not optional: the suite must not depend on
# declaration order, and the only way to know is to shuffle every run.
TEST_FLAGS := -race -shuffle=on
SHORT_FUZZ_TIME := 10s

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

# ---------------------------------------------------------------- build ----

.PHONY: build
build: ## Build the wiki binary into bin/
	@mkdir -p $(BIN_DIR)
	go build -trimpath \
		-ldflags "-s -w -X $(MODULE)/internal/version.Version=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev) -X $(MODULE)/internal/version.Commit=$(shell git rev-parse --short HEAD 2>/dev/null || echo unknown) -X $(MODULE)/internal/version.Date=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)" \
		-o $(BIN_DIR)/wiki ./cmd/wiki

.PHONY: run
run: ## Build and serve the wiki
	go run ./cmd/wiki serve

.PHONY: install-tools
install-tools: ## Install the pinned linter and templ generator into GOBIN
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	go install github.com/a-h/templ/cmd/templ@$(TEMPL_VERSION)

# ----------------------------------------------------------------- test ----

.PHONY: check
check: fmt-check vet lint test ## Everything CI runs. Do this before pushing.

.PHONY: test
test: ## Run the tests with the race detector
	go test $(TEST_FLAGS) ./...

.PHONY: cover
cover: ## Report coverage and fail below $(COVERAGE_MIN)%
	go test $(TEST_FLAGS) -covermode=atomic -coverprofile=coverage.out ./...
	@go tool cover -func=coverage.out | tail -1
	@total=$$(go tool cover -func=coverage.out | tail -1 | awk '{gsub("%","",$$NF); print $$NF}'); \
	awk -v total="$$total" -v min="$(COVERAGE_MIN)" -v file=coverage.out 'BEGIN { \
		if (total + 0 < min + 0) { \
			printf "coverage %.1f%% is below the %.0f%% gate\n", total, min; \
			exit 1; \
		} \
		printf "coverage %.1f%% meets the %.0f%% gate\n", total, min; \
	}'

.PHONY: fuzz
fuzz: ## Run every fuzz target for $(SHORT_FUZZ_TIME)
	go test -run=^$$ -fuzz=$$FUZZ -fuzztime=$(SHORT_FUZZ_TIME) ./...

.PHONY: spike
spike: ## Run the Datastar spike, a separate module under spike/
	cd spike/datastar && go test $(TEST_FLAGS) ./...

.PHONY: reindex
reindex: ## Rebuild the index from the vault, discarding the database
	go run ./cmd/wiki reindex --full

# ----------------------------------------------------------------- lint ----

.PHONY: lint
lint: ## Run golangci-lint
	golangci-lint run

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: fmt
fmt: ## Rewrite files with gofmt and goimports
	golangci-lint fmt

.PHONY: fmt-check
fmt-check: ## Fail if any file is not gofmt clean. Needs no installed tools.
	@unformatted=$$(gofmt -s -l . | grep -v '^\.kilo/' || true); \
	if [ -n "$$unformatted" ]; then \
		echo "not gofmt clean:"; echo "$$unformatted"; exit 1; \
	fi

# --------------------------------------------------------------- version ---

# CI reads these so that the pinned tool versions have exactly one home.
.PHONY: print-golangci-lint-version
print-golangci-lint-version:
	@echo $(GOLANGCI_LINT_VERSION)

.PHONY: print-templ-version
print-templ-version:
	@echo $(TEMPL_VERSION)

.PHONY: print-coverage-min
print-coverage-min:
	@echo $(COVERAGE_MIN)
