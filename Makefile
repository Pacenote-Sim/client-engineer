# Pacenote engineer companion — developer entry points. Every target is what CI runs.
SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
export PATH := $(PATH):$(shell go env GOPATH)/bin

# The tools, each named once. GNU Make runs a recipe line without a shell when
# it holds no shell metacharacters, and that search uses make's own PATH, so a
# tool installed by `go install` is resolved here.
GOBIN        := $(shell go env GOPATH)/bin
GOFUMPT      := $(shell command -v gofumpt       2>/dev/null || echo $(GOBIN)/gofumpt)
GOLANGCILINT := $(shell command -v golangci-lint 2>/dev/null || echo $(GOBIN)/golangci-lint)
GOVULNCHECK  := $(shell command -v govulncheck   2>/dev/null || echo $(GOBIN)/govulncheck)

TESTFLAGS := -race -shuffle=on -count=1
COVER_MIN := 90

.DEFAULT_GOAL := check

.PHONY: help check build vet lint lint-fix fmt fmt-check test cover bench bench-smoke tidy-check vuln clean

## help: list targets
help:
	@grep -E '^## [a-z-]+:' $(MAKEFILE_LIST) | sed -E 's/^## ([a-z-]+): */\1\t/' | column -t -s $$'\t'

## check: everything CI runs, in order
check: fmt-check build vet lint test cover bench-smoke tidy-check

## build: compile the module
build:
	go build ./...

## vet: go vet
vet:
	go vet ./...

## lint: golangci-lint
lint:
	$(GOLANGCILINT) run ./...

## lint-fix: golangci-lint, fixing what it can
lint-fix:
	$(GOLANGCILINT) run --fix ./...

## fmt: gofumpt in place
fmt:
	$(GOFUMPT) -w .

## fmt-check: fail if anything is unformatted
fmt-check:
	@out=$$($(GOFUMPT) -l .); if [ -n "$$out" ]; then echo "$$out"; exit 1; fi

## test: the suite, with the race detector
test:
	go test $(TESTFLAGS) ./...

## cover: coverage — every package over $(COVER_MIN)%
cover:
	go test -covermode=atomic -coverprofile=coverage.out ./...
	scripts/coverage.sh coverage.out $(COVER_MIN)

## bench: what the contract costs where it is hot
bench:
	go test -run XXX -bench . -benchmem ./...

## bench-smoke: run each benchmark once, so they cannot rot unnoticed
bench-smoke:
	go test -run XXX -bench . -benchtime=1x ./...

## tidy-check: fail if go.mod or go.sum would change, as a checkout outside the workspace sees them
tidy-check:
	GOWORK=off go mod tidy -diff

## vuln: govulncheck
vuln:
	$(GOVULNCHECK) ./...

## clean: remove build outputs
clean:
	rm -f coverage.out
