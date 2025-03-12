PROJECT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))
EXTERNAL_CRDS_DIR ?= $(PROJECT_DIR)/dep-crds
ARTIFACTS ?= $(PROJECT_DIR)/bin

ifeq ($(shell uname),Darwin)
    GOFLAGS ?= -ldflags=-linkmode=internal
endif

ifeq (,$(shell go env GOBIN))
	GOBIN=$(shell go env GOPATH)/bin
else
	GOBIN=$(shell go env GOBIN)
endif

GO_CMD ?= go
GO_TEST_FLAGS ?= -race
version_pkg = sigs.k8s.io/kueue/pkg/version
LD_FLAGS += -X '$(version_pkg).GitVersion=$(GIT_TAG)'
LD_FLAGS += -X '$(version_pkg).GitCommit=$(shell git rev-parse HEAD)'

# test flags

# ENVTEST_OCP_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_OCP_K8S_VERSION ?= 1.32

# Number of processes to use during integration tests to run specs within a
# suite in parallel. Suites still run sequentially. User may set this value to 1
# to run without parallelism.
INTEGRATION_NPROCS ?= 4
INTEGRATION_NPROCS_MULTIKUEUE ?= 3
# Folder where the integration tests are located.
INTEGRATION_TARGET ?= ./test/integration/singlecluster/...
INTEGRATION_TARGET_MULTIKUEUE ?= ./test/integration/multikueue/...
# Verbosity level for apiserver logging.
# The logging is disabled if 0.
INTEGRATION_API_LOG_LEVEL ?= 0
# Integration filters
INTEGRATION_RUN_ALL?=true
ifneq ($(INTEGRATION_RUN_ALL),true) 
	INTEGRATION_FILTERS= --label-filter="!slow && !redundant"
endif

# Use go.mod go version as source.
KUSTOMIZE_OCP_VERSION ?= $(shell $(GO_CMD) list -m -mod=mod -f '{{.Version}}' sigs.k8s.io/kustomize/kustomize/v5)
GINKGO_OCP_VERSION ?= $(shell $(GO_CMD) list -m -mod=mod -f '{{.Version}}' github.com/onsi/ginkgo/v2)
YQ_OCP_VERSION ?= $(shell $(GO_CMD) list -m -mod=mod -f '{{.Version}}' github.com/mikefarah/yq/v4)
ENVTEST_OCP_VERSION ?= $(shell $(GO_CMD) list -m -mod=mod -f '{{.Version}}' sigs.k8s.io/controller-runtime/tools/setup-envtest)

KUSTOMIZE = $(PROJECT_DIR)/bin/kustomize
.PHONY: kustomize-ocp
kustomize-ocp: ## Download kustomize locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install -mod=mod sigs.k8s.io/kustomize/kustomize/v5@$(KUSTOMIZE_OCP_VERSION)

GINKGO = $(PROJECT_DIR)/bin/ginkgo
.PHONY: ginkgo-ocp
ginkgo-ocp: ## Download ginkgo locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install -mod=mod github.com/onsi/ginkgo/v2/ginkgo@$(GINKGO_OCP_VERSION)

ENVTEST = $(PROJECT_DIR)/bin/setup-envtest
.PHONY: envtest
envtest-ocp: ## Download envtest-setup locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install -mod=mod sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_OCP_VERSION)

YQ = $(PROJECT_DIR)/bin/yq
.PHONY: yq-ocp
yq-ocp: ## Download yq locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install -mod=mod github.com/mikefarah/yq/v4@$(YQ_OCP_VERSION)

.PHONY: test-ocp
test-ocp: ## Run tests.
# Configs were filtered out due to a failure
# Running this in openshift CI we are hitting failures in the unit tests
# due to how kueue grabs the namespace for default configs
# Kueue will read the serviceaccount for the pod
# and this seems to break in OCP Prow
# We added "grep -v 'config'" to filter out those unit tests
	${GO_CMD} run ./vendor/gotest.tools/gotestsum --junitfile $(ARTIFACTS)/junit.xml -- $(GOFLAGS) $(GO_TEST_FLAGS) $(shell $(GO_CMD) list ./... | grep -v '/test/' | grep -v 'config')

.PHONY: test-integration-ocp
.PHONY: test-integration-ocp
test-integration-ocp: envtest-ocp ginkgo-ocp kueuectl-ocp ginkgo-top-ocp ## Run integration tests for all singlecluster suites.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_OCP_K8S_VERSION) --bin-dir $(PROJECT_DIR)/bin -p path)" \
	PROJECT_DIR=$(PROJECT_DIR)/ \
	KUEUE_BIN=$(PROJECT_DIR)/bin \
	ENVTEST_K8S_VERSION=$(ENVTEST_OCP_K8S_VERSION) \
	API_LOG_LEVEL=$(INTEGRATION_API_LOG_LEVEL) \
	$(GINKGO) $(INTEGRATION_FILTERS) $(GINKGO_ARGS) -procs=$(INTEGRATION_NPROCS) --race --junit-report=junit.xml --json-report=integration.json --output-dir=$(ARTIFACTS) -v $(INTEGRATION_TARGET)
	$(PROJECT_DIR)/bin/ginkgo-top -i $(ARTIFACTS)/integration.json > $(ARTIFACTS)/integration-top.yaml

