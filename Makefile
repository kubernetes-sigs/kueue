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
# Use go.mod go version as a single source of truth of GO version.
GO_VERSION := $(shell awk '/^go /{print $$2}' go.mod|head -n1)

GIT_TAG ?= $(shell git describe --tags --dirty --always)
# Image URL to use all building/pushing image targets
PLATFORMS ?= linux/amd64,linux/arm64,linux/s390x,linux/ppc64le
CLI_PLATFORMS ?= linux/amd64,linux/arm64,darwin/amd64,darwin/arm64
DOCKER_BUILDX_CMD ?= docker buildx
IMAGE_BUILD_CMD ?= $(DOCKER_BUILDX_CMD) build
IMAGE_BUILD_EXTRA_OPTS ?=
STAGING_IMAGE_REGISTRY := us-central1-docker.pkg.dev/k8s-staging-images
IMAGE_REGISTRY ?= $(STAGING_IMAGE_REGISTRY)/kueue
IMAGE_NAME := kueue
IMAGE_REPO ?= $(IMAGE_REGISTRY)/$(IMAGE_NAME)
IMAGE_TAG ?= $(IMAGE_REPO):$(GIT_TAG)
HELM_CHART_REPO := $(STAGING_IMAGE_REGISTRY)/charts

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

# Setting SHELL to bash allows bash commands to be executed by recipes.
# This is a requirement for 'setup-envtest.sh' in the test target.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

# Setting SED allows macos users to install GNU sed and use the latter
# instead of the default BSD sed.
ifeq ($(shell command -v gsed 2>/dev/null),)
    SED ?= $(shell command -v sed)
else
    SED ?= $(shell command -v gsed)
endif
ifeq ($(shell ${SED} --version 2>&1 | grep -q GNU; echo $$?),1)
    $(error !!! GNU sed is required. If on OS X, use 'brew install gnu-sed'.)
endif

version_pkg = sigs.k8s.io/kueue/pkg/version
LD_FLAGS += -X '$(version_pkg).GitVersion=$(GIT_TAG)'
LD_FLAGS += -X '$(version_pkg).GitCommit=$(shell git rev-parse HEAD)'

# Update these variables when preparing a new release or a release branch.
# Then run `make prepare-release-branch`
RELEASE_VERSION=v0.9.1
RELEASE_BRANCH=main

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
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-24s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

include Makefile-deps.mk

include Makefile-test.mk

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
	TOOLS_DIR=${TOOLS_DIR} ./hack/update-codegen.sh $(GO_CMD)

.PHONY: fmt
fmt: ## Run go fmt against code.
	$(GO_CMD) fmt ./...

.PHONY: fmt-verify
fmt-verify:
	@out=`$(GO_FMT) -w -l -d $$(find . -name '*.go' | grep -v /vendor/)`; \
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
toc-update: mdtoc
	./hack/update-toc.sh

.PHONY: toc-verify
toc-verify: mdtoc
	./hack/verify-toc.sh

.PHONY: vet
vet: ## Run go vet against code.
	$(GO_CMD) vet ./...

.PHONY: ci-lint
ci-lint: golangci-lint
	$(GOLANGCI_LINT) run --timeout 15m0s

.PHONY: lint-fix
lint-fix: golangci-lint
	$(GOLANGCI_LINT) run --fix --timeout 15m0s

.PHONY: shell-lint
shell-lint: ## Run shell linting.
	$(PROJECT_DIR)/hack/shellcheck/verify.sh

PATHS_TO_VERIFY := config/components apis charts/kueue/templates client-go site/
.PHONY: verify
verify: gomod-verify ci-lint fmt-verify shell-lint toc-verify manifests generate update-helm generate-apiref generate-kueuectl-docs prepare-release-branch
	git --no-pager diff --exit-code $(PATHS_TO_VERIFY)
	if git ls-files --exclude-standard --others $(PATHS_TO_VERIFY) | grep -q . ; then exit 1; fi

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

.PHONY: helm-chart-push
helm-chart-push: yq helm
	EXTRA_TAG="$(EXTRA_TAG)" GIT_TAG="$(GIT_TAG)" IMAGE_REGISTRY="$(IMAGE_REGISTRY)" HELM_CHART_REPO="$(HELM_CHART_REPO)" IMAGE_REPO="$(IMAGE_REPO)" HELM="$(HELM)" YQ="$(YQ)" ./hack/push-chart.sh

# Build an amd64 image that can be used for Kind E2E tests.
.PHONY: kind-image-build
kind-image-build: PLATFORMS=linux/amd64
kind-image-build: IMAGE_BUILD_EXTRA_OPTS=--load
kind-image-build: kind image-build

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

