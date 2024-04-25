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

# Use go.mod go version as a single source of truth of Ginkgo version.
GINKGO_VERSION ?= $(shell $(GO_CMD) list -m -f '{{.Version}}' github.com/onsi/ginkgo/v2)

GIT_TAG ?= $(shell git describe --tags --dirty --always)
# Image URL to use all building/pushing image targets
PLATFORMS ?= linux/amd64,linux/arm64,linux/s390x,linux/ppc64le
DOCKER_BUILDX_CMD ?= docker buildx
IMAGE_BUILD_CMD ?= $(DOCKER_BUILDX_CMD) build
IMAGE_BUILD_EXTRA_OPTS ?=
# TODO(#52): Add kueue to k8s gcr registry
STAGING_IMAGE_REGISTRY := gcr.io/k8s-staging-kueue
IMAGE_REGISTRY ?= $(STAGING_IMAGE_REGISTRY)
IMAGE_NAME := kueue
IMAGE_REPO ?= $(IMAGE_REGISTRY)/$(IMAGE_NAME)
IMAGE_TAG ?= $(IMAGE_REPO):$(GIT_TAG)

ifdef EXTRA_TAG
IMAGE_EXTRA_TAG ?= $(IMAGE_REPO):$(EXTRA_TAG)
endif
ifdef IMAGE_EXTRA_TAG
IMAGE_BUILD_EXTRA_OPTS += -t $(IMAGE_EXTRA_TAG)
endif

PROJECT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))
ARTIFACTS ?= $(PROJECT_DIR)/bin
# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
BASE_IMAGE ?= gcr.io/distroless/static:nonroot
BUILDER_IMAGE ?= golang:$(GO_VERSION)
CGO_ENABLED ?= 0

# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION ?= 1.29

INTEGRATION_TARGET ?= ./test/integration/...

E2E_TARGET ?= ./test/e2e/...

E2E_KIND_VERSION ?= kindest/node:v1.29.2

# E2E_K8S_VERSIONS sets the list of k8s versions included in test-e2e-all
E2E_K8S_VERSIONS ?= 1.27.11 1.28.7 1.29.2

# For local testing, we should allow user to use different kind cluster name
# Default will delete default kind cluster
KIND_CLUSTER_NAME ?= kind

# Number of processes to use during integration tests to run specs within a
# suite in parallel. Suites still run sequentially. User may set this value to 1
# to run without parallelism.
INTEGRATION_NPROCS ?= 4

# Setting SHELL to bash allows bash commands to be executed by recipes.
# This is a requirement for 'setup-envtest.sh' in the test target.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

# Setting SED allows macos users to install GNU sed and use the latter
# instead of the default BSD sed.
SED ?= $(shell command -v sed)
ifeq ($(shell command -v gsed 2>/dev/null),)
    SED = $(shell command -v sed)
endif
ifeq ($(shell ${SED} --version 2>&1 | grep -q GNU; echo $$?),1)
    $(error !!! GNU sed is required. If on OS X, use 'brew install gnu-sed'.)
endif

version_pkg = sigs.k8s.io/kueue/pkg/version
LD_FLAGS += -X '$(version_pkg).GitVersion=$(GIT_TAG)'
LD_FLAGS += -X '$(version_pkg).GitCommit=$(shell git rev-parse HEAD)'

# Update these variables when preparing a new release or a release branch.
# Then run `make prepare-release-branch`
RELEASE_VERSION=v0.6.2
RELEASE_BRANCH=main

# JobSet Version
JOBSET_VERSION = $(shell $(GO_CMD) list -m -f "{{.Version}}" sigs.k8s.io/jobset)

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
		crd:generateEmbeddedObjectMeta=true output:crd:artifacts:config=config/components/crd/bases\
		paths="./apis/..."
	$(CONTROLLER_GEN) \
		rbac:roleName=manager-role output:rbac:artifacts:config=config/components/rbac\
		webhook output:webhook:artifacts:config=config/components/webhook\
		paths="./pkg/controller/...;./pkg/webhooks/...;./pkg/util/cert/...;./pkg/visibility/..."

.PHONY: update-helm
update-helm: manifests yq
	SED=$(SED) ./hack/update-helm.sh

