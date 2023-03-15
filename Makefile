# Copyright 2022 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

GO_CMD ?= go
GO_FMT ?= gofmt
GO_TEST_FLAGS ?= -race
# Use go.mod go version as a single source of truth of GO version.
GO_VERSION := $(shell awk '/^go /{print $$2}' go.mod|head -n1)

# Use go.mod go version as a single source of truth of MPI version.
MPI_VERSION := $(shell awk '/mpi-operator/{print $$2}' go.mod|head -n1)

GIT_TAG ?= $(shell git describe --tags --dirty --always)
# Image URL to use all building/pushing image targets
PLATFORMS ?= linux/amd64,linux/arm64
DOCKER_BUILDX_CMD ?= docker buildx
IMAGE_BUILD_CMD ?= $(DOCKER_BUILDX_CMD) build
IMAGE_BUILD_EXTRA_OPTS ?=
# TODO(#52): Add kueue to k8s gcr registry
IMAGE_REGISTRY ?= gcr.io/k8s-staging-kueue
IMAGE_NAME := kueue
IMAGE_REPO ?= $(IMAGE_REGISTRY)/$(IMAGE_NAME)
IMAGE_TAG ?= $(IMAGE_REPO):$(GIT_TAG)

ifdef EXTRA_TAG
IMAGE_EXTRA_TAG ?= $(IMAGE_REPO):$(EXTRA_TAG)
endif
ifdef IMAGE_EXTRA_TAG
IMAGE_BUILD_EXTRA_OPTS += -t $(IMAGE_EXTRA_TAG)
endif

ARTIFACTS ?= $(PROJECT_DIR)/bin
# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
BASE_IMAGE ?= gcr.io/distroless/static:nonroot
BUILDER_IMAGE ?= golang:$(GO_VERSION)

# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION = 1.26

INTEGRATION_TARGET ?= ./test/integration/...

E2E_TARGET ?= ./test/e2e/...

E2E_KIND_VERSION ?= kindest/node:v1.24.7

# For local testing, we should allow user to use different kind cluster name
# Default will delete default kind cluster
KIND_CLUSTER_NAME ?= kind

# Setting SHELL to bash allows bash commands to be executed by recipes.
# This is a requirement for 'setup-envtest.sh' in the test target.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

version_pkg = sigs.k8s.io/kueue/pkg/version
LD_FLAGS += -X '$(version_pkg).GitVersion=$(GIT_TAG)'
LD_FLAGS += -X '$(version_pkg).GitCommit=$(shell git rev-parse HEAD)'

.PHONY: all
all: generate fmt vet build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk commands is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) \
		rbac:roleName=manager-role output:rbac:artifacts:config=config/components/rbac\
		crd output:crd:artifacts:config=config/components/crd/bases\
		webhook output:webhook:artifacts:config=config/components/webhook\
		paths="./..."

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: fmt
fmt: ## Run go fmt against code.
	$(GO_CMD) fmt ./...

.PHONY: fmt-verify
fmt-verify:
	@out=`$(GO_FMT) -w -l -d $$(find . -name '*.go')`; \
	if [ -n "$$out" ]; then \
	    echo "$$out"; \
	    exit 1; \
	fi

.PHONY: gomod-verify
gomod-verify:
	$(GO_CMD) mod tidy
	git --no-pager diff --exit-code go.mod go.sum

.PHONY: toc-update
toc-update:
	./hack/update-toc.sh

.PHONY: toc-verify
toc-verify:
	./hack/verify-toc.sh

.PHONY: vet
vet: ## Run go vet against code.
	$(GO_CMD) vet ./...

.PHONY: test
test: generate fmt vet gotestsum ## Run tests.
	$(GOTESTSUM) --junitfile $(ARTIFACTS)/junit.xml -- $(GO_TEST_FLAGS) $(shell go list ./... | grep -v '/test/') -coverprofile $(ARTIFACTS)/cover.out

.PHONY: test-integration
test-integration: manifests generate fmt vet envtest ginkgo mpi-operator-crd ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) --arch=amd64 use $(ENVTEST_K8S_VERSION) -p path)" \
	$(GINKGO) --junit-report=junit.xml --output-dir=$(ARTIFACTS) -v $(INTEGRATION_TARGET)

CREATE_KIND_CLUSTER ?= true
.PHONY: test-e2e
test-e2e: kustomize manifests generate fmt vet envtest ginkgo
	E2E_KIND_VERSION=$(E2E_KIND_VERSION) KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) CREATE_KIND_CLUSTER=$(CREATE_KIND_CLUSTER) ARTIFACTS=$(ARTIFACTS) IMAGE_TAG=$(IMAGE_TAG) ./hack/e2e-test.sh