.PHONY: test-e2e-ocp
test-e2e-ocp: kustomize-ocp ginkgo-ocp yq-ocp kueuectl-ocp ginkgo-top-ocp run-test-e2e-ocp-singlecluster
run-test-e2e-ocp-singlecluster:
	@echo "Running e2e tests on OpenShift cluster ($(shell oc whoami --show-server))"
	ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(GINKGO_ARGS)" \
	E2E_TARGET_FOLDER="singlecluster" \
	./hack/e2e-test-ocp.sh
	$(PROJECT_DIR)/bin/ginkgo-top -i $(ARTIFACTS)/$@/e2e.json > $(ARTIFACTS)/$@/e2e-top.yaml

.PHONY: ginkgo-top-ocp
ginkgo-top-ocp:
	$(GO_BUILD_ENV) $(GO_CMD) build -ldflags="$(LD_FLAGS)" -o $(PROJECT_DIR)/bin/ginkgo-top ./pkg/openshift/ginkgo-top

.PHONY: kueuectl-ocp
kueuectl-ocp:
	CGO_ENABLED=$(CGO_ENABLED) $(GO_BUILD_ENV) $(GO_CMD) build -ldflags="$(LD_FLAGS)" -o $(PROJECT_DIR)/bin/kubectl-kueue cmd/kueuectl/main.go

##@ External CRDs

