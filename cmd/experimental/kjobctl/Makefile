# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION = 1.31.0

ifeq ($(shell uname),Darwin)
    GOFLAGS ?= -ldflags=-extldflags=-Wl,-ld_classic
endif

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

CGO_ENABLED ?= 0

GO_CMD ?= go
GO_FMT ?= gofmt

PROJECT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))
ARTIFACTS ?= $(PROJECT_DIR)/bin
TOOLS_DIR ?= $(PROJECT_DIR)/hack/tools
CLI_PLATFORMS ?= linux/amd64,linux/arm64,darwin/amd64,darwin/arm64
EXTERNAL_CRDS_DIR ?= $(PROJECT_DIR)/dep-crds

MANIFEST_DIR := $(PROJECT_DIR)/config/crd
MANIFEST_SOURCES := $(shell find $(MANIFEST_DIR) -name "*.yaml")
EMBEDDED_MANIFEST_DIR := $(PROJECT_DIR)/pkg/cmd/printcrds/embed
EMBEDDED_MANIFEST := $(EMBEDDED_MANIFEST_DIR)/manifest.gz

# Number of processes to use during integration tests to run specs within a
# suite in parallel. Suites still run sequentially. User may set this value to 1
# to run without parallelism.
INTEGRATION_NPROCS ?= 4
# Folder where the integration tests are located.
INTEGRATION_TARGET ?= $(PROJECT_DIR)/test/integration/...

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk command is responsible for reading the
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

##@ Release
.PHONY: artifacts
artifacts: kustomize ## Generate release artifacts.
	if [ -d artifacts ]; then rm -rf artifacts; fi
	mkdir -p artifacts
	$(KUSTOMIZE) build config/default -o artifacts/manifests.yaml
	CGO_ENABLED=$(CGO_ENABLED) GO_CMD="$(GO_CMD)" LD_FLAGS="$(LD_FLAGS)" BUILD_DIR="artifacts" BUILD_NAME=kubectl-kjob PLATFORMS="$(CLI_PLATFORMS)" ./hack/multiplatform-build.sh ./cmd/kjobctl/main.go

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate ClusterRole and CustomResourceDefinition objects.
	# We need to allow dangerous types because on RayJobSpec still uses float32 type.
	$(CONTROLLER_GEN) crd:allowDangerousTypes=true paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: gomod-download controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."
	TOOLS_DIR=${TOOLS_DIR} ./hack/update-codegen.sh $(GO_CMD)

.PHONY: verify
verify: gomod-verify ci-lint shell-lint fmt-verify manifests generate generate-kjobctl-docs embeded-manifest
	git --no-pager diff --exit-code config/crd apis client-go docs pkg/cmd/printcrds/embed

.PHONY: gomod-verify
gomod-verify:
	$(GO_CMD) mod tidy
	git --no-pager diff --exit-code go.mod go.sum

.PHONY: ci-lint
ci-lint: golangci-lint
	$(GOLANGCI_LINT) run --timeout 15m0s

.PHONY: shell-lint
shell-lint: ## Run shell linting.
	$(PROJECT_DIR)/hack/shellcheck/verify.sh

.PHONY: fmt-verify
fmt-verify:
	@out=`$(GO_FMT) -w -l -d $$(find . -name '*.go')`; \
	if [ -n "$$out" ]; then \
	    echo "$$out"; \
	    exit 1; \
	fi

$(EMBEDDED_MANIFEST): kustomize $(MANIFEST_SOURCES) 
	mkdir -p  $(EMBEDDED_MANIFEST_DIR)
	# This is embedded in the kjobctl binary. 
	$(KUSTOMIZE) build $(MANIFEST_DIR) | $(GO_CMD) run $(PROJECT_DIR)/hack/tools/gzip/gzip.go > $(EMBEDDED_MANIFEST)

embeded-manifest: $(EMBEDDED_MANIFEST)

.PHONY: gomod-download
gomod-download:
	$(GO_CMD) mod download

.PHONY: vet
vet: ## Run go vet against code.
	$(GO_CMD) vet ./...

.PHONY: test
test: verify vet test-unit test-integration ## Run all tests.

.PHONY: test-unit
test-unit: gomod-download gotestsum embeded-manifest ## Run unit tests.
	$(GOTESTSUM) --junitfile $(ARTIFACTS)/junit-unit.xml -- $(GOFLAGS) $(GO_TEST_FLAGS) $(shell $(GO_CMD) list ./... | grep -v '/test/') -coverpkg=./... -coverprofile $(ARTIFACTS)/cover.out