.PHONY: ci-lint
ci-lint: golangci-lint
	$(GOLANGCI_LINT) run --timeout 7m0s

.PHONY: verify
verify: gomod-verify vet ci-lint fmt-verify toc-verify manifests generate
	git --no-pager diff --exit-code config/components apis

##@ Build

.PHONY: build
build:
	$(GO_BUILD_ENV) $(GO_CMD) build -ldflags="$(LD_FLAGS)" -o bin/manager main.go

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	$(GO_CMD) run ./main.go

# Build the container image
.PHONY: image-local-build
image-local-build:
	BUILDER=$(shell $(DOCKER_BUILDX_CMD) create --use)
	$(MAKE) image-build PUSH=$(PUSH)
	$(DOCKER_BUILDX_CMD) rm $$BUILDER

.PHONY: image-local-push
image-local-push: PUSH=--push
image-local-push: image-local-build

.PHONY: image-build
image-build:
	$(IMAGE_BUILD_CMD) -t $(IMAGE_TAG) \
		--platform=$(PLATFORMS) \
		--build-arg BASE_IMAGE=$(BASE_IMAGE) \
		--build-arg BUILDER_IMAGE=$(BUILDER_IMAGE) \
		$(PUSH) \
		$(IMAGE_BUILD_EXTRA_OPTS) ./

.PHONY: image-push
image-push: PUSH=--push
image-push: image-build

.PHONY: kind-image-build
kind-image-build: PLATFORMS=linux/amd64
kind-image-build: IMAGE_BUILD_EXTRA_OPTS=--load
kind-image-build: kind image-build

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

clean-manifests = (cd config/components/manager && $(KUSTOMIZE) edit set image controller=gcr.io/k8s-staging-kueue/kueue:main)

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/components/crd | kubectl apply --server-side -f -

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/components/crd | kubectl delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: deploy
deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/components/manager && $(KUSTOMIZE) edit set image controller=${IMAGE_TAG}
	kubectl apply --server-side -k config/default
	@$(call clean-manifests)

.PHONY: prometheus
prometheus:
	kubectl apply --server-side -k config/prometheus

.PHONY: undeploy
undeploy: ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/default | kubectl delete --ignore-not-found=$(ignore-not-found) -f -

##@ Release
.PHONY: artifacts
artifacts: kustomize
	cd config/components/manager && $(KUSTOMIZE) edit set image controller=${IMAGE_TAG}
	if [ -d artifacts ]; then rm -rf artifacts; fi
	mkdir -p artifacts
	$(KUSTOMIZE) build config/default -o artifacts/manifests.yaml
	$(KUSTOMIZE) build config/dev -o artifacts/manifests-dev.yaml
	$(KUSTOMIZE) build config/prometheus -o artifacts/prometheus.yaml
	@$(call clean-manifests)

##@ Tools
PROJECT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))
GOLANGCI_LINT = $(shell pwd)/bin/golangci-lint
.PHONY: golangci-lint
golangci-lint: ## Download golangci-lint locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.48.0

CONTROLLER_GEN = $(shell pwd)/bin/controller-gen
.PHONY: controller-gen
controller-gen: ## Download controller-gen locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.9.2

KUSTOMIZE = $(shell pwd)/bin/kustomize
.PHONY: kustomize
kustomize: ## Download kustomize locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install sigs.k8s.io/kustomize/kustomize/v4@v4.5.7

ENVTEST = $(shell pwd)/bin/setup-envtest
.PHONY: envtest
envtest: ## Download envtest-setup locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest

GINKGO = $(shell pwd)/bin/ginkgo
.PHONY: ginkgo
ginkgo: ## Download ginkgo locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install github.com/onsi/ginkgo/v2/ginkgo@v2.1.4

GOTESTSUM = $(shell pwd)/bin/gotestsum
.PHONY: gotestsum
gotestsum: ## Download gotestsum locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install gotest.tools/gotestsum@v1.8.2
KIND = $(shell pwd)/bin/kind
.PHONY: kind
kind:
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install sigs.k8s.io/kind@v0.16.0
.PHONY: mpi-operator-crd
mpi-operator-crd:
	GOPATH=/tmp GO111MODULE=on $(GO_CMD) install github.com/kubeflow/mpi-operator/cmd/mpi-operator@$(MPI_VERSION)
	mkdir -p $(shell pwd)/dep-crds/mpi-operator/
	cp -f /tmp/pkg/mod/github.com/kubeflow/mpi-operator@$(MPI_VERSION)/manifests/base/* $(shell pwd)/dep-crds/mpi-operator/
