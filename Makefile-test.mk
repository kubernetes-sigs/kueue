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

ifeq ($(shell uname),Darwin)
    GOFLAGS ?= -ldflags=-linkmode=internal
endif

GO_TEST_FLAGS ?= -race

# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION ?= 1.33

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
INTEGRATION_OUTPUT_OPTIONS ?= --output-interceptor-mode=none 

# Folder where the e2e tests are located.
E2E_TARGET ?= ./test/e2e/...
E2E_K8S_VERSIONS ?= 1.31.9 1.32.5 1.33.1
E2E_K8S_VERSION ?= 1.33
E2E_K8S_FULL_VERSION ?= $(filter $(E2E_K8S_VERSION).%,$(E2E_K8S_VERSIONS))
# Default to E2E_K8S_VERSION.0 if no match is found
E2E_K8S_FULL_VERSION := $(or $(E2E_K8S_FULL_VERSION),$(E2E_K8S_VERSION).0)
E2E_KIND_VERSION ?= kindest/node:v$(E2E_K8S_FULL_VERSION)
E2E_RUN_ONLY_ENV ?= false
E2E_USE_HELM ?= false

# For local testing, we should allow user to use different kind cluster name
# Default will delete default kind cluster
KIND_CLUSTER_NAME ?= kind

##@ Tests

# Periodic builds are tested with full ray image
ifeq ($(JOB_TYPE),periodic)
    export USE_RAY_FOR_TESTS="ray"
else
    export USE_RAY_FOR_TESTS="raymini"
endif

.PHONY: test
test: gotestsum ## Run tests.
	$(GOTESTSUM) --junitfile $(ARTIFACTS)/junit.xml -- $(GOFLAGS) $(GO_TEST_FLAGS) $(shell $(GO_CMD) list ./... | grep -v '/test/') -coverpkg=./... -coverprofile $(ARTIFACTS)/cover.out

.PHONY: test-integration
test-integration: gomod-download envtest ginkgo dep-crds kueuectl ginkgo-top ## Run integration tests for all singlecluster suites.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" \
	PROJECT_DIR=$(PROJECT_DIR)/ \
	KUEUE_BIN=$(BIN_DIR) \
	ENVTEST_K8S_VERSION=$(ENVTEST_K8S_VERSION) \
	TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) API_LOG_LEVEL=$(INTEGRATION_API_LOG_LEVEL) \
	$(GINKGO) $(INTEGRATION_FILTERS) $(GINKGO_ARGS) $(GOFLAGS) -procs=$(INTEGRATION_NPROCS) --race --junit-report=junit.xml --json-report=integration.json $(INTEGRATION_OUTPUT_OPTIONS) --output-dir=$(ARTIFACTS) -v $(INTEGRATION_TARGET)
	$(BIN_DIR)/ginkgo-top -i $(ARTIFACTS)/integration.json > $(ARTIFACTS)/integration-top.yaml

.PHONY: test-integration-baseline
test-integration-baseline: INTEGRATION_FILTERS= --label-filter="!slow && !redundant"
test-integration-baseline: test-integration ## Run baseline integration tests for singlecluster suites.

.PHONY: test-integration-extended
test-integration-extended: INTEGRATION_FILTERS= --label-filter="slow || redundant"
test-integration-extended: test-integration ## Run extended integration tests for singlecluster suites.

.PHONY: test-multikueue-integration
test-multikueue-integration: gomod-download envtest ginkgo dep-crds ginkgo-top ## Run integration tests for MultiKueue suite.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" \
	PROJECT_DIR=$(PROJECT_DIR)/ \
	KUEUE_BIN=$(BIN_DIR) \
	ENVTEST_K8S_VERSION=$(ENVTEST_K8S_VERSION) \
	TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) API_LOG_LEVEL=$(INTEGRATION_API_LOG_LEVEL) \
	$(GINKGO) $(INTEGRATION_FILTERS) $(GINKGO_ARGS) $(GOFLAGS) -procs=$(INTEGRATION_NPROCS_MULTIKUEUE) --race --junit-report=multikueue-junit.xml --json-report=multikueue-integration.json $(INTEGRATION_OUTPUT_OPTIONS) --output-dir=$(ARTIFACTS) -v $(INTEGRATION_TARGET_MULTIKUEUE)
	$(BIN_DIR)/ginkgo-top -i $(ARTIFACTS)/multikueue-integration.json > $(ARTIFACTS)/multikueue-integration-top.yaml

CREATE_KIND_CLUSTER ?= true


.PHONY: test-e2e
test-e2e: setup-e2e-env kueuectl kind-ray-project-mini-image-build run-test-e2e-singlecluster-$(E2E_KIND_VERSION:kindest/node:v%=%)

.PHONY: test-e2e-helm
test-e2e-helm: E2E_USE_HELM=true
test-e2e-helm: test-e2e

