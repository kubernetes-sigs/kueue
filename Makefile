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
GIT_COMMIT ?= $(shell git rev-parse HEAD)
# Image URL to use all building/pushing image targets
HOST_IMAGE_PLATFORM ?= linux/$(shell go env GOARCH)
PLATFORMS ?= linux/amd64,linux/arm64,linux/s390x,linux/ppc64le
CLI_PLATFORMS ?= linux/amd64,linux/arm64,darwin/amd64,darwin/arm64
VIZ_PLATFORMS ?= linux/amd64,linux/arm64,linux/s390x,linux/ppc64le
DOCKER_BUILDX_CMD ?= docker buildx
IMAGE_BUILD_CMD ?= $(DOCKER_BUILDX_CMD) build

STAGING_IMAGE_REGISTRY := us-central1-docker.pkg.dev/k8s-staging-images/kueue
IMAGE_REGISTRY ?= $(STAGING_IMAGE_REGISTRY)

IMAGE_REPO := $(IMAGE_REGISTRY)/kueue
IMAGE_REPO_KUEUEVIZ_BACKEND := $(IMAGE_REGISTRY)/kueueviz-backend
IMAGE_REPO_KUEUEVIZ_FRONTEND := $(IMAGE_REGISTRY)/kueueviz-frontend

IMAGE_TAG := $(IMAGE_REPO):$(GIT_TAG)
IMAGE_TAG_KUEUEVIZ_BACKEND := $(IMAGE_REPO_KUEUEVIZ_BACKEND):$(GIT_TAG)
IMAGE_TAG_KUEUEVIZ_FRONTEND := $(IMAGE_REPO_KUEUEVIZ_FRONTEND):$(GIT_TAG)

RAY_VERSION := 2.41.0
RAYMINI_VERSION ?= 0.0.1

PROJECT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))
BIN_DIR ?= $(PROJECT_DIR)/bin
ARTIFACTS ?= $(BIN_DIR)
TOOLS_DIR := $(PROJECT_DIR)/hack/internal/tools

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
BASE_IMAGE ?= gcr.io/distroless/static:nonroot
BUILDER_IMAGE ?= golang:$(GO_VERSION)
CGO_ENABLED ?= 0

YAML_PROCESSOR_LOG_LEVEL ?= info

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
LD_FLAGS += -X '$(version_pkg).GitCommit=$(GIT_COMMIT)'
LD_FLAGS += -X '$(version_pkg).BuildDate=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)'

# Update these variables when preparing a new release or a release branch.
# Then run `make prepare-release-branch`
RELEASE_VERSION=v0.13.2
RELEASE_BRANCH=main
# Application version for Helm and npm (strips leading 'v' from RELEASE_VERSION)
APP_VERSION := $(shell echo $(RELEASE_VERSION) | cut -c2-)

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
update-helm: manifests yq yaml-processor
	$(BIN_DIR)/yaml-processor -zap-log-level=$(YAML_PROCESSOR_LOG_LEVEL) hack/processing-plan.yaml

.PHONY: generate
generate: gomod-download generate-apiref generate-code generate-kueuectl-docs generate-helm-docs

.PHONY: generate-code
generate-code: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations and client-go libraries.
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

.PHONY: helm-lint
helm-lint: helm ## Run Helm chart lint test.
	${HELM} lint charts/kueue

.PHONY: helm-verify
helm-verify: helm helm-lint ## run helm template and detect any rendering failures
# test default values
	$(HELM) template charts/kueue > /dev/null
# test nondefault options (kueueviz, prometheus, certmanager)
	$(HELM) template charts/kueue --set enableKueueViz=true --set enableCertManager=true --set enablePrometheus=true > /dev/null
# test added managedJobsNamespaceSelector option
	$(HELM) template charts/kueue --set managerConfig.controllerManagerConfigYaml="managedJobsNamespaceSelector:\n  matchExpressions:\n    - key: kubernetes.io/metadata.name\n      operator: In\n      values: [ kube-system ]" > /dev/null
# test priorityClassName option
	$(HELM) template charts/kueue --set controllerManager.manager.priorityClassName="system-cluster-critical" > /dev/null