.PHONY: generate
generate: gomod-download controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations and client-go libraries.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./apis/..."
	./hack/update-codegen.sh $(GO_CMD)

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

.PHONY: gomod-download
gomod-download:
	$(GO_CMD) mod download

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
test: gotestsum ## Run tests.
	$(GOTESTSUM) --junitfile $(ARTIFACTS)/junit.xml -- $(GO_TEST_FLAGS) $(shell $(GO_CMD) list ./... | grep -v '/test/') -coverpkg=./... -coverprofile $(ARTIFACTS)/cover.out

.PHONY: test-integration
test-integration: gomod-download envtest ginkgo mpi-operator-crd ray-operator-crd jobset-operator-crd kf-training-operator-crd  cluster-autoscaler-crd ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" \
	$(GINKGO) $(GINKGO_ARGS) -procs=$(INTEGRATION_NPROCS) --junit-report=junit.xml --output-dir=$(ARTIFACTS) -v $(INTEGRATION_TARGET)

CREATE_KIND_CLUSTER ?= true
.PHONY: test-e2e
test-e2e: kustomize ginkgo yq gomod-download jobset-operator-crd run-test-e2e-$(E2E_KIND_VERSION:kindest/node:v%=%)

.PHONY: test-multikueue-e2e
test-multikueue-e2e: kustomize ginkgo yq gomod-download jobset-operator-crd run-test-multikueue-e2e-$(E2E_KIND_VERSION:kindest/node:v%=%)


E2E_TARGETS := $(addprefix run-test-e2e-,${E2E_K8S_VERSIONS})
MULTIKUEUE-E2E_TARGETS := $(addprefix run-test-multikueue-e2e-,${E2E_K8S_VERSIONS})
.PHONY: test-e2e-all
test-e2e-all: ginkgo $(E2E_TARGETS) $(MULTIKUEUE-E2E_TARGETS)

FORCE:

run-test-e2e-%: K8S_VERSION = $(@:run-test-e2e-%=%)
run-test-e2e-%: FORCE
	@echo Running e2e for k8s ${K8S_VERSION}
	E2E_KIND_VERSION="kindest/node:v$(K8S_VERSION)" KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) CREATE_KIND_CLUSTER=$(CREATE_KIND_CLUSTER) ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(GINKGO_ARGS)" JOBSET_VERSION=$(JOBSET_VERSION) ./hack/e2e-test.sh

run-test-multikueue-e2e-%: K8S_VERSION = $(@:run-test-multikueue-e2e-%=%)
run-test-multikueue-e2e-%: FORCE
	@echo Running multikueue e2e for k8s ${K8S_VERSION}
	E2E_KIND_VERSION="kindest/node:v$(K8S_VERSION)" KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) CREATE_KIND_CLUSTER=$(CREATE_KIND_CLUSTER) ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(GINKGO_ARGS)" JOBSET_VERSION=$(JOBSET_VERSION) ./hack/multikueue-e2e-test.sh

SCALABILITY_RUNNER := $(ARTIFACTS)/scalability-runner
.PHONY: scalability-runner
scalability-runner:
	$(GO_BUILD_ENV) $(GO_CMD) build -ldflags="$(LD_FLAGS)" -o $(SCALABILITY_RUNNER) test/scalability/runner/main.go

.PHONY: minimalkueue
minimalkueue: 
	$(GO_BUILD_ENV) $(GO_CMD) build -ldflags="$(LD_FLAGS)" -o  $(ARTIFACTS)/minimalkueue test/scalability/minimalkueue/main.go

ifdef SCALABILITY_CPU_PROFILE
SCALABILITY_EXTRA_ARGS += --withCPUProfile=true
endif

ifdef SCALABILITY_KUEUE_LOGS
SCALABILITY_EXTRA_ARGS +=  --withLogs=true --logToFile=true
endif

ifdef SCALABILITY_SCRAPE_INTERVAL
SCALABILITY_SCRAPE_ARGS +=  --metricsScrapeInterval=$(SCALABILITY_SCRAPE_INTERVAL)
endif

ifdef SCALABILITY_SCRAPE_URL
SCALABILITY_SCRAPE_ARGS +=  --metricsScrapeURL=$(SCALABILITY_SCRAPE_URL)
endif