.PHONY: test-multikueue-e2e
test-multikueue-e2e: setup-e2e-env kind-ray-project-mini-image-build run-test-multikueue-e2e-$(E2E_KIND_VERSION:kindest/node:v%=%)

.PHONY: test-multikueue-e2e-helm
test-multikueue-e2e-helm: E2E_USE_HELM=true
test-multikueue-e2e-helm: test-multikueue-e2e

.PHONY: test-tas-e2e
test-tas-e2e: setup-e2e-env kind-ray-project-mini-image-build run-test-tas-e2e-$(E2E_KIND_VERSION:kindest/node:v%=%)

.PHONY: test-tas-e2e-helm
test-tas-e2e-helm: E2E_USE_HELM=true
test-tas-e2e-helm: test-tas-e2e

.PHONY: test-e2e-customconfigs
test-e2e-customconfigs: setup-e2e-env run-test-e2e-customconfigs-$(E2E_KIND_VERSION:kindest/node:v%=%)

.PHONY: test-e2e-customconfigs-helm
test-e2e-customconfigs-helm: E2E_USE_HELM=true
test-e2e-customconfigs-helm: test-e2e-customconfigs

.PHONY: test-e2e-certmanager
test-e2e-certmanager: setup-e2e-env run-test-e2e-certmanager-$(E2E_KIND_VERSION:kindest/node:v%=%)

run-test-e2e-singlecluster-%: K8S_VERSION = $(@:run-test-e2e-singlecluster-%=%)
run-test-e2e-singlecluster-%:
	@echo Running e2e for k8s ${K8S_VERSION}
	E2E_KIND_VERSION="kindest/node:v$(K8S_VERSION)" KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) CREATE_KIND_CLUSTER=$(CREATE_KIND_CLUSTER) \
		ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(GINKGO_ARGS)" \
		APPWRAPPER_VERSION=$(APPWRAPPER_VERSION) \
		JOBSET_VERSION=$(JOBSET_VERSION) \
		KUBEFLOW_VERSION=$(KUBEFLOW_VERSION) \
		LEADERWORKERSET_VERSION=$(LEADERWORKERSET_VERSION) \
		KUBERAY_VERSION=$(KUBERAY_VERSION) RAY_VERSION=$(RAY_VERSION) RAYMINI_VERSION=$(RAYMINI_VERSION) USE_RAY_FOR_TESTS=$(USE_RAY_FOR_TESTS) \
		KIND_CLUSTER_FILE="kind-cluster.yaml" E2E_TARGET_FOLDER="singlecluster" \
		TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) \
		E2E_RUN_ONLY_ENV=$(E2E_RUN_ONLY_ENV) \
		E2E_USE_HELM=$(E2E_USE_HELM) \
		./hack/e2e-test.sh

run-test-multikueue-e2e-%: K8S_VERSION = $(@:run-test-multikueue-e2e-%=%)
run-test-multikueue-e2e-%:
	@echo Running multikueue e2e for k8s ${K8S_VERSION}
	E2E_KIND_VERSION="kindest/node:v$(K8S_VERSION)" KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) CREATE_KIND_CLUSTER=$(CREATE_KIND_CLUSTER) \
		ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(GINKGO_ARGS)" \
		APPWRAPPER_VERSION=$(APPWRAPPER_VERSION) \
		JOBSET_VERSION=$(JOBSET_VERSION) KUBEFLOW_VERSION=$(KUBEFLOW_VERSION) \
		KUBEFLOW_MPI_VERSION=$(KUBEFLOW_MPI_VERSION) \
		KUBERAY_VERSION=$(KUBERAY_VERSION) RAY_VERSION=$(RAY_VERSION) RAYMINI_VERSION=$(RAYMINI_VERSION) USE_RAY_FOR_TESTS=$(USE_RAY_FOR_TESTS) \
		TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) \
		E2E_RUN_ONLY_ENV=$(E2E_RUN_ONLY_ENV) \
		E2E_USE_HELM=$(E2E_USE_HELM) \
		./hack/multikueue-e2e-test.sh

run-test-tas-e2e-%: K8S_VERSION = $(@:run-test-tas-e2e-%=%)
run-test-tas-e2e-%:
	@echo Running tas e2e for k8s ${K8S_VERSION}
	E2E_KIND_VERSION="kindest/node:v$(K8S_VERSION)" KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) CREATE_KIND_CLUSTER=$(CREATE_KIND_CLUSTER) \
		ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(GINKGO_ARGS)" \
		JOBSET_VERSION=$(JOBSET_VERSION) KUBEFLOW_VERSION=$(KUBEFLOW_VERSION) KUBEFLOW_MPI_VERSION=$(KUBEFLOW_MPI_VERSION) \
		APPWRAPPER_VERSION=$(APPWRAPPER_VERSION) \
		LEADERWORKERSET_VERSION=$(LEADERWORKERSET_VERSION) \
		KUBERAY_VERSION=$(KUBERAY_VERSION) RAY_VERSION=$(RAY_VERSION) RAYMINI_VERSION=$(RAYMINI_VERSION) USE_RAY_FOR_TESTS=$(USE_RAY_FOR_TESTS) \
		KIND_CLUSTER_FILE="tas-kind-cluster.yaml" E2E_TARGET_FOLDER="tas" \
		TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) \
		E2E_RUN_ONLY_ENV=$(E2E_RUN_ONLY_ENV) \
		E2E_USE_HELM=$(E2E_USE_HELM) \
		./hack/e2e-test.sh

