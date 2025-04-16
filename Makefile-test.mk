# Copyright 2024 The Kubernetes Authors.
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

PROJECT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))
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

# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION ?= 1.32

TEST_LOG_LEVEL ?= -3

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

# Folder where the e2e tests are located.
E2E_TARGET ?= ./test/e2e/...
E2E_K8S_VERSIONS ?= 1.30.10 1.31.6 1.32.3
E2E_K8S_VERSION ?= 1.32
E2E_K8S_FULL_VERSION ?= $(filter $(E2E_K8S_VERSION).%,$(E2E_K8S_VERSIONS))
# Default to E2E_K8S_VERSION.0 if no match is found
E2E_K8S_FULL_VERSION := $(or $(E2E_K8S_FULL_VERSION),$(E2E_K8S_VERSION).0)
E2E_KIND_VERSION ?= kindest/node:v$(E2E_K8S_FULL_VERSION)

# For local testing, we should allow user to use different kind cluster name
# Default will delete default kind cluster
KIND_CLUSTER_NAME ?= kind

GIT_TAG ?= $(shell git describe --tags --dirty --always)
STAGING_IMAGE_REGISTRY := us-central1-docker.pkg.dev/k8s-staging-images
IMAGE_REGISTRY ?= $(STAGING_IMAGE_REGISTRY)/kueue
IMAGE_NAME := kueue
IMAGE_REPO ?= $(IMAGE_REGISTRY)/$(IMAGE_NAME)
IMAGE_TAG ?= $(IMAGE_REPO):$(GIT_TAG)
CYPRESS_IMAGE_NAME ?= cypress/base:22.14.0

# Versions for external controllers
APPWRAPPER_VERSION = $(shell $(GO_CMD) list -m -f "{{.Version}}" github.com/project-codeflare/appwrapper)
JOBSET_VERSION = $(shell $(GO_CMD) list -m -f "{{.Version}}" sigs.k8s.io/jobset)
KUBEFLOW_VERSION = $(shell $(GO_CMD) list -m -f "{{.Version}}" github.com/kubeflow/training-operator)
KUBEFLOW_MPI_VERSION = $(shell $(GO_CMD) list -m -f "{{.Version}}" github.com/kubeflow/mpi-operator)
KUBERAY_VERSION = $(shell $(GO_CMD) list -m -f "{{.Version}}" github.com/ray-project/kuberay/ray-operator)
LEADERWORKERSET_VERSION = $(shell $(GO_CMD) list -m -f "{{.Version}}" sigs.k8s.io/lws)
CERTMANAGER_VERSION=$(shell $(GO_CMD) list -m -f "{{.Version}}" github.com/cert-manager/cert-manager)

##@ Tests

.PHONY: test
test: gotestsum ## Run tests.
	$(GOTESTSUM) --junitfile $(ARTIFACTS)/junit.xml -- $(GOFLAGS) $(GO_TEST_FLAGS) $(shell $(GO_CMD) list ./... | grep -v '/test/') -coverpkg=./... -coverprofile $(ARTIFACTS)/cover.out

.PHONY: test-integration
test-integration: gomod-download envtest ginkgo dep-crds kueuectl ginkgo-top ## Run integration tests for all singlecluster suites.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" \
	PROJECT_DIR=$(PROJECT_DIR)/ \
	KUEUE_BIN=$(PROJECT_DIR)/bin \
	ENVTEST_K8S_VERSION=$(ENVTEST_K8S_VERSION) \
	TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) API_LOG_LEVEL=$(INTEGRATION_API_LOG_LEVEL) \
	$(GINKGO) $(INTEGRATION_FILTERS) $(GINKGO_ARGS) $(GOFLAGS) -procs=$(INTEGRATION_NPROCS) --race --junit-report=junit.xml --json-report=integration.json --output-dir=$(ARTIFACTS) -v $(INTEGRATION_TARGET)
	$(PROJECT_DIR)/bin/ginkgo-top -i $(ARTIFACTS)/integration.json > $(ARTIFACTS)/integration-top.yaml