SCALABILITY_GENERATOR_CONFIG ?= $(PROJECT_DIR)/test/scalability/default_generator_config.yaml

SCALABILITY_RUN_DIR := $(ARTIFACTS)/run-scalability
.PHONY: run-scalability
run-scalability: envtest scalability-runner minimalkueue
	mkdir -p $(SCALABILITY_RUN_DIR)
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" \
	$(SCALABILITY_RUNNER) \
		--o $(SCALABILITY_RUN_DIR) \
		--crds=$(PROJECT_DIR)/config/components/crd/bases \
		--generatorConfig=$(SCALABILITY_GENERATOR_CONFIG) \
		--minimalKueue=$(ARTIFACTS)/minimalkueue $(SCALABILITY_EXTRA_ARGS) $(SCALABILITY_SCRAPE_ARGS)

.PHONY: test-scalability
test-scalability: gotestsum run-scalability
	$(GOTESTSUM) --junitfile $(ARTIFACTS)/junit.xml -- $(GO_TEST_FLAGS) ./test/scalability/checker  \
		--summary=$(SCALABILITY_RUN_DIR)/summary.yaml \
		--cmdStats=$(SCALABILITY_RUN_DIR)/minimalkueue.stats.yaml \
		--range=$(PROJECT_DIR)/test/scalability/default_rangespec.yaml

.PHONY: run-scalability-in-cluster
run-scalability-in-cluster: envtest scalability-runner
	mkdir -p $(ARTIFACTS)/run-scalability-in-cluster
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" \
	$(SCALABILITY_RUNNER) \
		--o $(ARTIFACTS)/run-scalability-in-cluster \
		--generatorConfig=$(SCALABILITY_GENERATOR_CONFIG) \
		--qps=1000 --burst=2000 --timeout=15m $(SCALABILITY_SCRAPE_ARGS)

.PHONY: ci-lint
ci-lint: golangci-lint
	$(GOLANGCI_LINT) run --timeout 15m0s

.PHONY: lint-fix
lint-fix: golangci-lint
	$(GOLANGCI_LINT) run --fix --timeout 15m0s

.PHONY: verify
verify: gomod-verify ci-lint fmt-verify shell-lint toc-verify manifests generate update-helm generate-apiref prepare-release-branch
	git --no-pager diff --exit-code config/components apis charts/kueue/templates client-go site/

##@ Build

.PHONY: build
build:
	$(GO_BUILD_ENV) $(GO_CMD) build -ldflags="$(LD_FLAGS)" -o bin/manager cmd/kueue/main.go

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	$(GO_CMD) run cmd/kueue/main.go

# Build the multiplatform container image locally.
.PHONY: image-local-build
image-local-build:
	BUILDER=$(shell $(DOCKER_BUILDX_CMD) create --use)
	$(MAKE) image-build PUSH=$(PUSH)
	$(DOCKER_BUILDX_CMD) rm $$BUILDER

# Build the multiplatform container image locally and push to repo.
.PHONY: image-local-push
image-local-push: PUSH=--push
image-local-push: image-local-build

.PHONY: image-build
image-build:
	$(IMAGE_BUILD_CMD) -t $(IMAGE_TAG) \
		--platform=$(PLATFORMS) \
		--build-arg BASE_IMAGE=$(BASE_IMAGE) \
		--build-arg BUILDER_IMAGE=$(BUILDER_IMAGE) \
		--build-arg CGO_ENABLED=$(CGO_ENABLED) \
		$(PUSH) \
		$(IMAGE_BUILD_EXTRA_OPTS) ./

.PHONY: image-push
image-push: PUSH=--push
image-push: image-build

# Build an amd64 image that can be used for Kind E2E tests.
.PHONY: kind-image-build
kind-image-build: PLATFORMS=linux/amd64
kind-image-build: IMAGE_BUILD_EXTRA_OPTS=--load
kind-image-build: kind image-build

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

clean-manifests = (cd config/components/manager && $(KUSTOMIZE) edit set image controller=gcr.io/k8s-staging-kueue/kueue:$(RELEASE_BRANCH))

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

.PHONY: site-server
site-server: hugo
	(cd site; $(HUGO) server)

