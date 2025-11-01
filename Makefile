SHELL := bash
.DEFAULT_GOAL := build

GO ?= go
CMD_DIR := ./cmd/runproc
BIN := runproc
OUT := $(CURDIR)/$(BIN)

.PHONY: build test integration-test clean fmt vet tidy smoke help kind-e2e

help:
	@echo "Targets:"
	@echo "  build             Build the runproc binary to ./runproc"
	@echo "  test              Run all tests (including integration)"
	@echo "  integration-test  Run integration tests only"
	@echo "  fmt               Run go fmt on all packages"
	@echo "  vet               Run go vet on all packages"
	@echo "  tidy              Run go mod tidy"
	@echo "  clean             Remove built artifacts"
	@echo "  smoke             Build and run a quick local smoke test"
	@echo "  kind-e2e          Run Kind-based E2E test (creates a Kind cluster; Linux only)"

build:
	@echo "Building $(BIN) ..."
	$(GO) build -o $(OUT) $(CMD_DIR)
	@echo "Built $(OUT)"

test:
	@echo "Running tests ..."
	$(GO) test ./... -v

integration-test:
	@echo "Running integration tests ..."
	$(GO) test ./integration -v

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

tidy:
	$(GO) mod tidy

clean:
	@echo "Cleaning ..."
	rm -f $(OUT)

# Quick local smoke test using the example bundle
smoke: build
	@set -euo pipefail; \
	STATE_DIR=$$(mktemp -d 2>/dev/null || mktemp -d -t runproc); \
	export RUNPROC_STATE_DIR=$$STATE_DIR; \
	$(OUT) create --bundle $$(pwd)/examples/echo demo; \
	$(OUT) start demo; \
	true

# Kind-based end-to-end test: spins up a Kind cluster and runs a pod using the 'runproc' runtime handler
# Requirements: Linux, kubectl, and either docker or podman. 'kind' is optional (test will attempt download for linux/amd64).
kind-e2e: build
	@echo "Running Kind E2E (this will create a Kind cluster) ...";
	RUNPROC_KIND_E2E=1 $(GO) test ./integration -run TestKind_E2E_RuntimeClassPod -v
