.PHONY: test
test:
	go test -v -race ./...

.PHONY: simple-test
simple-test:
	go test -v ./...

.PHONY: cover
cover:
	go test -coverprofile=cover.out ./...

.PHONY: cover-html
cover-html: cover
	go tool cover -html=cover.out

.PHONY: ycat/build
ycat/build:
	go build -o ycat ./cmd/ycat

.PHONY: lint
lint: golangci-lint ## Run golangci-lint
	@$(GOLANGCI_LINT) run

.PHONY: fmt
fmt: golangci-lint ## Ensure consistent code style
	@go mod tidy
	@go fmt ./...
	@$(GOLANGCI_LINT) run --fix

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
GOLANGCI_LINT ?= $(LOCALBIN)/golangci-lint

## Tool Versions
GOLANGCI_VERSION := 1.61.0

.PHONY: golangci-lint
.PHONY: $(GOLANGCI_LINT)
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	@test -s $(LOCALBIN)/golangci-lint && $(LOCALBIN)/golangci-lint version --format short | grep -q $(GOLANGCI_VERSION) || \
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(LOCALBIN) v$(GOLANGCI_VERSION)