##@ Release
.PHONY: artifacts
artifacts: kustomize yq helm
	cd config/components/manager && $(KUSTOMIZE) edit set image controller=${IMAGE_TAG}
	if [ -d artifacts ]; then rm -rf artifacts; fi
	mkdir -p artifacts
	$(KUSTOMIZE) build config/default -o artifacts/manifests.yaml
	$(KUSTOMIZE) build config/dev -o artifacts/manifests-dev.yaml
	$(KUSTOMIZE) build config/alpha-enabled -o artifacts/manifests-alpha-enabled.yaml
	$(KUSTOMIZE) build config/prometheus -o artifacts/prometheus.yaml
	$(KUSTOMIZE) build config/visibility -o artifacts/visibility-api.yaml
	@$(call clean-manifests)
	# Update the image tag and policy
	$(YQ)  e  '.controllerManager.manager.image.repository = "$(IMAGE_REPO)" | .controllerManager.manager.image.tag = "$(GIT_TAG)" | .controllerManager.manager.image.pullPolicy = "IfNotPresent"' -i charts/kueue/values.yaml
	# create the package. TODO: consider signing it
	$(HELM) package --version $(GIT_TAG) --app-version $(GIT_TAG) charts/kueue -d artifacts/
	mv artifacts/kueue-$(GIT_TAG).tgz artifacts/kueue-chart-$(GIT_TAG).tgz
	# Revert the image changes
	$(YQ)  e  '.controllerManager.manager.image.repository = "$(IMAGE_REGISTRY)/$(IMAGE_NAME)" | .controllerManager.manager.image.tag = "main" | .controllerManager.manager.image.pullPolicy = "Always"' -i charts/kueue/values.yaml

.PHONY: prepare-release-branch
prepare-release-branch: yq kustomize
	$(SED) -r 's/v[0-9]+\.[0-9]+\.[0-9]+/$(RELEASE_VERSION)/g' -i README.md -i site/hugo.toml
	$(YQ) e '.appVersion = "$(RELEASE_VERSION)"' -i charts/kueue/Chart.yaml
	@$(call clean-manifests)

##@ Tools

# Build an image that can be used with kubectl debug
# Developers don't need to build this image, as it will be available as gcr.io/k8s-staging-kueue/debug
.PHONY: debug-image-push
debug-image-push:
	$(IMAGE_BUILD_CMD) -t $(IMAGE_REGISTRY)/debug:$(GIT_TAG) \
		--platform=$(PLATFORMS) \
		--push ./hack/debugpod

# Build the importer binary
.PHONY: importer-build
importer-build:
	$(GO_BUILD_ENV) $(GO_CMD) build -ldflags="$(LD_FLAGS)" -o bin/importer cmd/importer/main.go

.PHONY: importer-image-build
importer-image-build:
	$(IMAGE_BUILD_CMD) \
		-t $(IMAGE_REGISTRY)/importer:$(GIT_TAG) \
		-t $(IMAGE_REGISTRY)/importer:$(RELEASE_BRANCH)-latest \
		--platform=$(PLATFORMS) \
		--build-arg BASE_IMAGE=$(BASE_IMAGE) \
		--build-arg BUILDER_IMAGE=$(BUILDER_IMAGE) \
		--build-arg CGO_ENABLED=$(CGO_ENABLED) \
		$(PUSH) \
		-f ./cmd/importer/Dockerfile ./

.PHONY: importer-image-push
importer-image-push: PUSH=--push
importer-image-push: importer-image-build

# Build a docker local gcr.io/k8s-staging-kueue/importer image
.PHONY: importer-image
importer-image: PLATFORMS=linux/amd64
importer-image: PUSH=--load
importer-image: importer-image-build

GOLANGCI_LINT = $(PROJECT_DIR)/bin/golangci-lint
.PHONY: golangci-lint
golangci-lint: ## Download golangci-lint locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.57.2

CONTROLLER_GEN = $(PROJECT_DIR)/bin/controller-gen
.PHONY: controller-gen
controller-gen: ## Download controller-gen locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install sigs.k8s.io/controller-tools/cmd/controller-gen

.PHONY: shell-lint
shell-lint:
	./hack/verify-shellcheck.sh