run-test-e2e-customconfigs-%: K8S_VERSION = $(@:run-test-e2e-customconfigs-%=%)
run-test-e2e-customconfigs-%:
	@echo Running customconfigs e2e for k8s ${K8S_VERSION}
	E2E_KIND_VERSION="kindest/node:v$(K8S_VERSION)" KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) CREATE_KIND_CLUSTER=$(CREATE_KIND_CLUSTER) \
		ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(GINKGO_ARGS)" \
		KIND_CLUSTER_FILE="kind-cluster.yaml" E2E_TARGET_FOLDER="customconfigs" \
		JOBSET_VERSION=$(JOBSET_VERSION) APPWRAPPER_VERSION=$(APPWRAPPER_VERSION) \
		LEADERWORKERSET_VERSION=$(LEADERWORKERSET_VERSION) \
		TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) \
		E2E_RUN_ONLY_ENV=$(E2E_RUN_ONLY_ENV) \
		E2E_USE_HELM=$(E2E_USE_HELM) \
		./hack/e2e-test.sh

run-test-e2e-certmanager-%: K8S_VERSION = $(@:run-test-e2e-certmanager-%=%)
run-test-e2e-certmanager-%:
	@echo Running certmanager e2e for k8s ${K8S_VERSION}
	E2E_KIND_VERSION="kindest/node:v$(K8S_VERSION)" KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) CREATE_KIND_CLUSTER=$(CREATE_KIND_CLUSTER) \
		ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(GINKGO_ARGS)" \
		KIND_CLUSTER_FILE="kind-cluster.yaml" E2E_TARGET_FOLDER="certmanager" \
		CERTMANAGER_VERSION=$(CERTMANAGER_VERSION) \
		TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) \
		E2E_RUN_ONLY_ENV=$(E2E_RUN_ONLY_ENV) \
		E2E_USE_HELM=$(E2E_USE_HELM) \
		./hack/e2e-test.sh

SCALABILITY_RUNNER := $(BIN_DIR)/performance-scheduler-runner
.PHONY: performance-scheduler-runner
performance-scheduler-runner:
	$(GO_BUILD_ENV) $(GO_CMD) build -ldflags="$(LD_FLAGS)" -o $(SCALABILITY_RUNNER) test/performance/scheduler/runner/main.go

MINIMALKUEUE_RUNNER := $(BIN_DIR)/minimalkueue
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
	cd $(TOOLS_DIR) && go mod download && \
	$(GO_BUILD_ENV) $(GO_CMD) build -ldflags="$(LD_FLAGS)" -o $(BIN_DIR)/ginkgo-top ./ginkgo-top

.PHONY: setup-e2e-env
setup-e2e-env: kustomize yq gomod-download dep-crds kind helm ginkgo ginkgo-top ## Setup environment for e2e tests without running tests.
	@echo "Setting up environment for e2e tests"

.PHONY: test-e2e-kueueviz-local
test-e2e-kueueviz-local: setup-e2e-env ## Run end-to-end tests for kueueviz without running kueue tests.
	CYPRESS_SCREENSHOTS_FOLDER=$(ARTIFACTS)/cypress/screenshots CYPRESS_VIDEOS_FOLDER=$(ARTIFACTS)/cypress/videos \
	ARTIFACTS=$(ARTIFACTS) KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) PROJECT_DIR=$(PROJECT_DIR)/ \
	KIND_CLUSTER_FILE="kind-cluster.yaml" IMAGE_TAG=$(IMAGE_TAG) ${PROJECT_DIR}/hack/e2e-kueueviz-local.sh

.PHONY: test-e2e-kueueviz
test-e2e-kueueviz: setup-e2e-env ## Run end-to-end tests for kueueviz without running kueue tests.
	@echo Starting kueueviz end to end test in containers
	CYPRESS_SCREENSHOTS_FOLDER=$(ARTIFACTS)/cypress/screenshots CYPRESS_VIDEOS_FOLDER=$(ARTIFACTS)/cypress/videos \
	ARTIFACTS=$(ARTIFACTS) KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) PROJECT_DIR=$(PROJECT_DIR)/ \
	KIND_CLUSTER_FILE="kind-cluster.yaml" IMAGE_TAG=$(IMAGE_TAG) \
	${PROJECT_DIR}/hack/e2e-kueueviz-backend.sh