.PHONY: test-multikueue-integration
test-multikueue-integration: gomod-download envtest ginkgo dep-crds kueuectl ginkgo-top ## Run integration tests for Multikueue suite.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" \
	PROJECT_DIR=$(PROJECT_DIR)/ \
	KUEUE_BIN=$(PROJECT_DIR)/bin \
	ENVTEST_K8S_VERSION=$(ENVTEST_K8S_VERSION) \
	TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) API_LOG_LEVEL=$(INTEGRATION_API_LOG_LEVEL) \
	$(GINKGO) $(INTEGRATION_FILTERS) $(GINKGO_ARGS) $(GOFLAGS) -procs=$(INTEGRATION_NPROCS_MULTIKUEUE) --race --junit-report=multikueue-junit.xml --json-report=multikueue-integration.json --output-dir=$(ARTIFACTS) -v $(INTEGRATION_TARGET_MULTIKUEUE)
	$(PROJECT_DIR)/bin/ginkgo-top -i $(ARTIFACTS)/multikueue-integration.json > $(ARTIFACTS)/multikueue-integration-top.yaml

CREATE_KIND_CLUSTER ?= true


.PHONY: test-e2e
test-e2e: kustomize ginkgo yq gomod-download dep-crds kueuectl ginkgo-top run-test-e2e-singlecluster-$(E2E_KIND_VERSION:kindest/node:v%=%)

.PHONY: test-multikueue-e2e
test-multikueue-e2e: kustomize ginkgo yq gomod-download dep-crds ginkgo-top run-test-multikueue-e2e-$(E2E_KIND_VERSION:kindest/node:v%=%)

.PHONY: test-tas-e2e
test-tas-e2e: kustomize ginkgo yq gomod-download dep-crds kueuectl ginkgo-top run-test-tas-e2e-$(E2E_KIND_VERSION:kindest/node:v%=%)

.PHONY: test-e2e-customconfigs
test-e2e-customconfigs: kustomize ginkgo yq gomod-download dep-crds kueuectl ginkgo-top run-test-e2e-customconfigs-$(E2E_KIND_VERSION:kindest/node:v%=%)

.PHONY: test-e2e-certmanager
test-e2e-certmanager: kustomize ginkgo yq gomod-download dep-crds kueuectl ginkgo-top run-test-e2e-certmanager-$(E2E_KIND_VERSION:kindest/node:v%=%)

FORCE:

run-test-e2e-singlecluster-%: K8S_VERSION = $(@:run-test-e2e-singlecluster-%=%)
run-test-e2e-singlecluster-%: FORCE
	@echo Running e2e for k8s ${K8S_VERSION}
	E2E_KIND_VERSION="kindest/node:v$(K8S_VERSION)" KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) CREATE_KIND_CLUSTER=$(CREATE_KIND_CLUSTER) \
		ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(GINKGO_ARGS)" \
		APPWRAPPER_VERSION=$(APPWRAPPER_VERSION) \
		JOBSET_VERSION=$(JOBSET_VERSION) \
		LEADERWORKERSET_VERSION=$(LEADERWORKERSET_VERSION) \
		KIND_CLUSTER_FILE="kind-cluster.yaml" E2E_TARGET_FOLDER="singlecluster" \
		TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) \
		./hack/e2e-test.sh
	$(PROJECT_DIR)/bin/ginkgo-top -i $(ARTIFACTS)/$@/e2e.json > $(ARTIFACTS)/$@/e2e-top.yaml

run-test-multikueue-e2e-%: K8S_VERSION = $(@:run-test-multikueue-e2e-%=%)
run-test-multikueue-e2e-%: FORCE
	@echo Running multikueue e2e for k8s ${K8S_VERSION}
	E2E_KIND_VERSION="kindest/node:v$(K8S_VERSION)" KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) CREATE_KIND_CLUSTER=$(CREATE_KIND_CLUSTER) \
		ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(GINKGO_ARGS)" \
		APPWRAPPER_VERSION=$(APPWRAPPER_VERSION) \
		JOBSET_VERSION=$(JOBSET_VERSION) KUBEFLOW_VERSION=$(KUBEFLOW_VERSION) \
		KUBEFLOW_MPI_VERSION=$(KUBEFLOW_MPI_VERSION) KUBERAY_VERSION=$(KUBERAY_VERSION) \
		TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) \
		./hack/multikueue-e2e-test.sh
	$(PROJECT_DIR)/bin/ginkgo-top -i $(ARTIFACTS)/$@/e2e.json > $(ARTIFACTS)/$@/e2e-top.yaml

