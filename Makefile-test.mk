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
ENVTEST_K8S_VERSION ?= 1.31

# Number of processes to use during integration tests to run specs within a
# suite in parallel. Suites still run sequentially. User may set this value to 1
# to run without parallelism.
INTEGRATION_NPROCS ?= 4
# Folder where the integration tests are located.
INTEGRATION_TARGET ?= ./test/integration/...
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
E2E_KIND_VERSION ?= kindest/node:v1.31.0
# E2E_K8S_VERSIONS sets the list of k8s versions included in test-e2e-all
E2E_K8S_VERSIONS ?= 1.28.13 1.29.8 1.30.4 1.31.0

# For local testing, we should allow user to use different kind cluster name
# Default will delete default kind cluster
KIND_CLUSTER_NAME ?= kind

GIT_TAG ?= $(shell git describe --tags --dirty --always)
STAGING_IMAGE_REGISTRY := us-central1-docker.pkg.dev/k8s-staging-images
IMAGE_REGISTRY ?= $(STAGING_IMAGE_REGISTRY)/kueue
IMAGE_NAME := kueue
IMAGE_REPO ?= $(IMAGE_REGISTRY)/$(IMAGE_NAME)
IMAGE_TAG ?= $(IMAGE_REPO):$(GIT_TAG)

# JobSet Version
JOBSET_VERSION = $(shell $(GO_CMD) list -m -f "{{.Version}}" sigs.k8s.io/jobset)
KUBEFLOW_VERSION = $(shell $(GO_CMD) list -m -f "{{.Version}}" github.com/kubeflow/training-operator)
KUBEFLOW_MPI_VERSION = $(shell $(GO_CMD) list -m -f "{{.Version}}" github.com/kubeflow/mpi-operator)

##@ Tests

.PHONY: test
test: gotestsum ## Run tests.
	$(GOTESTSUM) --junitfile $(ARTIFACTS)/junit.xml -- $(GOFLAGS) $(GO_TEST_FLAGS) $(shell $(GO_CMD) list ./... | grep -v '/test/') -coverpkg=./... -coverprofile $(ARTIFACTS)/cover.out

.PHONY: test-integration
test-integration: gomod-download envtest ginkgo dep-crds kueuectl ginkgo-top ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" \
	KUEUE_BIN=$(PROJECT_DIR)/bin \
	ENVTEST_K8S_VERSION=$(ENVTEST_K8S_VERSION) \
	API_LOG_LEVEL=$(INTEGRATION_API_LOG_LEVEL) \
	$(GINKGO) $(INTEGRATION_FILTERS) $(GINKGO_ARGS) -procs=$(INTEGRATION_NPROCS) --race --junit-report=junit.xml --json-report=integration.json --output-dir=$(ARTIFACTS) -v $(INTEGRATION_TARGET)
	$(PROJECT_DIR)/bin/ginkgo-top -i $(ARTIFACTS)/integration.json > $(ARTIFACTS)/integration-top.yaml

CREATE_KIND_CLUSTER ?= true
.PHONY: test-e2e
test-e2e: kustomize ginkgo yq gomod-download dep-crds kueuectl ginkgo-top run-test-e2e-$(E2E_KIND_VERSION:kindest/node:v%=%)

.PHONY: test-multikueue-e2e
test-multikueue-e2e: kustomize ginkgo yq gomod-download dep-crds ginkgo-top run-test-multikueue-e2e-$(E2E_KIND_VERSION:kindest/node:v%=%)

.PHONY: test-tas-e2e
test-tas-e2e: kustomize ginkgo yq gomod-download dep-crds kueuectl ginkgo-top run-test-tas-e2e-$(E2E_KIND_VERSION:kindest/node:v%=%)

E2E_TARGETS := $(addprefix run-test-e2e-,${E2E_K8S_VERSIONS})
MULTIKUEUE-E2E_TARGETS := $(addprefix run-test-multikueue-e2e-,${E2E_K8S_VERSIONS})
.PHONY: test-e2e-all
test-e2e-all: ginkgo ginkgo-top $(E2E_TARGETS) $(MULTIKUEUE-E2E_TARGETS)

FORCE:

run-test-e2e-%: K8S_VERSION = $(@:run-test-e2e-%=%)
run-test-e2e-%: FORCE
	@echo Running e2e for k8s ${K8S_VERSION}
	E2E_KIND_VERSION="kindest/node:v$(K8S_VERSION)" KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) CREATE_KIND_CLUSTER=$(CREATE_KIND_CLUSTER) ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(GINKGO_ARGS)" \
	JOBSET_VERSION=$(JOBSET_VERSION) KUBEFLOW_VERSION=$(KUBEFLOW_VERSION) KUBEFLOW_MPI_VERSION=$(KUBEFLOW_MPI_VERSION) KIND_CLUSTER_FILE="kind-cluster.yaml" E2E_TARGET_FOLDER="singlecluster" ./hack/e2e-test.sh
	$(PROJECT_DIR)/bin/ginkgo-top -i $(ARTIFACTS)/$@/e2e.json > $(ARTIFACTS)/$@/e2e-top.yaml

run-test-multikueue-e2e-%: K8S_VERSION = $(@:run-test-multikueue-e2e-%=%)
run-test-multikueue-e2e-%: FORCE
	@echo Running multikueue e2e for k8s ${K8S_VERSION}
	E2E_KIND_VERSION="kindest/node:v$(K8S_VERSION)" KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) CREATE_KIND_CLUSTER=$(CREATE_KIND_CLUSTER) ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(GINKGO_ARGS)" \
	JOBSET_VERSION=$(JOBSET_VERSION) KUBEFLOW_VERSION=$(KUBEFLOW_VERSION) KUBEFLOW_MPI_VERSION=$(KUBEFLOW_MPI_VERSION) ./hack/multikueue-e2e-test.sh
	$(PROJECT_DIR)/bin/ginkgo-top -i $(ARTIFACTS)/$@/e2e.json > $(ARTIFACTS)/$@/e2e-top.yaml

run-test-tas-e2e-%: K8S_VERSION = $(@:run-test-tas-e2e-%=%)
run-test-tas-e2e-%: FORCE
	@echo Running tas e2e for k8s ${K8S_VERSION}
	E2E_KIND_VERSION="kindest/node:v$(K8S_VERSION)" KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) CREATE_KIND_CLUSTER=$(CREATE_KIND_CLUSTER) ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(GINKGO_ARGS)" \
	JOBSET_VERSION=$(JOBSET_VERSION) KUBEFLOW_VERSION=$(KUBEFLOW_VERSION) KUBEFLOW_MPI_VERSION=$(KUBEFLOW_MPI_VERSION) KIND_CLUSTER_FILE="tas-kind-cluster.yaml" E2E_TARGET_FOLDER="tas" ./hack/e2e-test.sh
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