# test controllerManager nodeSelector and tolerations
	$(HELM) template charts/kueue --set controllerManager.nodeSelector.nodetype=infra --set 'controllerManager.tolerations[0].key=node-role.kubernetes.io/master' --set 'controllerManager.tolerations[0].operator=Exists' --set 'controllerManager.tolerations[0].effect=NoSchedule' > /dev/null
# test kueueViz backend nodeSelector and tolerations
	$(HELM) template charts/kueue --set enableKueueViz=true --set kueueViz.backend.nodeSelector.nodetype=infra --set 'kueueViz.backend.tolerations[0].key=node-role.kubernetes.io/master' --set 'kueueViz.backend.tolerations[0].operator=Exists' --set 'kueueViz.backend.tolerations[0].effect=NoSchedule' > /dev/null
# test kueueViz frontend nodeSelector and tolerations
	$(HELM) template charts/kueue --set enableKueueViz=true --set kueueViz.frontend.nodeSelector.nodetype=infra --set 'kueueViz.frontend.tolerations[0].key=node-role.kubernetes.io/master' --set 'kueueViz.frontend.tolerations[0].operator=Exists' --set 'kueueViz.frontend.tolerations[0].effect=NoSchedule' > /dev/null
# test kueueViz priorityClassName options for backend
	$(HELM) template charts/kueue --set enableKueueViz=true --set kueueViz.backend.priorityClassName="system-cluster-critical" > /dev/null
# test kueueViz priorityClassName options for frontend
	$(HELM) template charts/kueue --set enableKueueViz=true --set kueueViz.backend.priorityClassName="system-cluster-critical" > /dev/null