run-test-tas-e2e-%: K8S_VERSION = $(@:run-test-tas-e2e-%=%)
run-test-tas-e2e-%: FORCE
	@echo Running tas e2e for k8s ${K8S_VERSION}
	E2E_KIND_VERSION="kindest/node:v$(K8S_VERSION)" KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) CREATE_KIND_CLUSTER=$(CREATE_KIND_CLUSTER) \
		ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(GINKGO_ARGS)" \
		JOBSET_VERSION=$(JOBSET_VERSION) KUBEFLOW_VERSION=$(KUBEFLOW_VERSION) KUBEFLOW_MPI_VERSION=$(KUBEFLOW_MPI_VERSION) \
		APPWRAPPER_VERSION=$(APPWRAPPER_VERSION) \
		LEADERWORKERSET_VERSION=$(LEADERWORKERSET_VERSION) \
		KUBERAY_VERSION=$(KUBERAY_VERSION) \
		KIND_CLUSTER_FILE="tas-kind-cluster.yaml" E2E_TARGET_FOLDER="tas" \
		TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) \
		./hack/e2e-test.sh
	$(PROJECT_DIR)/bin/ginkgo-top -i $(ARTIFACTS)/$@/e2e.json > $(ARTIFACTS)/$@/e2e-top.yaml

run-test-e2e-customconfigs-%: K8S_VERSION = $(@:run-test-e2e-customconfigs-%=%)
run-test-e2e-customconfigs-%: FORCE
	@echo Running customconfigs e2e for k8s ${K8S_VERSION}
	E2E_KIND_VERSION="kindest/node:v$(K8S_VERSION)" KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) CREATE_KIND_CLUSTER=$(CREATE_KIND_CLUSTER) \
		ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(GINKGO_ARGS)" \
		KIND_CLUSTER_FILE="kind-cluster.yaml" E2E_TARGET_FOLDER="customconfigs" \
		JOBSET_VERSION=$(JOBSET_VERSION) APPWRAPPER_VERSION=$(APPWRAPPER_VERSION) \
		LEADERWORKERSET_VERSION=$(LEADERWORKERSET_VERSION) \
		TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) \
		./hack/e2e-test.sh
	$(PROJECT_DIR)/bin/ginkgo-top -i $(ARTIFACTS)/$@/e2e.json > $(ARTIFACTS)/$@/e2e-top.yaml

run-test-e2e-certmanager-%: K8S_VERSION = $(@:run-test-e2e-certmanager-%=%)
run-test-e2e-certmanager-%: FORCE
	@echo Running certmanager e2e for k8s ${K8S_VERSION}
	E2E_KIND_VERSION="kindest/node:v$(K8S_VERSION)" KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) CREATE_KIND_CLUSTER=$(CREATE_KIND_CLUSTER) \
		ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(GINKGO_ARGS)" \
		KIND_CLUSTER_FILE="kind-cluster.yaml" E2E_TARGET_FOLDER="certmanager" \
		CERTMANAGER_VERSION=$(CERTMANAGER_VERSION) \
		TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) \
		./hack/e2e-test.sh
	$(PROJECT_DIR)/bin/ginkgo-top -i $(ARTIFACTS)/$@/e2e.json > $(ARTIFACTS)/$@/e2e-top.yaml

SCALABILITY_RUNNER := $(PROJECT_DIR)/bin/performance-scheduler-runner
.PHONY: performance-scheduler-runner
performance-scheduler-runner:
	$(GO_BUILD_ENV) $(GO_CMD) build -ldflags="$(LD_FLAGS)" -o $(SCALABILITY_RUNNER) test/performance/scheduler/runner/main.go

MINIMALKUEUE_RUNNER := $(PROJECT_DIR)/bin/minimalkueue
.PHONY: minimalkueue
minimalkueue:
	$(GO_BUILD_ENV) $(GO_CMD) build -ldflags="$(LD_FLAGS)" -o $(MINIMALKUEUE_RUNNER) test/performance/scheduler/minimalkueue/main.go

ifdef SCALABILITY_CPU_PROFILE
SCALABILITY_EXTRA_ARGS += --withCPUProfile=true
endif

ifndef NO_SCALABILITY_KUEUE_LOGS
SCALABILITY_EXTRA_ARGS +=  --withLogs=true --logToFile=true
endif

