# Makefile for the wiki.
#
# The target names come from AGENTS.md and from the command list the project
# expects to exist. `make check` is what CI runs and what CONTRIBUTING.md tells
# you to run before every push; if you need a fourth thing, add a target rather
# than a paragraph in this comment.
#
# Pinned tool versions live here and nowhere else. CI reads them from this file
# with `make print-<tool>-version`, so a bump is one edit.
#
# The Go toolchain is pinned here for the same reason, and it matters more than
# the other two. gofmt may change its output in *any* release, on purpose, and
# `make fmt-check` is a byte-for-byte comparison -- so a developer's gofmt and
# CI's gofmt disagreeing is a red build over nothing, and the fix is never to
# ignore the check. The version has to be named, and it has to be the one CI
# runs. It must match the `go` line in go.mod, because `go-version-file: go.mod`
# is what the CI jobs install; `make check-go-version` is the test that says so.
GO_VERSION := 1.25.0

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

# The prefix `make fuzz` uses to find fuzz targets. Override it to fuzz one of
# them: make fuzz FUZZ=FuzzNewSlug
FUZZ ?= Fuzz

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
check: fmt-check generate-check vet lint test ## Everything CI runs. Do this before pushing.

.PHONY: test
test: ## Run the tests with the race detector
	go test $(TEST_FLAGS) ./...

.PHONY: cover
cover: ## Report coverage and fail below $(COVERAGE_MIN)%
	@# -coverpkg=./... is what makes a package that is only ever run by
	@# another package's tests count as covered. internal/store/testsuite is
	@# the first such package and it is most of the M1 test suite; without the
	@# flag each package is measured by its own binary, a test helper reports
	@# 0%, and the gate fails on code the tests exercise on every run.
	go test $(TEST_FLAGS) -covermode=atomic -coverpkg=./... -coverprofile=coverage.out ./...
	@# Generated templ output is excluded from the profile before it is read, for
	@# the same reason `.golangci.yml` excludes it from linting: it is not code
	@# anybody wrote, it is what the template compiler produced from the
	@# `.templ` files, and measuring it says something about the templates'
	@# element-by-element branches rather than about whether the application is
	@# tested. The filter is a filename and nothing else, and the raw profile is
	@# still written so a reviewer can diff the generated part separately.
	@grep -v '_templ\.go:' coverage.out > coverage-handwritten.out || true
	@go tool cover -func=coverage-handwritten.out | tail -1
	@total=$$(go tool cover -func=coverage-handwritten.out | tail -1 | awk '{gsub("%","",$$NF); print $$NF}'); \
	awk -v total="$$total" -v min="$(COVERAGE_MIN)" 'BEGIN { \
		if (total + 0 < min + 0) { \
			printf "coverage %.1f%% is below the %.0f%% gate\n", total, min; \
			exit 1; \
		} \
		printf "coverage %.1f%% meets the %.0f%% gate\n", total, min; \
	}'

.PHONY: fuzz
fuzz: ## Run every fuzz target for $(SHORT_FUZZ_TIME)
	@# `go test -fuzz` refuses more than one package, and refuses a pattern
	@# that matches more than one target in a package, so the names are
	@# listed and each one is fuzzed on its own. A package with no fuzz
	@# target contributes nothing rather than a "no tests to run" line that
	@# reads like a pass.
	@failed=0; \
	for pkg in $$(go list ./...); do \
		for target in $$(go test -list='^$(FUZZ)' "$$pkg" 2>/dev/null | grep '^$(FUZZ)'); do \
			echo "fuzzing $$pkg.$$target"; \
			go test -run='^$$' -fuzz="^$$target$$" -fuzztime=$(SHORT_FUZZ_TIME) "$$pkg" || failed=1; \
		done; \
	done; \
	exit $$failed

.PHONY: reindex
reindex: ## Rebuild the index from the vault, discarding the database
	go run ./cmd/wiki reindex --full