MPI_ROOT = $(shell $(GO_CMD) list -m -mod=mod -f "{{.Dir}}" github.com/kubeflow/mpi-operator)
.PHONY: mpi-operator-crd-ocp
mpi-operator-crd-ocp: ## Copy the CRDs from the mpi-operator to the dep-crds directory.
	mkdir -p $(EXTERNAL_CRDS_DIR)/mpi-operator/
	cp -f $(MPI_ROOT)/manifests/base/* $(EXTERNAL_CRDS_DIR)/mpi-operator/

KF_TRAINING_ROOT = $(shell $(GO_CMD) list -m -mod=mod -f "{{.Dir}}" github.com/kubeflow/training-operator)
.PHONY: kf-training-operator-crd-ocp
kf-training-operator-crd-ocp: ## Copy the CRDs from the training-operator to the dep-crds directory.
	## Removing kubeflow.org_mpijobs.yaml is required as the version of MPIJob is conflicting between training-operator and mpi-operator - in integration tests.
	mkdir -p $(EXTERNAL_CRDS_DIR)/training-operator-crds/
	find $(KF_TRAINING_ROOT)/manifests/base/crds/* -type f -not -name "kubeflow.org_mpijobs.yaml" -exec cp -pf {} $(EXTERNAL_CRDS_DIR)/training-operator-crds/ \;

.PHONY: kf-training-operator-manifests-ocp
kf-training-operator-manifests-ocp: ## Copy whole manifests folder from the training-operator to the dep-crds directory.
	## Full version of the manifest is required for e2e multikueue tests.
	if [ -d "$(EXTERNAL_CRDS_DIR)/training-operator" ]; then \
		chmod -R u+w "$(EXTERNAL_CRDS_DIR)/training-operator" && \
		rm -rf "$(EXTERNAL_CRDS_DIR)/training-operator"; \
	fi
	mkdir -p "$(EXTERNAL_CRDS_DIR)/training-operator"
	cp -rf "$(KF_TRAINING_ROOT)/manifests" "$(EXTERNAL_CRDS_DIR)/training-operator"

RAY_ROOT = $(shell $(GO_CMD) list -m -mod=mod -f "{{.Dir}}" github.com/ray-project/kuberay/ray-operator)
.PHONY: ray-operator-crd-ocp
ray-operator-crd-ocp: ## Copy the CRDs from the ray-operator to the dep-crds directory.
	mkdir -p $(EXTERNAL_CRDS_DIR)/ray-operator-crds/
	cp -f $(RAY_ROOT)/config/crd/bases/* $(EXTERNAL_CRDS_DIR)/ray-operator-crds/

.PHONY: ray-operator-manifests-ocp
ray-operator-manifests-ocp: ## Copy the whole manifests content from the ray-operator to the dep-crds directory.
	## Full version of the manifest is required for e2e multikueue tests.
	if [ -d "$(EXTERNAL_CRDS_DIR)/ray-operator" ]; then \
		chmod -R u+w "$(EXTERNAL_CRDS_DIR)/ray-operator" && \
		rm -rf "$(EXTERNAL_CRDS_DIR)/ray-operator"; \
	fi
	mkdir -p "$(EXTERNAL_CRDS_DIR)/ray-operator"; \
	cp -rf "$(RAY_ROOT)/config/crd" "$(EXTERNAL_CRDS_DIR)/ray-operator"
	cp -rf "$(RAY_ROOT)/config/default" "$(EXTERNAL_CRDS_DIR)/ray-operator"
	cp -rf "$(RAY_ROOT)/config/rbac" "$(EXTERNAL_CRDS_DIR)/ray-operator"
	cp -rf "$(RAY_ROOT)/config/manager" "$(EXTERNAL_CRDS_DIR)/ray-operator"


JOBSET_ROOT = $(shell $(GO_CMD) list -m -mod=mod -f "{{.Dir}}" sigs.k8s.io/jobset)
.PHONY: jobset-operator-crd-ocp
jobset-operator-crd-ocp: ## Copy the CRDs from the jobset-operator to the dep-crds directory.
	mkdir -p $(EXTERNAL_CRDS_DIR)/jobset-operator/
	cp -f $(JOBSET_ROOT)/config/components/crd/bases/* $(EXTERNAL_CRDS_DIR)/jobset-operator/

CLUSTER_AUTOSCALER_ROOT = $(shell $(GO_CMD) list -m -mod=mod -f "{{.Dir}}" k8s.io/autoscaler/cluster-autoscaler/apis)
.PHONY: cluster-autoscaler-crd-ocp
cluster-autoscaler-crd-ocp: ## Copy the CRDs from the cluster-autoscaler to the dep-crds directory.
	mkdir -p $(EXTERNAL_CRDS_DIR)/cluster-autoscaler/
	cp -f $(CLUSTER_AUTOSCALER_ROOT)/config/crd/* $(EXTERNAL_CRDS_DIR)/cluster-autoscaler/

APPWRAPPER_ROOT = $(shell $(GO_CMD) list -m -mod=mod -f "{{.Dir}}" github.com/project-codeflare/appwrapper)
APPWRAPPER_VERSION = $(shell $(GO_CMD) list -m -mod=mod -f "{{.Version}}" github.com/project-codeflare/appwrapper)
.PHONY: appwrapper-crd-ocp
appwrapper-crd-ocp: ## Copy the CRDs from the appwrapper to the dep-crds directory.
	mkdir -p $(EXTERNAL_CRDS_DIR)/appwrapper-crds/
	cp -f $(APPWRAPPER_ROOT)/config/crd/bases/* $(EXTERNAL_CRDS_DIR)/appwrapper-crds/

.PHONY: appwrapper-manifests-ocp
appwrapper-manifests-ocp: kustomize-ocp ## Copy whole manifests folder from the appwrapper controller to the dep-crds directory.
	## Full version of the manifest required for e2e tests.
	if [ -d "$(EXTERNAL_CRDS_DIR)/appwrapper" ]; then \
		chmod -R u+w "$(EXTERNAL_CRDS_DIR)/appwrapper" && \
		rm -rf "$(EXTERNAL_CRDS_DIR)/appwrapper"; \
	fi
	mkdir -p "$(EXTERNAL_CRDS_DIR)/appwrapper"
	cp -rf "$(APPWRAPPER_ROOT)/config" "$(EXTERNAL_CRDS_DIR)/appwrapper"
	cd "$(EXTERNAL_CRDS_DIR)/appwrapper/config/manager" && chmod u+w kustomization.yaml && $(KUSTOMIZE) edit set image controller=quay.io/ibm/appwrapper:${APPWRAPPER_VERSION} && chmod u-w kustomization.yaml

LEADERWORKERSET_ROOT = $(shell $(GO_CMD) list -m -mod=mod -f "{{.Dir}}" sigs.k8s.io/lws)
.PHONY: leaderworkerset-operator-crd-ocp
leaderworkerset-operator-crd-ocp: ## Copy the CRDs from the leaderworkerset-operator to the dep-crds directory.
	mkdir -p $(EXTERNAL_CRDS_DIR)/leaderworkerset-operator/
	cp -f $(LEADERWORKERSET_ROOT)/config/crd/bases/* $(EXTERNAL_CRDS_DIR)/leaderworkerset-operator/

# Run this to generate new CRDs when the dependencies change.
# Commit dep-crds
.PHONY: dep-crds-ocp
dep-crds-ocp: mpi-operator-crd-ocp kf-training-operator-crd-ocp ray-operator-crd-ocp jobset-operator-crd-ocp leaderworkerset-operator-crd-ocp cluster-autoscaler-crd-ocp appwrapper-crd-ocp appwrapper-manifests-ocp kf-training-operator-manifests-ocp ray-operator-manifests-ocp## Copy the CRDs from the external operators to the dep-crds directory.
	@echo "Copying CRDs from external operators to dep-crds directory"