.PHONY: test-integration
test-integration: gomod-download envtest ginkgo embeded-manifest ray-operator-crd ## Run integration tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" \
	KUEUE_BIN=$(PROJECT_DIR)/bin \
	ENVTEST_K8S_VERSION=$(ENVTEST_K8S_VERSION) \
	$(GINKGO) $(GINKGO_ARGS) -procs=$(INTEGRATION_NPROCS) --race --junit-report=junit-integration.xml --output-dir=$(ARTIFACTS) -v $(INTEGRATION_TARGET)

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter
	$(GOLANGCI_LINT) run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	$(GOLANGCI_LINT) run --fix

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) apply --server-side -f -

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -


.PHONY: kubectl-kjob
kubectl-kjob: embeded-manifest ## Build kubectl-kjob binary file to bin directory.
	CGO_ENABLED=$(CGO_ENABLED) $(GO_BUILD_ENV) $(GO_CMD) build -ldflags="$(LD_FLAGS)" -o bin/kubectl-kjob cmd/kjobctl/main.go

.PHONY: kjobctl-docs
kjobctl-docs: ## Build kjobctl-docs binary file to bin directory.
	$(GO_BUILD_ENV) $(GO_CMD) build -ldflags="$(LD_FLAGS)" -o $(PROJECT_DIR)/bin/kjobctl-docs ./cmd/kjobctl-docs/main.go

.PHONY: generate-kjobctl-docs
generate-kjobctl-docs: kjobctl-docs ## Generate kjobctl docs.
	rm -Rf $(PROJECT_DIR)/docs/commands/*
	$(PROJECT_DIR)/bin/kjobctl-docs \
		$(PROJECT_DIR)/cmd/kjobctl-docs/templates \
		$(PROJECT_DIR)/docs/commands

##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Versions

# Use go.mod go version as source.
KUSTOMIZE_VERSION ?= $(shell cd $(TOOLS_DIR) && $(GO_CMD) list -m -f '{{.Version}}' sigs.k8s.io/kustomize/kustomize/v5)
CONTROLLER_GEN_VERSION ?= $(shell cd $(TOOLS_DIR) && $(GO_CMD) list -m -f '{{.Version}}' sigs.k8s.io/controller-tools)
ENVTEST_VERSION ?= $(shell cd $(TOOLS_DIR) && $(GO_CMD) list -m -f '{{.Version}}' sigs.k8s.io/controller-runtime/tools/setup-envtest)
GOLANGCI_LINT_VERSION ?= $(shell cd $(TOOLS_DIR) && $(GO_CMD) list -m -f '{{.Version}}' github.com/golangci/golangci-lint)
GOTESTSUM_VERSION ?= $(shell cd $(TOOLS_DIR) && $(GO_CMD) list -m -f '{{.Version}}' gotest.tools/gotestsum)
GINKGO_VERSION ?= $(shell cd $(TOOLS_DIR) && $(GO_CMD) list -m -f '{{.Version}}' github.com/onsi/ginkgo/v2)

## Tool Binaries
KUBECTL ?= kubectl
KUSTOMIZE ?= $(LOCALBIN)/kustomize-$(KUSTOMIZE_VERSION)
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen-$(CONTROLLER_GEN_VERSION)
ENVTEST ?= $(LOCALBIN)/setup-envtest-$(ENVTEST_VERSION)
GOLANGCI_LINT ?= $(LOCALBIN)/golangci-lint-$(GOLANGCI_LINT_VERSION)
GOTESTSUM ?= $(LOCALBIN)/gotestsum-$(GOTESTSUM_VERSION)
GINKGO ?= $(LOCALBIN)/ginkgo-$(GINKGO_VERSION)

.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_GEN_VERSION))

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))

.PHONY: gotestsum
gotestsum: ## Download gotestsum locally if necessary.
	$(call go-install-tool,$(GOTESTSUM),gotest.tools/gotestsum,$(GOTESTSUM_VERSION))

.PHONY: ginkgo
ginkgo: ## Download ginkgo locally if necessary.
	$(call go-install-tool,$(GINKGO),github.com/onsi/ginkgo/v2/ginkgo,$(GINKGO_VERSION))

##@ External CRDs

RAY_ROOT = $(shell $(GO_CMD) list -m -mod=readonly -f "{{.Dir}}" github.com/ray-project/kuberay/ray-operator)
.PHONY: ray-operator-crd
ray-operator-crd: ## Copy the CRDs from the ray-operator to the dep-crds directory.
	mkdir -p $(EXTERNAL_CRDS_DIR)/ray-operator/
	cp -f $(RAY_ROOT)/config/crd/bases/* $(EXTERNAL_CRDS_DIR)/ray-operator/

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary (ideally with version)
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f $(1) ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
GOBIN=$(LOCALBIN) go install $${package} ;\
mv "$$(echo "$(1)" | sed "s/-$(3)$$//")" $(1) ;\
} ;\
ln -sf $(1) "$$(echo "$(1)" | sed "s/-$(3)$$//")"
endef
