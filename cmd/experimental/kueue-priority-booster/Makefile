# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
	GOBIN=$(shell go env GOPATH)/bin
else
	GOBIN=$(shell go env GOBIN)
endif

GO_CMD ?= go
GO_FMT ?= gofmt

ROOT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))/../../..
BIN_DIR := $(ROOT_DIR)/bin

# Use go.mod go version as a single source of truth of GO version.
GO_VERSION := $(shell awk '/^go /{split($$2, v, "."); print v[1] "." v[2]}' $(ROOT_DIR)/go.mod|head -n1)

ENVTEST := $(BIN_DIR)/setup-envtest
GINKGO := $(BIN_DIR)/ginkgo
GOTESTSUM := $(BIN_DIR)/gotestsum
ARTIFACTS ?= $(ROOT_DIR)/artifacts
ENVTEST_K8S_VERSION ?= 1.35
KUBEBUILDER_ASSETS = $(or $(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path),$(error setup-envtest failed to download binaries. KUBEBUILDER_ASSETS is empty))
INTEGRATION_NPROCS ?= 4
CONTROLLER_GEN := $(BIN_DIR)/controller-gen
YAML_PROCESSOR := $(BIN_DIR)/yaml-processor
YAML_PROCESSOR_LOG_LEVEL ?= info
HELM := $(BIN_DIR)/helm
YQ := $(BIN_DIR)/yq

GIT_TAG ?= $(shell git describe --tags --dirty --always)
GIT_COMMIT ?= $(shell git rev-parse HEAD)
HOST_IMAGE_PLATFORM ?= linux/$(shell go env GOARCH)
PLATFORMS ?= $(HOST_IMAGE_PLATFORM)
DOCKER_BUILDX_CMD ?= docker buildx
IMAGE_BUILD_CMD ?= $(DOCKER_BUILDX_CMD) build

IMAGE_REGISTRY ?= us-central1-docker.pkg.dev/k8s-staging-images/kueue
IMAGE_NAME := kueue-priority-booster
IMAGE_REPO := $(IMAGE_REGISTRY)/$(IMAGE_NAME)
IMAGE_TAG := $(IMAGE_REPO):$(GIT_TAG)

# Use distroless as minimal base image to package the manager binary
BASE_IMAGE ?= gcr.io/distroless/static:nonroot
BASE_BUILDER_IMAGE ?= golang
BUILDER_IMAGE ?= $(BASE_BUILDER_IMAGE):$(GO_VERSION)
CGO_ENABLED ?= 0

$(GOTESTSUM):
	cd $(ROOT_DIR) && $(MAKE) gotestsum

$(ENVTEST):
	cd $(ROOT_DIR) && $(MAKE) envtest

$(GINKGO):
	cd $(ROOT_DIR) && $(MAKE) ginkgo

$(CONTROLLER_GEN):
	cd $(ROOT_DIR) && $(MAKE) controller-gen

$(YAML_PROCESSOR):
	cd $(ROOT_DIR) && $(MAKE) yaml-processor

$(HELM):
	cd $(ROOT_DIR) && $(MAKE) helm

$(YQ):
	cd $(ROOT_DIR) && $(MAKE) yq

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
		-f Dockerfile \
		-t $(IMAGE_TAG) \
		--platform=$(PLATFORMS) \
		--build-arg BASE_IMAGE=$(BASE_IMAGE) \
		--build-arg BUILDER_IMAGE=$(BUILDER_IMAGE) \
		--build-arg CGO_ENABLED=$(CGO_ENABLED) \
		$(PUSH) \
		$(IMAGE_BUILD_EXTRA_OPTS) \
		$(ROOT_DIR)

.PHONY: image-push
image-push: PUSH=--push
image-push: image-build

.PHONY: kind-image-build
kind-image-build: PLATFORMS=linux/amd64
kind-image-build: PUSH=--load
kind-image-build: image-build

.PHONY: manifests
manifests:
	$(CONTROLLER_GEN) rbac:roleName=kueue-priority-booster-role output:rbac:artifacts:config=config/rbac paths="./pkg/controller/..."

.PHONY: compile-crd-manifests
compile-crd-manifests:
	cd $(ROOT_DIR) && $(MAKE) compile-crd-manifests

.PHONY: test-integration
test-integration: compile-crd-manifests $(ENVTEST) $(GINKGO)
	KUBEBUILDER_ASSETS="$(KUBEBUILDER_ASSETS)" \
	PROJECT_DIR=$(ROOT_DIR)/ \
	GOWORK=off $(GINKGO) -procs=$(INTEGRATION_NPROCS) -v ./test/integration/...

.PHONY: test
test: $(GOTESTSUM) ## Run unit tests.
	mkdir -p $(ARTIFACTS)
	GOWORK=off $(GOTESTSUM) --junitfile $(ARTIFACTS)/junit-unit.xml -- \
		-race -coverpkg=./... -coverprofile $(ARTIFACTS)/cover-unit.out ./pkg/...

.PHONY: update-helm
update-helm: manifests $(YAML_PROCESSOR) ## Generate the Helm chart templates from config/.
	$(YAML_PROCESSOR) -zap-log-level=$(YAML_PROCESSOR_LOG_LEVEL) hack/processing-plan.yaml

.PHONY: helm-dep-build
helm-dep-build: $(HELM)
	$(HELM) dependency build charts/kueue-priority-booster

.PHONY: helm-lint
helm-lint: $(HELM) helm-dep-build ## Run Helm chart lint test.
	$(HELM) lint charts/kueue-priority-booster

.PHONY: helm-verify
helm-verify: $(HELM) helm-dep-build ## Verify the Helm chart renders.
	$(HELM) template charts/kueue-priority-booster > /dev/null
	$(HELM) template charts/kueue-priority-booster --set kueuePriorityBooster.image.repository=my-repo --set kueuePriorityBooster.image.tag=v0.1.0 > /dev/null

.PHONY: verify
verify: vet helm-verify ## Run all verification tests.