.PHONY: i18n-verify
i18n-verify: ## Verify all localized docs are in sync with English version. Usage: make i18n-verify [TARGET_LANG=zh-CN]
	@if [ -n "$(TARGET_LANG)" ]; then \
		if [ ! -d "$(PROJECT_DIR)/site/content/$(TARGET_LANG)/docs" ]; then \
			echo "Error: $(PROJECT_DIR)/site/content/$(TARGET_LANG)/docs does not exist"; \
			exit 1; \
		fi; \
		echo "Checking $(TARGET_LANG) docs sync status..."; \
		$(PROJECT_DIR)/site/scripts/lsync.sh "$(PROJECT_DIR)/site/content/$(TARGET_LANG)/docs/"; \
	else \
		for lang_dir in $(PROJECT_DIR)/site/content/*/; do \
			lang_code=$$(basename "$$lang_dir"); \
			if [ "$$lang_code" != "en" ] && [ -d "$$lang_dir/docs" ]; then \
				echo "Checking $$lang_code docs sync status..."; \
				$(PROJECT_DIR)/site/scripts/lsync.sh "$(PROJECT_DIR)/site/content/$$lang_code/docs/"; \
			fi; \
		done; \
	fi

# test
.PHONY: helm-unit-test
helm-unit-test: helm
	$(HELM) unittest charts/kueue --strict --debug

.PHONY: vet
vet: ## Run go vet against code.
	$(GO_CMD) vet ./...

.PHONY: ci-lint
ci-lint: golangci-lint
	find . -path ./site -prune -false -o -name go.mod -exec dirname {} \; | xargs -I {} sh -c 'cd "{}" && $(GOLANGCI_LINT) run $(GOLANGCI_LINT_FIX) --timeout 15m0s --config "$(PROJECT_DIR)/.golangci.yaml"'

.PHONY: lint-fix
lint-fix: GOLANGCI_LINT_FIX=--fix
lint-fix: ci-lint

.PHONY: shell-lint
shell-lint: ## Run shell linting.
	$(PROJECT_DIR)/hack/shellcheck/verify.sh

.PHONY: sync-hugo-version
sync-hugo-version:
	$(SED) -r 's/(.*(HUGO_VERSION).*)/  HUGO_VERSION = "$(subst v,,$(HUGO_VERSION))"/g' -i netlify.toml

PATHS_TO_VERIFY := config/components apis charts/kueue client-go site/ netlify.toml
.PHONY: verify
verify: gomod-verify ci-lint fmt-verify shell-lint toc-verify manifests generate update-helm helm-verify helm-unit-test prepare-release-branch sync-hugo-version npm-depcheck
	git --no-pager diff --exit-code $(PATHS_TO_VERIFY)
	if git ls-files --exclude-standard --others $(PATHS_TO_VERIFY) | grep -q . ; then exit 1; fi

.PHONY: npm-depcheck
npm-depcheck:
	$(PROJECT_DIR)/hack/depcheck/verify.sh $(PROJECT_DIR)/cmd/kueueviz/frontend
	$(PROJECT_DIR)/hack/depcheck/verify.sh $(PROJECT_DIR)/test/e2e/kueueviz

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
	$(MAKE) image-build PUSH="$(PUSH)" IMAGE_BUILD_EXTRA_OPTS="$(IMAGE_BUILD_EXTRA_OPTS)"
	$(DOCKER_BUILDX_CMD) rm $$BUILDER

# Build the multiplatform container image locally and push to repo.
.PHONY: image-local-push
image-local-push: PUSH=--push
image-local-push: image-local-build

.PHONY: image-build
image-build:
	$(IMAGE_BUILD_CMD) \
		-t $(IMAGE_TAG) \
		-t $(IMAGE_REPO):$(RELEASE_BRANCH) \
		--platform=$(PLATFORMS) \
		--build-arg BASE_IMAGE=$(BASE_IMAGE) \
		--build-arg BUILDER_IMAGE=$(BUILDER_IMAGE) \
		--build-arg CGO_ENABLED=$(CGO_ENABLED) \
		--build-arg GIT_TAG=$(GIT_TAG) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		$(PUSH) \
		$(IMAGE_BUILD_EXTRA_OPTS) \
		./

.PHONY: image-push
image-push: PUSH=--push
image-push: image-build

.PHONY: helm-chart-package
helm-chart-package: yq helm ## Package a chart into a versioned chart archive file.
	DEST_CHART_DIR=$(DEST_CHART_DIR) \
	HELM="$(HELM)" YQ="$(YQ)" GIT_TAG="$(GIT_TAG)" IMAGE_REGISTRY="$(IMAGE_REGISTRY)" \
	HELM_CHART_PUSH=$(HELM_CHART_PUSH) \
	./hack/helm-chart-package.sh

.PHONY: helm-chart-push
helm-chart-push: HELM_CHART_PUSH=true
helm-chart-push: helm-chart-package

# Build an image just for the host architecture that can be used for Kind E2E tests.
.PHONY: kind-image-build
kind-image-build: PLATFORMS=$(HOST_IMAGE_PLATFORM)
kind-image-build: PUSH=--load
kind-image-build: kind image-build

.PHONY: yaml-processor
yaml-processor:
	cd $(TOOLS_DIR)/yaml-processor && \
	$(GO_BUILD_ENV) $(GO_CMD) build -ldflags="$(LD_FLAGS)" -o $(BIN_DIR)/yaml-processor

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

clean-manifests = \
	(cd config/components/manager && \
		$(KUSTOMIZE) edit set image controller=$(STAGING_IMAGE_REGISTRY)/kueue:$(RELEASE_BRANCH)) && \
	(cd config/components/kueueviz && \
  		$(KUSTOMIZE) edit set image backend=$(STAGING_IMAGE_REGISTRY)/kueueviz-backend:$(RELEASE_BRANCH) && \
  		$(KUSTOMIZE) edit set image frontend=$(STAGING_IMAGE_REGISTRY)/kueueviz-frontend:$(RELEASE_BRANCH))

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/components/crd | kubectl apply --server-side -f -

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/components/crd | kubectl delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: deploy
deploy: manifests kustomize prepare-manifests ## Deploy controller to the K8s cluster specified in ~/.kube/config.
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
.PHONY: clean-artifacts
clean-artifacts:
	if [ -d artifacts ]; then rm -rf artifacts; fi

.PHONY: prepare-manifests
prepare-manifests:
	cd config/components/manager && $(KUSTOMIZE) edit set image controller=$(IMAGE_TAG)
	cd config/components/kueueviz && $(KUSTOMIZE) edit set image backend=$(IMAGE_TAG_KUEUEVIZ_BACKEND)
	cd config/components/kueueviz && $(KUSTOMIZE) edit set image frontend=$(IMAGE_TAG_KUEUEVIZ_FRONTEND)

.PHONY: artifacts
artifacts: DEST_CHART_DIR="artifacts"
artifacts: clean-artifacts kustomize helm-chart-package prepare-manifests ## Generate release artifacts.
	$(KUSTOMIZE) build config/default -o artifacts/manifests.yaml
	$(KUSTOMIZE) build config/dev -o artifacts/manifests-dev.yaml
	$(KUSTOMIZE) build config/alpha-enabled -o artifacts/manifests-alpha-enabled.yaml
	$(KUSTOMIZE) build config/prometheus -o artifacts/prometheus.yaml
	$(KUSTOMIZE) build config/visibility-apf -o artifacts/visibility-apf.yaml
	$(KUSTOMIZE) build config/kueueviz -o artifacts/kueueviz.yaml
	@$(call clean-manifests)
	CGO_ENABLED=$(CGO_ENABLED) GO_CMD="$(GO_CMD)" LD_FLAGS="$(LD_FLAGS)" BUILD_DIR="artifacts" BUILD_NAME=kubectl-kueue PLATFORMS="$(CLI_PLATFORMS)" ./hack/multiplatform-build.sh ./cmd/kueuectl/main.go

.PHONY: prepare-release-branch
prepare-release-branch: yq kustomize ## Prepare the release branch with the release version.
	$(SED) -r 's/v[0-9]+\.[0-9]+\.[0-9]+/$(RELEASE_VERSION)/g' -i README.md -i site/hugo.toml -i cmd/kueueviz/INSTALL.md
	$(SED) -r 's/chart_version = "[0-9]+\.[0-9]+\.[0-9]+/chart_version = "$(APP_VERSION)/g' -i README.md -i site/hugo.toml
	$(SED) -r 's/--version="[0-9]+\.[0-9]+\.[0-9]+/--version="$(APP_VERSION)/g' -i charts/kueue/README.md.gotmpl -i cmd/kueueviz/INSTALL.md
	$(YQ) e '.appVersion = "$(RELEASE_VERSION)" | .version = "$(APP_VERSION)"' -i charts/kueue/Chart.yaml
	$(YQ) e '.controllerManager.manager.image.tag = "$(RELEASE_BRANCH)" | .kueueViz.backend.image.tag = "$(RELEASE_BRANCH)" | .kueueViz.frontend.image.tag = "$(RELEASE_BRANCH)"' -i charts/kueue/values.yaml
	$(YQ) e '.version = "$(APP_VERSION)"' -i cmd/kueueviz/frontend/package.json
	$(YQ) e '.version = "$(APP_VERSION)" | .packages[""].version = "$(APP_VERSION)"' -i cmd/kueueviz/frontend/package-lock.json
	$(YQ) e '.version = "$(APP_VERSION)"' -i test/e2e/kueueviz/package.json
	$(YQ) e '.version = "$(APP_VERSION)" | .packages[""].version = "$(APP_VERSION)"' -i test/e2e/kueueviz/package-lock.json
	$(MAKE) generate-helm-docs

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
	$(IMAGE_BUILD_CMD) \
		-t $(IMAGE_REGISTRY)/debug:$(GIT_TAG) \
		-t $(IMAGE_REGISTRY)/debug:$(RELEASE_BRANCH) \
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
		-t $(IMAGE_REGISTRY)/importer:$(RELEASE_BRANCH) \
		--platform=$(PLATFORMS) \
		--build-arg BASE_IMAGE=$(BASE_IMAGE) \
		--build-arg BUILDER_IMAGE=$(BUILDER_IMAGE) \
		--build-arg CGO_ENABLED=$(CGO_ENABLED) \
		$(PUSH) \
		$(IMAGE_BUILD_EXTRA_OPTS) \
		-f ./cmd/importer/Dockerfile ./

.PHONY: importer-image-push
importer-image-push: PUSH=--push
importer-image-push: importer-image-build

# Build a docker local us-central1-docker.pkg.dev/k8s-staging-images/kueue/importer image
.PHONY: importer-image
importer-image: PLATFORMS=$(HOST_IMAGE_PLATFORM)
importer-image: PUSH=--load
importer-image: importer-image-build


# Build the kueueviz dashboard images (frontend and backend)
.PHONY: kueueviz-image-build
kueueviz-image-build:
	$(IMAGE_BUILD_CMD) \
		-t $(IMAGE_TAG_KUEUEVIZ_BACKEND) \
		-t $(IMAGE_REPO_KUEUEVIZ_BACKEND):$(RELEASE_BRANCH) \
		--platform=$(VIZ_PLATFORMS) \
		--build-arg BASE_IMAGE=$(BASE_IMAGE) \
		--build-arg BUILDER_IMAGE=$(BUILDER_IMAGE) \
		--build-arg CGO_ENABLED=$(CGO_ENABLED) \
		$(PUSH) \
		$(IMAGE_BUILD_EXTRA_OPTS) \
		-f ./cmd/kueueviz/backend/Dockerfile ./cmd/kueueviz/backend
	$(IMAGE_BUILD_CMD) \
		-t $(IMAGE_TAG_KUEUEVIZ_FRONTEND) \
		-t $(IMAGE_REPO_KUEUEVIZ_FRONTEND):$(RELEASE_BRANCH) \
		--platform=$(VIZ_PLATFORMS) \
		$(PUSH) \
		$(IMAGE_BUILD_EXTRA_OPTS) \
		-f ./cmd/kueueviz/frontend/Dockerfile ./cmd/kueueviz/frontend

.PHONY: kueueviz-image-push
kueueviz-image-push: PUSH=--push
kueueviz-image-push: kueueviz-image-build

# Build a docker local us-central1-docker.pkg.dev/k8s-staging-images/kueue/kueueviz image
.PHONY: kueueviz-image
kueueviz-image: VIZ_PLATFORMS=$(HOST_IMAGE_PLATFORM)
kueueviz-image: PUSH=--load
kueueviz-image: kueueviz-image-build

.PHONY: kueuectl
kueuectl:
	CGO_ENABLED=$(CGO_ENABLED) $(GO_BUILD_ENV) $(GO_CMD) build -ldflags="$(LD_FLAGS)" -o $(BIN_DIR)/kubectl-kueue cmd/kueuectl/main.go

.PHONY: generate-apiref
generate-apiref: genref
	cd $(PROJECT_DIR)/hack/genref/ && $(GENREF) -o $(PROJECT_DIR)/site/content/en/docs/reference

.PHONY: generate-kueuectl-docs
generate-kueuectl-docs: kueuectl-docs
	rm -Rf $(PROJECT_DIR)/site/content/en/docs/reference/kubectl-kueue/commands/kueuectl*
	$(BIN_DIR)/kueuectl-docs \
		$(PROJECT_DIR)/cmd/kueuectl-docs/templates \
		$(PROJECT_DIR)/site/content/en/docs/reference/kubectl-kueue/commands

.PHONY: generate-helm-docs
generate-helm-docs: helm-docs
	$(HELM_DOCS) -c $(PROJECT_DIR)/charts/kueue

# Build the ray-project-mini image
.PHONY: ray-project-mini-image-build
ray-project-mini-image-build:
	$(IMAGE_BUILD_CMD) \
		-t $(IMAGE_REGISTRY)/ray-project-mini:$(RAYMINI_VERSION) \
		-t $(IMAGE_REGISTRY)/ray-project-mini:$(RELEASE_BRANCH) \
		--platform=$(PLATFORMS) \
		--build-arg RAY_VERSION=$(RAY_VERSION) \
		$(PUSH) \
		$(IMAGE_BUILD_EXTRA_OPTS) \
		-f ./hack/internal/test-images/ray/Dockerfile ./ \

# The step is required for local e2e test run
.PHONY: kind-ray-project-mini-image-build
kind-ray-project-mini-image-build: PLATFORMS=$(HOST_IMAGE_PLATFORM)
kind-ray-project-mini-image-build: PUSH=--load
kind-ray-project-mini-image-build: ray-project-mini-image-build