clean-manifests = (cd config/components/manager && $(KUSTOMIZE) edit set image controller=us-central1-docker.pkg.dev/k8s-staging-images/kueue/kueue:$(RELEASE_BRANCH))

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
artifacts: kustomize yq helm ## Generate release artifacts.
	cd config/components/manager && $(KUSTOMIZE) edit set image controller=${IMAGE_TAG}
	if [ -d artifacts ]; then rm -rf artifacts; fi
	mkdir -p artifacts
	$(KUSTOMIZE) build config/default -o artifacts/manifests.yaml
	$(KUSTOMIZE) build config/dev -o artifacts/manifests-dev.yaml
	$(KUSTOMIZE) build config/alpha-enabled -o artifacts/manifests-alpha-enabled.yaml
	$(KUSTOMIZE) build config/prometheus -o artifacts/prometheus.yaml
	$(KUSTOMIZE) build config/visibility-apf/default -o artifacts/visibility-apf.yaml
	$(KUSTOMIZE) build config/visibility-apf/1_28 -o artifacts/visibility-apf-1-28.yaml
	@$(call clean-manifests)
	# Update the image tag and policy
	$(YQ)  e  '.controllerManager.manager.image.repository = "$(IMAGE_REPO)" | .controllerManager.manager.image.tag = "$(GIT_TAG)" | .controllerManager.manager.image.pullPolicy = "IfNotPresent"' -i charts/kueue/values.yaml
	# create the package. TODO: consider signing it
	$(HELM) package --version $(GIT_TAG) --app-version $(GIT_TAG) charts/kueue -d artifacts/
	mv artifacts/kueue-$(GIT_TAG).tgz artifacts/kueue-chart-$(GIT_TAG).tgz
	# Revert the image changes
	$(YQ)  e  '.controllerManager.manager.image.repository = "$(IMAGE_REGISTRY)/$(IMAGE_NAME)" | del(.controllerManager.manager.image.tag) | .controllerManager.manager.image.pullPolicy = "Always"' -i charts/kueue/values.yaml
	CGO_ENABLED=$(CGO_ENABLED) GO_CMD="$(GO_CMD)" LD_FLAGS="$(LD_FLAGS)" BUILD_DIR="artifacts" BUILD_NAME=kubectl-kueue PLATFORMS="$(CLI_PLATFORMS)" ./hack/multiplatform-build.sh ./cmd/kueuectl/main.go

.PHONY: prepare-release-branch
prepare-release-branch: yq kustomize ## Prepare the release branch with the release version.
	$(SED) -r 's/v[0-9]+\.[0-9]+\.[0-9]+/$(RELEASE_VERSION)/g' -i README.md -i site/hugo.toml
	$(SED) -r 's/--version="v[0-9]+\.[0-9]+\.[0-9]+/--version="$(RELEASE_VERSION)/g' -i charts/kueue/README.md
	$(YQ) e '.appVersion = "$(RELEASE_VERSION)"' -i charts/kueue/Chart.yaml
	@$(call clean-manifests)

.PHONY: update-security-insights
update-security-insights: yq
	$(YQ) e '.header.last-updated = "$(shell git log -1 --date=short --format=%cd $(GIT_TAG))"' -i SECURITY-INSIGHTS.yaml
	$(YQ) e '.header.last-reviewed = "$(shell git log -1 --date=short --format=%cd $(GIT_TAG))"' -i SECURITY-INSIGHTS.yaml
	$(YQ) e '.header.commit-hash = "$(shell git rev-list -1 $(GIT_TAG))"' -i SECURITY-INSIGHTS.yaml
	$(YQ) e '.header.project-release = "$(shell echo "$(GIT_TAG)" | $(SED) 's/v//g')"' -i SECURITY-INSIGHTS.yaml
	$(YQ) e '.distribution-points[0] = "https://github.com/kubernetes-sigs/kueue/releases/download/$(GIT_TAG)/manifests.yaml"' -i SECURITY-INSIGHTS.yaml
	$(YQ) e '.dependencies.sbom[0].sbom-file = "https://github.com/kubernetes-sigs/kueue/releases/download/$(GIT_TAG)/kueue-$(GIT_TAG).spdx.json"' -i SECURITY-INSIGHTS.yaml

##@ Debug

# Build an image that can be used with kubectl debug
# Developers don't need to build this image, as it will be available as us-central1-docker.pkg.dev/k8s-staging-images/kueue/debug
.PHONY: debug-image-push
debug-image-push: ## Build and push the debug image to the registry
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

# Build a docker local us-central1-docker.pkg.dev/k8s-staging-images/kueue/importer image
.PHONY: importer-image
importer-image: PLATFORMS=linux/amd64
importer-image: PUSH=--load
importer-image: importer-image-build

.PHONY: kueuectl
kueuectl:
	CGO_ENABLED=$(CGO_ENABLED) $(GO_BUILD_ENV) $(GO_CMD) build -ldflags="$(LD_FLAGS)" -o $(PROJECT_DIR)/bin/kubectl-kueue cmd/kueuectl/main.go

.PHONY: generate-apiref
generate-apiref: genref
	cd $(PROJECT_DIR)/hack/genref/ && $(GENREF) -o $(PROJECT_DIR)/site/content/en/docs/reference

.PHONY: generate-kueuectl-docs
generate-kueuectl-docs: kueuectl-docs
	rm -Rf $(PROJECT_DIR)/site/content/en/docs/reference/kubectl-kueue/commands/kueuectl*
	$(PROJECT_DIR)/bin/kueuectl-docs \
		$(PROJECT_DIR)/cmd/kueuectl-docs/templates \
		$(PROJECT_DIR)/site/content/en/docs/reference/kubectl-kueue/commands
