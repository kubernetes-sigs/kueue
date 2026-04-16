BIN_DIR ?= $(shell pwd)/bin
GOLANGCI_LINT_VERSION ?= v2.11.4
GOLANGCI_LINT = $(BIN_DIR)/golangci-lint

.PHONY: test test-unit test-e2e test-examples lint lint-fix golangci-lint

test: test-unit test-e2e

test-unit:
	go test -v ./...

test-e2e:
	go test -v -tags=e2e -timeout=5m ./test/e2e/...

test-examples:
	cd examples && go test -v -timeout=5m ./...

golangci-lint:
	@GOBIN=$(BIN_DIR) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

lint: golangci-lint
	$(GOLANGCI_LINT) run ./...

lint-fix: golangci-lint
	$(GOLANGCI_LINT) run --fix ./...