# The pin and go.mod have to agree, because CI installs Go from go.mod and
# formats with the pin. If they drift, `make fmt-check` fails on a file nobody
# changed -- which is the failure this whole mechanism exists to prevent, reached
# by the mechanism itself.
.PHONY: check-go-version
check-go-version:
	@declared=$$(awk '/^go /{print $$2; exit}' go.mod); \
	if [ "$$declared" != "$(GO_VERSION)" ]; then \
		echo "the pinned Go is $(GO_VERSION) and go.mod declares $$declared."; \
		echo "CI installs go.mod's version and formats with the pin, so they have to match."; \
		echo "Change GO_VERSION in the Makefile, or the go line in go.mod, not both."; \
		exit 1; \
	fi

# ----------------------------------------------------------------- lint ----

.PHONY: lint
lint: ## Run golangci-lint
	golangci-lint run

.PHONY: vet
vet: ## Run go vet
	go vet ./...

# `gofmt` is a separate binary from the `go` command, so `GOTOOLCHAIN` does not
# reach the one on PATH. `go env GOROOT` under the pin does, and that is how the
# recipe below gets the right gofmt:
#
#     gofmt=$$(GOTOOLCHAIN=go$(GO_VERSION) go env GOROOT)/bin/gofmt
#
# It is a recipe and not a `$(shell)` so that `make help` does not resolve it,
# and therefore does not download a toolchain on a machine that has not got it.
.PHONY: fmt
fmt: ## Rewrite files with gofmt and goimports
	golangci-lint fmt
	@gofmt=$$(GOTOOLCHAIN=go$(GO_VERSION) go env GOROOT)/bin/gofmt; \
	"$$gofmt" -s -w $$(git ls-files '*.go')

.PHONY: fmt-check
fmt-check: check-go-version ## Fail if any file is not gofmt clean. Needs no installed tools.
	@gofmt=$$(GOTOOLCHAIN=go$(GO_VERSION) go env GOROOT)/bin/gofmt; \
	echo "fmt-check with go$(GO_VERSION)"; \
	unformatted=$$("$$gofmt" -s -l . | grep -v '^\.kilo/' || true); \
	if [ -n "$$unformatted" ]; then \
		echo "not gofmt clean:"; echo "$$unformatted"; exit 1; \
	fi

# -------------------------------------------------------------- generate ---

# `templ generate` turns every `templates.templ` into the `templates_templ.go` that
# is committed beside it. The generated file is committed rather than built, so
# that `go test` needs no toolchain beyond Go -- and so that a reviewer reads the
# `.templ` and the diff in the generated file, rather than having to run a compiler
# to see what changed.
#
# The generated files are excluded from `.golangci.yml` and from the coverage
# profile for the same reason: they are not code anybody wrote, and measuring them
# says something about the templates' element branches rather than about whether
# the application is tested.
.PHONY: generate
generate: ## Regenerate the templ templates from their .templ sources
	templ generate

# The check is that running it changes nothing, which is the property that makes a
# committed generated file safe: a contributor who edits a `.templ` and forgets to
# regenerate gets told, rather than shipping a template and code that disagree.
.PHONY: generate-check
generate-check: ## Fail if the templ output is out of date
	@# The comparison is between the bytes on disk before and after regenerating,
	@# not between git and the working tree: a contributor who has already run
	@# `make generate` and staged the result has a *correct* generated file, and a
	@# check that asks git whether the tree is clean fails them for having done
	@# the right thing. `git hash-object` hashes any file, so this works on a file
	@# that is not tracked either.
	@before=$$(git hash-object $$(git ls-files '*_templ.go')); \
	templ generate; \
	after=$$(git hash-object $$(git ls-files '*_templ.go')); \
	if [ "$$before" != "$$after" ]; then \
		echo "the generated templates are out of date; run 'make generate' and commit the result"; \
		exit 1; \
	fi

# --------------------------------------------------------------- version ---

# CI reads these so that the pinned tool versions have exactly one home.
.PHONY: print-go-version
print-go-version:
	@echo $(GO_VERSION)

.PHONY: print-golangci-lint-version
print-golangci-lint-version:
	@echo $(GOLANGCI_LINT_VERSION)

.PHONY: print-templ-version
print-templ-version:
	@echo $(TEMPL_VERSION)

.PHONY: print-coverage-min
print-coverage-min:
	@echo $(COVERAGE_MIN)