KUSTOMIZE = $(PROJECT_DIR)/bin/kustomize
.PHONY: kustomize
kustomize: ## Download kustomize locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install sigs.k8s.io/kustomize/kustomize/v4@v4.5.7

ENVTEST = $(PROJECT_DIR)/bin/setup-envtest
.PHONY: envtest
envtest: ## Download envtest-setup locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install sigs.k8s.io/controller-runtime/tools/setup-envtest@v0.0.0-20240320141353-395cfc7486e6

GINKGO = $(PROJECT_DIR)/bin/ginkgo
.PHONY: ginkgo
ginkgo: ## Download ginkgo locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install github.com/onsi/ginkgo/v2/ginkgo@$(GINKGO_VERSION)

GOTESTSUM = $(PROJECT_DIR)/bin/gotestsum
.PHONY: gotestsum
gotestsum: ## Download gotestsum locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install gotest.tools/gotestsum@v1.8.2

KIND = $(PROJECT_DIR)/bin/kind
.PHONY: kind
kind:
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install sigs.k8s.io/kind@v0.20.0

YQ = $(PROJECT_DIR)/bin/yq
.PHONY: yq
yq: ## Download yq locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install github.com/mikefarah/yq/v4@v4.34.1

HELM = $(PROJECT_DIR)/bin/helm
.PHONY: helm
helm: ## Download helm locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install helm.sh/helm/v3/cmd/helm@v3.12.1

GENREF = $(PROJECT_DIR)/bin/genref
.PHONY: genref
genref: ## Download genref locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin $(GO_CMD) install github.com/kubernetes-sigs/reference-docs/genref@v0.28.0

HUGO = $(PROJECT_DIR)/bin/hugo
.PHONY: hugo
hugo:
	@GOBIN=$(PROJECT_DIR)/bin CGO_ENABLED=1 $(GO_CMD) install -tags extended github.com/gohugoio/hugo@v0.124.1

MPIROOT = $(shell $(GO_CMD) list -m -f "{{.Dir}}" github.com/kubeflow/mpi-operator)
.PHONY: mpi-operator-crd
mpi-operator-crd:
	mkdir -p $(PROJECT_DIR)/dep-crds/mpi-operator/
	cp -f $(MPIROOT)/manifests/base/* $(PROJECT_DIR)/dep-crds/mpi-operator/

KFTRAININGROOT = $(shell $(GO_CMD) list -m -f "{{.Dir}}" github.com/kubeflow/training-operator)
.PHONY: kf-training-operator-crd
kf-training-operator-crd:
	mkdir -p $(PROJECT_DIR)/dep-crds/training-operator/
	cp -f $(KFTRAININGROOT)/manifests/base/crds/* $(PROJECT_DIR)/dep-crds/training-operator/

RAYROOT = $(shell $(GO_CMD) list -m -f "{{.Dir}}" github.com/ray-project/kuberay/ray-operator)
.PHONY: ray-operator-crd
ray-operator-crd:
	mkdir -p $(PROJECT_DIR)/dep-crds/ray-operator/
	cp -f $(RAYROOT)/config/crd/bases/* $(PROJECT_DIR)/dep-crds/ray-operator/

JOBSETROOT = $(shell $(GO_CMD) list -m -f "{{.Dir}}" sigs.k8s.io/jobset)
.PHONY: jobset-operator-crd
jobset-operator-crd:
	mkdir -p $(PROJECT_DIR)/dep-crds/jobset-operator/
	cp -f $(JOBSETROOT)/config/components/crd/bases/* $(PROJECT_DIR)/dep-crds/jobset-operator/


CAROOT = $(shell $(GO_CMD) list -m -f "{{.Dir}}" k8s.io/autoscaler/cluster-autoscaler/apis)
.PHONY: cluster-autoscaler-crd
cluster-autoscaler-crd:
	mkdir -p $(PROJECT_DIR)/dep-crds/cluster-autoscaler/
	cp -f $(CAROOT)/config/crd/* $(PROJECT_DIR)/dep-crds/cluster-autoscaler/

.PHONY: generate-apiref
generate-apiref: genref
	cd $(PROJECT_DIR)/hack/genref/ && $(GENREF) -o $(PROJECT_DIR)/site/content/en/docs/reference