SCALABILITY_SCRAPE_INTERVAL ?= 5s
ifndef NO_SCALABILITY_SCRAPE
SCALABILITY_SCRAPE_ARGS +=  --metricsScrapeInterval=$(SCALABILITY_SCRAPE_INTERVAL)
endif

ifdef SCALABILITY_SCRAPE_URL
SCALABILITY_SCRAPE_ARGS +=  --metricsScrapeURL=$(SCALABILITY_SCRAPE_URL)
endif

SCALABILITY_GENERATOR_CONFIG ?= $(PROJECT_DIR)/test/performance/scheduler/default_generator_config.yaml

SCALABILITY_RUN_DIR := $(ARTIFACTS)/run-performance-scheduler
.PHONY: run-performance-scheduler
run-performance-scheduler: envtest performance-scheduler-runner minimalkueue
	mkdir -p $(SCALABILITY_RUN_DIR)
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" \
	$(SCALABILITY_RUNNER) \
		--o $(SCALABILITY_RUN_DIR) \
		--crds=$(PROJECT_DIR)/config/components/crd/bases \
		--generatorConfig=$(SCALABILITY_GENERATOR_CONFIG) \
		--minimalKueue=$(MINIMALKUEUE_RUNNER) $(SCALABILITY_EXTRA_ARGS) $(SCALABILITY_SCRAPE_ARGS)

.PHONY: test-performance-scheduler-once
test-performance-scheduler-once: gotestsum run-performance-scheduler
	$(GOTESTSUM) --junitfile $(ARTIFACTS)/junit.xml -- $(GO_TEST_FLAGS) ./test/performance/scheduler/checker  \
		--summary=$(SCALABILITY_RUN_DIR)/summary.yaml \
		--cmdStats=$(SCALABILITY_RUN_DIR)/minimalkueue.stats.yaml \
		--range=$(PROJECT_DIR)/test/performance/scheduler/default_rangespec.yaml

PERFORMANCE_RETRY_COUNT?=2
.PHONY: test-performance-scheduler
test-performance-scheduler:
	ARTIFACTS=$(ARTIFACTS) ./hack/performance-retry.sh $(PERFORMANCE_RETRY_COUNT)

.PHONY: run-performance-scheduler-in-cluster
run-performance-scheduler-in-cluster: envtest performance-scheduler-runner
	mkdir -p $(ARTIFACTS)/run-performance-scheduler-in-cluster
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" \
	$(SCALABILITY_RUNNER) \
		--o $(ARTIFACTS)/run-performance-scheduler-in-cluster \
		--generatorConfig=$(SCALABILITY_GENERATOR_CONFIG) \
		--qps=1000 --burst=2000 --timeout=15m $(SCALABILITY_SCRAPE_ARGS)

.PHONY: ginkgo-top
ginkgo-top:
	cd $(PROJECT_DIR)/hack/internal/tools && \
	go mod download && \
	$(GO_BUILD_ENV) $(GO_CMD) build -ldflags="$(LD_FLAGS)" -o $(PROJECT_DIR)/bin/ginkgo-top ./ginkgo-top

.PHONY: setup-e2e-env
setup-e2e-env: kustomize yq gomod-download dep-crds kueuectl kind ## Setup environment for e2e tests without running tests.
	@echo "Setting up environment for e2e tests"

.PHONY: test-e2e-kueueviz-local
test-e2e-kueueviz-local: setup-e2e-env ## Run end-to-end tests for kueueviz without running kueue tests.
	ARTIFACTS=$(ARTIFACTS) KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) \
	KIND_CLUSTER_FILE="kind-cluster-viz.yaml" ${PROJECT_DIR}/hack/e2e-kueueviz-local.sh

.PHONY: test-e2e-kueueviz
test-e2e-kueueviz: setup-e2e-env ## Run end-to-end tests for kueueviz without running kueue tests.
	@echo Starting kueueviz end to end test in containers
	ARTIFACTS=$(ARTIFACTS) KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) \
	KIND_CLUSTER_FILE="kind-cluster-viz.yaml" \
	CYPRESS_IMAGE_NAME=$(CYPRESS_IMAGE_NAME) ${PROJECT_DIR}/hack/e2e-kueueviz-backend.sh
