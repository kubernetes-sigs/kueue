# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
	GOBIN=$(shell go env GOPATH)/bin
else
	GOBIN=$(shell go env GOBIN)
endif

GO_CMD ?= go
GO_FMT ?= gofmt

GIT_TAG ?= $(shell git describe --tags --dirty --always)
GIT_COMMIT ?= $(shell git rev-parse HEAD)
HOST_IMAGE_PLATFORM ?= linux/$(shell go env GOARCH)
PLATFORMS ?= $(HOST_IMAGE_PLATFORM)
DOCKER_BUILDX_CMD ?= docker buildx
IMAGE_BUILD_CMD ?= $(DOCKER_BUILDX_CMD) build

IMAGE_REGISTRY ?= us-central1-docker.pkg.dev/k8s-staging-images/kueue
IMAGE_NAME := kueue-populator
IMAGE_REPO := $(IMAGE_REGISTRY)/$(IMAGE_NAME)
IMAGE_TAG := $(IMAGE_REPO):$(GIT_TAG)

# Use distroless as minimal base image to package the manager binary
BASE_IMAGE ?= gcr.io/distroless/static:nonroot
BASE_BUILDER_IMAGE ?= golang:1.25.0
BUILDER_IMAGE ?= $(BASE_BUILDER_IMAGE)
CGO_ENABLED ?= 0

.PHONY: all
all: fmt vet build

.PHONY: fmt
fmt: ## Run go fmt against code.
	$(GO_CMD) fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	$(GO_CMD) vet ./...

.PHONY: build
build:
	$(GO_BUILD_ENV) $(GO_CMD) build -ldflags="$(LD_FLAGS)" -o bin/manager main.go

.PHONY: image-build
image-build:
	$(IMAGE_BUILD_CMD) \
		-t $(IMAGE_TAG) \
		--platform=$(PLATFORMS) \
		--build-arg BASE_IMAGE=$(BASE_IMAGE) \
		--build-arg BUILDER_IMAGE=$(BUILDER_IMAGE) \
		--build-arg CGO_ENABLED=$(CGO_ENABLED) \
		$(PUSH) \
		.

.PHONY: image-push
image-push: PUSH=--push
image-push: image-build

.PHONY: kind-image-build
kind-image-build: PLATFORMS=linux/amd64
kind-image-build: PUSH=--load
kind-image-build: image-build

# Integration tests
ROOT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))/../../..
BIN_DIR := $(ROOT_DIR)/bin
ENVTEST := $(BIN_DIR)/setup-envtest
GINKGO := $(BIN_DIR)/ginkgo
GOTESTSUM := $(BIN_DIR)/gotestsum
ARTIFACTS ?= $(ROOT_DIR)/artifacts
ENVTEST_K8S_VERSION ?= 1.34
INTEGRATION_NPROCS ?= 4
CONTROLLER_GEN := $(BIN_DIR)/controller-gen

.PHONY: manifests
manifests:
	$(CONTROLLER_GEN) rbac:roleName=kueue-populator-role output:rbac:artifacts:config=config/rbac paths="./pkg/controller/..."

.PHONY: test-integration
test-integration:
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" \
	PROJECT_DIR=$(ROOT_DIR)/ \
	GOWORK=off $(GINKGO) -procs=$(INTEGRATION_NPROCS) -v ./test/integration/...

.PHONY: test-e2e
test-e2e:
	./run-e2e.sh

.PHONY: test
test: ## Run unit tests.
	GOWORK=off $(GOTESTSUM) --junitfile $(ARTIFACTS)/junit-unit.xml -- \
		-race -coverpkg=./... -coverprofile $(ARTIFACTS)/cover-unit.out ./pkg/controller/...
