# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
	GOBIN=$(shell go env GOPATH)/bin
else
	GOBIN=$(shell go env GOBIN)
endif

GO_CMD ?= go
GO_FMT ?= gofmt
# Use go.mod go version as a single source of truth of GO version.
GO_VERSION := $(shell awk '/^go /{split($$2, v, "."); print v[1] "." v[2]}' go.mod|head -n1)

ROOT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))/../../..
BIN_DIR := $(ROOT_DIR)/bin

HELM := $(BIN_DIR)/helm
ENVTEST := $(BIN_DIR)/setup-envtest
GINKGO := $(BIN_DIR)/ginkgo
GOTESTSUM := $(BIN_DIR)/gotestsum
ARTIFACTS ?= $(ROOT_DIR)/artifacts
ENVTEST_K8S_VERSION ?= 1.35
INTEGRATION_NPROCS ?= 4
CONTROLLER_GEN := $(BIN_DIR)/controller-gen
YAML_PROCESSOR = $(BIN_DIR)/yaml-processor
YAML_PROCESSOR_LOG_LEVEL ?= info
YQ := $(BIN_DIR)/yq

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
BASE_BUILDER_IMAGE ?= golang
BUILDER_IMAGE ?= $(BASE_BUILDER_IMAGE):$(GO_VERSION)
CGO_ENABLED ?= 0

$(YQ):
	cd $(ROOT_DIR) && $(MAKE) yq

$(YAML_PROCESSOR):
	cd $(ROOT_DIR) && $(MAKE) yaml-processor

$(GOTESTSUM):
	cd $(ROOT_DIR) && $(MAKE) gotestsum

$(ENVTEST):
	cd $(ROOT_DIR) && $(MAKE) envtest

$(GINKGO):
	cd $(ROOT_DIR) && $(MAKE) ginkgo

$(HELM):
	cd $(ROOT_DIR) && $(MAKE) helm

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
		$(IMAGE_BUILD_EXTRA_OPTS) \
		.

.PHONY: image-push
image-push: PUSH=--push
image-push: image-build

.PHONY: kind-image-build
kind-image-build: PLATFORMS=linux/amd64
kind-image-build: PUSH=--load
kind-image-build: image-build

.PHONY: manifests
manifests:
	$(CONTROLLER_GEN) rbac:roleName=kueue-populator-role output:rbac:artifacts:config=config/rbac paths="./pkg/controller/..."

.PHONY: compile-crd-manifests
compile-crd-manifests:
	cd $(ROOT_DIR) && $(MAKE) compile-crd-manifests

.PHONY: test-integration
test-integration: compile-crd-manifests $(ENVTEST) $(GINKGO)
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" \
	PROJECT_DIR=$(ROOT_DIR)/ \
	GOWORK=off $(GINKGO) -procs=$(INTEGRATION_NPROCS) -v ./test/integration/...

.PHONY: test-e2e
test-e2e: helm-verify-install
	./run-e2e.sh

.PHONY: test
test: $(GOTESTSUM) ## Run unit tests.
	mkdir -p $(ARTIFACTS)
	GOWORK=off $(GOTESTSUM) --junitfile $(ARTIFACTS)/junit-unit.xml -- \
		-race -coverpkg=./... -coverprofile $(ARTIFACTS)/cover-unit.out ./pkg/controller/...

.PHONY: update-helm
update-helm: manifests $(YAML_PROCESSOR)
	$(YAML_PROCESSOR) -zap-log-level=$(YAML_PROCESSOR_LOG_LEVEL) hack/processing-plan.yaml

.PHONY: helm-dep-build
helm-dep-build: $(HELM)
	$(HELM) dependency build charts/kueue-populator

.PHONY: helm-lint
helm-lint: $(HELM) helm-dep-build ## Run Helm chart lint test.
	$(HELM) lint charts/kueue-populator

.PHONY: helm-verify
helm-verify: $(HELM) helm-dep-build ## Run Helm chart lint test.
	$(HELM) template charts/kueue-populator > /dev/null
	$(HELM) template charts/kueue-populator --set image.repository=my-repo --set image.tag=v0.1.0 > /dev/null

.PHONY: helm-unit-test
helm-unit-test: $(HELM) helm-dep-build ## Run Helm chart unit tests.
	cd $(ROOT_DIR) && $(MAKE) helm-unittest-plugin
	HELM_PLUGINS=$(BIN_DIR)/helm-plugins $(HELM) unittest charts/kueue-populator --strict --debug

.PHONY: helm-test
helm-test: helm-lint helm-verify helm-unit-test ## Run Helm chart integration tests (requires active cluster).
	$(HELM) test kueue-populator

.PHONY: verify
verify: vet helm-verify helm-unit-test ## Run all verification tests.

.PHONY: helm-chart-package
helm-chart-package: $(HELM) $(YQ) ## Package a chart into a versioned chart archive file.
	DEST_CHART_DIR=$(ARTIFACTS) \
	HELM="$(HELM)" YQ="$(YQ)" GIT_TAG="$(GIT_TAG)" IMAGE_REGISTRY="$(IMAGE_REGISTRY)" \
	HELM_CHART_PUSH=$(HELM_CHART_PUSH) \
	./hack/helm-chart-package.sh

.PHONY: helm-chart-push
helm-chart-push: HELM_CHART_PUSH=true
helm-chart-push: helm-chart-package

.PHONY: helm-verify-install
helm-verify-install: ## Run Helm install verification script.
	./test/verify-helm-install.sh
