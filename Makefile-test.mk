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
ENVTEST_K8S_VERSION ?= 1.36

ENVTEST_RETRY_ATTEMPTS ?= 4
ENVTEST_RETRY_DELAY ?= 5
KUBEBUILDER_ASSETS = $(or \
	$(shell $(PROJECT_DIR)/hack/testing/retry.sh --attempts $(ENVTEST_RETRY_ATTEMPTS) --delay $(ENVTEST_RETRY_DELAY) -- $(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path), \
	$(error setup-envtest failed to download binaries. KUBEBUILDER_ASSETS is empty))

TEST_LOG_LEVEL ?= -3

# Number of processes to use during integration tests to run specs within a
# suite in parallel. Suites still run sequentially. User may set this value to 1
# to run without parallelism.
INTEGRATION_NPROCS ?= 4
INTEGRATION_NPROCS_MULTIKUEUE ?= 3
# Folder where the integration tests are located.
ifdef INTEGRATION_TOTAL_SHARDS
INTEGRATION_TARGET := $(shell ./hack/testing/shard-integration-tests.sh $(INTEGRATION_SHARD_INDEX) $(INTEGRATION_TOTAL_SHARDS))
ifeq ($(INTEGRATION_TARGET),)
$(error Aborting execution due to invalid sharding parameters (INTEGRATION_SHARD_INDEX / INTEGRATION_TOTAL_SHARDS))
endif
else
INTEGRATION_TARGET ?= ./test/integration/singlecluster/...
endif
INTEGRATION_TARGET_MULTIKUEUE ?= ./test/integration/multikueue/...
# Verbosity level for apiserver logging.
# The logging is disabled if 0.
INTEGRATION_API_LOG_LEVEL ?= 0

# Folder where the e2e tests are located.
E2E_TARGET ?= ./test/e2e/...
E2E_K8S_VERSIONS ?= 1.34.8 1.35.5 1.36.1
E2E_K8S_VERSION ?= 1.36
E2E_K8S_FULL_VERSION ?= $(filter $(E2E_K8S_VERSION).%,$(E2E_K8S_VERSIONS))
# Default to E2E_K8S_VERSION.0 if no match is found
E2E_K8S_FULL_VERSION := $(or $(E2E_K8S_FULL_VERSION),$(E2E_K8S_VERSION).0)
E2E_KIND_VERSION ?= kindest/node:v$(E2E_K8S_FULL_VERSION)
E2E_USE_HELM ?= false
E2E_MODE ?= ci
E2E_SKIP_REINSTALL ?= false
KUEUE_UPGRADE_FROM_VERSION ?= v0.14.8
PROMETHEUS_OPERATOR_VERSION ?= v0.89.0
# When truthy, force re-installing external operators (MPI, Ray, etc.) on each run, even in E2E_MODE=dev.
E2E_ENFORCE_OPERATOR_UPDATE ?= false

# For local testing, we should allow user to use different kind cluster name
# Default will delete default kind cluster
KIND_CLUSTER_NAME ?= kind

# Number of processes to use during e2e tests.
E2E_NPROCS ?= 1

# Deferred variable: evaluated at recipe time so target-specific E2E_NPROCS overrides take effect.
E2E_GINKGO_ARGS = $(GINKGO_ARGS) $(if $(filter-out 1,$(E2E_NPROCS)),-procs=$(E2E_NPROCS))

# For restricting to a specific directory
GO_TEST_TARGET ?= .

# Unit test sharding: set UNIT_TOTAL_SHARDS to split packages across parallel CI jobs.
# UNIT_SHARD_INDEX selects which shard this job runs (0-based).
# When UNIT_TOTAL_SHARDS is not set, all packages run in a single job (existing behaviour).
ifdef UNIT_TOTAL_SHARDS
UNIT_TEST_PACKAGES := $(shell ./hack/testing/shard-unit-tests.sh $(UNIT_SHARD_INDEX) $(UNIT_TOTAL_SHARDS))
ifeq ($(UNIT_TEST_PACKAGES),)
$(error Aborting: shard-unit-tests.sh returned no packages. Check UNIT_SHARD_INDEX / UNIT_TOTAL_SHARDS.)
endif
else
UNIT_TEST_PACKAGES := $(shell $(GO_CMD) list $(GO_TEST_TARGET)/... | grep -v '/test/')
endif

OPTIONAL_SHARD_SUFFIX = $(if $(UNIT_TOTAL_SHARDS),-shard-$(UNIT_SHARD_INDEX))

##@ Tests

# Periodic builds are tested with full ray image
ifeq ($(JOB_TYPE),periodic)
    export USE_RAY_FOR_TESTS="ray"
else
	export USE_RAY_FOR_TESTS="raymini"
endif

# When using raymini, exclude tests that require the full ray image (e.g. RayService).
# Workaround until upstream Ray fixes protobuf 7.35+ compatibility (ray-project/ray#64362).
FULLRAY_EXCLUDE := $(if $(filter "raymini",$(USE_RAY_FOR_TESTS)), && !requires:fullray)

.PHONY: test
test: gotestsum ## Run tests. Set UNIT_TOTAL_SHARDS and UNIT_SHARD_INDEX to run a specific shard.
	mkdir -p $(ARTIFACTS)
# GORACE log_path makes the race detector write each report to
# $(ARTIFACTS)/race$(OPTIONAL_SHARD_SUFFIX).<pid> so it is archived as a CI artifact.
# Otherwise the report only goes to stderr, which gotestsum discards when a package
# fails under -race without an attributed test failure (kueue issue 13290). The file is
# written only when a race is actually detected, so green runs stay clean.
	GORACE="log_path=$(ARTIFACTS)/race$(OPTIONAL_SHARD_SUFFIX)" TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) $(GOTESTSUM) --junitfile $(ARTIFACTS)/junit$(OPTIONAL_SHARD_SUFFIX).xml -- $(GOFLAGS) $(GO_TEST_FLAGS) $(UNIT_TEST_PACKAGES) -coverpkg=$(GO_TEST_TARGET)/... -coverprofile $(ARTIFACTS)/cover$(OPTIONAL_SHARD_SUFFIX).out

## Label Taxonomy:
##   Controllers: controller:workload, controller:localqueue, controller:clusterqueue, controller:admissioncheck, controller:resourceflavor, controller:provisioning
##   Job Types: job:batch, job:pod, job:jobset, job:pytorch, job:tensorflow, job:mpi, job:paddle, job:xgboost, job:jax, job:train, job:ray, job:appwrapper, job:sparkapplication
##   Features: feature:tas, feature:multikueue, feature:provisioning, feature:fairsharing, feature:admissionfairsharing
##   Areas: area:core, area:jobs, area:admissionchecks, area:multikueue
##
## Examples:
##   Run only LocalQueue tests: INTEGRATION_FILTERS="--label-filter=controller:localqueue" make test-integration
##   Run all job tests: INTEGRATION_FILTERS="--label-filter=area:jobs" make test-integration
##   Run PyTorch job tests: INTEGRATION_FILTERS="--label-filter=job:pytorch" make test-integration
##   Run all tests except slow: INTEGRATION_FILTERS="--label-filter=!slow" make test-integration
##   Run core tests except slow: INTEGRATION_FILTERS="--label-filter=area:core && !slow" make test-integration
##   Run TAS-related tests: INTEGRATION_FILTERS="--label-filter=feature:tas" make test-integration
##   Run FairSharing tests: INTEGRATION_FILTERS="--label-filter=feature:fairsharing" make test-integration
##   Run AdmissionFairSharing tests: INTEGRATION_FILTERS="--label-filter=feature:admissionfairsharing" make test-integration

.PHONY: test-integration
test-integration: compile-crd-manifests envtest ginkgo dep-crds kueuectl ginkgo-top test-integration-run ## Run integration tests for all singlecluster suites with dependencies.

.PHONY: test-integration-run ## Run integration tests for all singlecluster suites.
test-integration-run:
	mkdir -p $(ARTIFACTS)
	KUBEBUILDER_ASSETS="$(KUBEBUILDER_ASSETS)" \
	PROJECT_DIR=$(PROJECT_DIR)/ \
	KUEUE_BIN=$(BIN_DIR) \
	ENVTEST_K8S_VERSION=$(ENVTEST_K8S_VERSION) \
	ARTIFACTS=$(ARTIFACTS) \
	TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) API_LOG_LEVEL=$(INTEGRATION_API_LOG_LEVEL) \
	GORACE="log_path=$(ARTIFACTS)/race" \
	$(GINKGO) $(INTEGRATION_FILTERS) $(GINKGO_ARGS) $(GOFLAGS) -procs=$(INTEGRATION_NPROCS) --race --json-report=integration.json --output-interceptor-mode=none --output-dir=$(ARTIFACTS) -v $(INTEGRATION_TARGET)
	$(BIN_DIR)/ginkgo-top -i $(ARTIFACTS)/integration.json > $(ARTIFACTS)/integration-top.yaml

.PHONY: test-integration-baseline
test-integration-baseline: INTEGRATION_FILTERS= --label-filter="!slow && !redundant"
test-integration-baseline: test-integration ## Run baseline integration tests for singlecluster suites.

.PHONY: test-integration-extended
test-integration-extended: INTEGRATION_FILTERS= --label-filter="slow || redundant"
test-integration-extended: test-integration ## Run extended integration tests for singlecluster suites.

.PHONY: test-multikueue-integration
test-multikueue-integration: compile-crd-manifests envtest ginkgo dep-crds ginkgo-top ## Run integration tests for MultiKueue suite.
	mkdir -p $(ARTIFACTS)
	KUBEBUILDER_ASSETS="$(KUBEBUILDER_ASSETS)" \
	PROJECT_DIR=$(PROJECT_DIR)/ \
	KUEUE_BIN=$(BIN_DIR) \
	ENVTEST_K8S_VERSION=$(ENVTEST_K8S_VERSION) \
	ARTIFACTS=$(ARTIFACTS) \
	TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) API_LOG_LEVEL=$(INTEGRATION_API_LOG_LEVEL) \
	GORACE="log_path=$(ARTIFACTS)/race" \
	$(GINKGO) $(INTEGRATION_FILTERS) $(GINKGO_ARGS) $(GOFLAGS) -procs=$(INTEGRATION_NPROCS_MULTIKUEUE) --race --json-report=multikueue-integration.json --output-interceptor-mode=none --output-dir=$(ARTIFACTS) -v $(INTEGRATION_TARGET_MULTIKUEUE)
	$(BIN_DIR)/ginkgo-top -i $(ARTIFACTS)/multikueue-integration.json > $(ARTIFACTS)/multikueue-integration-top.yaml

.PHONY: test-e2e-baseline-helm
test-e2e-baseline-helm: E2E_USE_HELM=true
test-e2e-baseline-helm: test-e2e-baseline

.PHONY: test-e2e-extended-helm
test-e2e-extended-helm: E2E_USE_HELM=true
test-e2e-extended-helm: test-e2e-extended

.PHONY: test-multikueue-e2e-baseline
test-multikueue-e2e-baseline: E2E_NPROCS := 5
test-multikueue-e2e-baseline: setup-e2e-env run-test-multikueue-e2e-baseline-$(E2E_KIND_VERSION:kindest/node:v%=%) ## Run the baseline MultiKueue e2e test suite.

# Assign shard-0 operator versions (all operators except KubeRay) to the shard-0 target
TEST_MULTIKUEUE_E2E_EXTENDED_SHARD_0_TARGETS := test-multikueue-e2e-extended test-multikueue-e2e-extended-shard-0
$(TEST_MULTIKUEUE_E2E_EXTENDED_SHARD_0_TARGETS): export JOBSET_VERSION := $(JOBSET_VERSION)
$(TEST_MULTIKUEUE_E2E_EXTENDED_SHARD_0_TARGETS): export LEADERWORKERSET_VERSION := $(LEADERWORKERSET_VERSION)
$(TEST_MULTIKUEUE_E2E_EXTENDED_SHARD_0_TARGETS): export APPWRAPPER_VERSION := $(APPWRAPPER_VERSION)
$(TEST_MULTIKUEUE_E2E_EXTENDED_SHARD_0_TARGETS): export KUBEFLOW_VERSION := $(KUBEFLOW_VERSION)
$(TEST_MULTIKUEUE_E2E_EXTENDED_SHARD_0_TARGETS): export KUBEFLOW_MPI_VERSION := $(KUBEFLOW_MPI_VERSION)
$(TEST_MULTIKUEUE_E2E_EXTENDED_SHARD_0_TARGETS): export KUBEFLOW_TRAINER_VERSION := $(KUBEFLOW_TRAINER_VERSION)

# Assign shard-1 operator versions (KubeRay + Ray) to the shard-1 target
TEST_MULTIKUEUE_E2E_EXTENDED_SHARD_1_TARGETS := test-multikueue-e2e-extended test-multikueue-e2e-extended-shard-1
$(TEST_MULTIKUEUE_E2E_EXTENDED_SHARD_1_TARGETS): export KUBERAY_VERSION := $(KUBERAY_VERSION)
$(TEST_MULTIKUEUE_E2E_EXTENDED_SHARD_1_TARGETS): export RAY_VERSION := $(RAY_VERSION)
$(TEST_MULTIKUEUE_E2E_EXTENDED_SHARD_1_TARGETS): export RAYMINI_VERSION := $(RAYMINI_VERSION)

.PHONY: test-multikueue-e2e-extended
test-multikueue-e2e-extended: E2E_NPROCS := 5
test-multikueue-e2e-extended: setup-e2e-env run-test-multikueue-e2e-extended-$(E2E_KIND_VERSION:kindest/node:v%=%) ## Run the extended MultiKueue e2e test suite.

.PHONY: test-multikueue-e2e-extended-shard-0
test-multikueue-e2e-extended-shard-0: E2E_NPROCS := 5
test-multikueue-e2e-extended-shard-0: GINKGO_ARGS=--label-filter=feature:leaderworkerset,feature:jobset,feature:appwrapper,feature:pytorchjob,feature:mpijob,feature:trainjob
test-multikueue-e2e-extended-shard-0: E2E_CONFIG_FOLDER=multikueue/extended-shard-0
test-multikueue-e2e-extended-shard-0: setup-e2e-env run-test-multikueue-e2e-extended-$(E2E_KIND_VERSION:kindest/node:v%=%)

.PHONY: test-multikueue-e2e-extended-shard-1
test-multikueue-e2e-extended-shard-1: E2E_NPROCS := 5
test-multikueue-e2e-extended-shard-1: GINKGO_ARGS=--label-filter='feature:kuberay$(FULLRAY_EXCLUDE)'
test-multikueue-e2e-extended-shard-1: E2E_CONFIG_FOLDER=multikueue/extended-shard-1
test-multikueue-e2e-extended-shard-1: setup-e2e-env run-test-multikueue-e2e-extended-$(E2E_KIND_VERSION:kindest/node:v%=%)

.PHONY: test-multikueue-e2e-sequential
test-multikueue-e2e-sequential: setup-e2e-env kind-secretreader-plugin-image-build run-test-e2e-multikueue-sequential-$(E2E_KIND_VERSION:kindest/node:v%=%) ## Run the sequential MultiKueue e2e test suite.

.PHONY: test-multikueue-e2e-sequential-shard-0
test-multikueue-e2e-sequential-shard-0: GINKGO_ARGS=--label-filter=shard-0
test-multikueue-e2e-sequential-shard-0: test-multikueue-e2e-sequential

.PHONY: test-multikueue-e2e-sequential-shard-1
test-multikueue-e2e-sequential-shard-1: GINKGO_ARGS=--label-filter=shard-1
test-multikueue-e2e-sequential-shard-1: test-multikueue-e2e-sequential

.PHONY: test-multikueue-e2e-helm
test-multikueue-e2e-helm: E2E_USE_HELM=true
test-multikueue-e2e-helm: test-multikueue-e2e

# Assign Shard 0 variables to all parent suites AND all shard-0 targets
TEST_E2E_SHARD_0_TARGETS := test-e2e-extended test-e2e-extended-shard-0 test-tas-e2e-extended test-tas-e2e-extended-shard-0
$(TEST_E2E_SHARD_0_TARGETS): export KUBERAY_VERSION := $(KUBERAY_VERSION)
$(TEST_E2E_SHARD_0_TARGETS): export RAY_VERSION := $(RAY_VERSION)
$(TEST_E2E_SHARD_0_TARGETS): export RAYMINI_VERSION := $(RAYMINI_VERSION)

# Assign Shard 1 variables to all parent suites AND all shard-1 targets
TEST_E2E_SHARD_1_TARGETS := test-e2e-extended test-e2e-extended-shard-1 test-tas-e2e-extended test-tas-e2e-extended-shard-1
$(TEST_E2E_SHARD_1_TARGETS): export JOBSET_VERSION := $(JOBSET_VERSION)
$(TEST_E2E_SHARD_1_TARGETS): export LEADERWORKERSET_VERSION := $(LEADERWORKERSET_VERSION)
$(TEST_E2E_SHARD_1_TARGETS): export APPWRAPPER_VERSION := $(APPWRAPPER_VERSION)
$(TEST_E2E_SHARD_1_TARGETS): export KUBEFLOW_VERSION := $(KUBEFLOW_VERSION)
$(TEST_E2E_SHARD_1_TARGETS): export KUBEFLOW_MPI_VERSION := $(KUBEFLOW_MPI_VERSION)
$(TEST_E2E_SHARD_1_TARGETS): export KUBEFLOW_TRAINER_VERSION := $(KUBEFLOW_TRAINER_VERSION)

# Assign Shard 2 variables to all parent suites AND all shard-2 targets
TEST_E2E_SHARD_2_TARGETS := test-e2e-extended test-e2e-extended-shard-2
$(TEST_E2E_SHARD_2_TARGETS): export KUBERAY_VERSION := $(KUBERAY_VERSION)
$(TEST_E2E_SHARD_2_TARGETS): export RAY_VERSION := $(RAY_VERSION)
$(TEST_E2E_SHARD_2_TARGETS): export RAYMINI_VERSION := $(RAYMINI_VERSION)

## Label Taxonomy:
##   Features: appwrapper,jaxjob,jobset,kuberay,leaderworkerset,pytorchjob,trainjob,mpijob
##
## Examples:
##   Run only AppWrapper tests: GINKGO_ARGS="--label-filter=feature:appwrapper" make test-e2e-extended
.PHONY: test-e2e-extended
test-e2e-extended: E2E_NPROCS := 4
test-e2e-extended: run-test-e2e-extended-$(E2E_KIND_VERSION:kindest/node:v%=%) ## Run the extended e2e test suite (job-framework integrations) on a kind cluster.

.PHONY: test-e2e-extended-shard-0
test-e2e-extended-shard-0: E2E_NPROCS := 4
test-e2e-extended-shard-0: GINKGO_ARGS=--label-filter='feature:kuberay && shard:kuberay-a$(FULLRAY_EXCLUDE)'
test-e2e-extended-shard-0: setup-e2e-env run-test-e2e-extended-$(E2E_KIND_VERSION:kindest/node:v%=%)

.PHONY: test-e2e-extended-shard-1
test-e2e-extended-shard-1: E2E_NPROCS := 4
test-e2e-extended-shard-1: GINKGO_ARGS=--label-filter=feature:appwrapper,feature:jaxjob,feature:jobset,feature:leaderworkerset,feature:pytorchjob,feature:trainjob,feature:mpijob
test-e2e-extended-shard-1: setup-e2e-env run-test-e2e-extended-$(E2E_KIND_VERSION:kindest/node:v%=%)

.PHONY: test-e2e-extended-shard-2
test-e2e-extended-shard-2: E2E_NPROCS := 2
test-e2e-extended-shard-2: GINKGO_ARGS=--label-filter='feature:kuberay && shard:kuberay-b$(FULLRAY_EXCLUDE)'
test-e2e-extended-shard-2: setup-e2e-env run-test-e2e-extended-$(E2E_KIND_VERSION:kindest/node:v%=%)

## Label Taxonomy:
##   Features: certs,deployment,job,fairsharing,kueuectl,metrics,pod,statefulset,visibility,e2e_v1beta1,ha
##
## Examples:
##   Run only job tests: GINKGO_ARGS="--label-filter=feature:job" make test-e2e-baseline
.PHONY: test-e2e-baseline
test-e2e-baseline: E2E_NPROCS := 4
test-e2e-baseline: setup-e2e-env kueuectl run-test-e2e-baseline-$(E2E_KIND_VERSION:kindest/node:v%=%) ## Run the baseline e2e test suite on a kind cluster.

.PHONY: test-tas-e2e-baseline
test-tas-e2e-baseline: setup-e2e-env run-test-tas-e2e-baseline-$(E2E_KIND_VERSION:kindest/node:v%=%) ## Run the baseline Topology-Aware Scheduling (TAS) e2e test suite.

.PHONY: test-tas-e2e-extended
test-tas-e2e-extended: run-test-tas-e2e-extended-$(E2E_KIND_VERSION:kindest/node:v%=%) ## Run the extended Topology-Aware Scheduling (TAS) e2e test suite.

.PHONY: test-tas-e2e-extended-shard-0
test-tas-e2e-extended-shard-0: GINKGO_ARGS=--label-filter=feature:kuberay
test-tas-e2e-extended-shard-0: setup-e2e-env run-test-tas-e2e-extended-$(E2E_KIND_VERSION:kindest/node:v%=%)

.PHONY: test-tas-e2e-extended-shard-1
test-tas-e2e-extended-shard-1: GINKGO_ARGS=--label-filter=feature:appwrapper,feature:jobset,feature:leaderworkerset,feature:pytorchjob,feature:trainjob,feature:mpijob
test-tas-e2e-extended-shard-1: setup-e2e-env run-test-tas-e2e-extended-$(E2E_KIND_VERSION:kindest/node:v%=%)

.PHONY: test-tas-e2e-baseline-helm
test-tas-e2e-baseline-helm: E2E_USE_HELM=true
test-tas-e2e-baseline-helm: test-tas-e2e-baseline

.PHONY: test-tas-e2e-extended-helm
test-tas-e2e-extended-helm: E2E_USE_HELM=true
test-tas-e2e-extended-helm: test-tas-e2e-extended

# WAS versions of TAS e2e tests
.PHONY: test-tas-was-e2e-baseline
test-tas-was-e2e-baseline: E2E_EXTRA_KUEUE_FEATURE_GATES=SchedulerLibraryIntegration=true
test-tas-was-e2e-baseline: test-tas-e2e-baseline

.PHONY: test-tas-was-e2e-extended
test-tas-was-e2e-extended: E2E_EXTRA_KUEUE_FEATURE_GATES=SchedulerLibraryIntegration=true
test-tas-was-e2e-extended: test-tas-e2e-extended

.PHONY: test-tas-was-e2e-extended-shard-0
test-tas-was-e2e-extended-shard-0: E2E_EXTRA_KUEUE_FEATURE_GATES=SchedulerLibraryIntegration=true
test-tas-was-e2e-extended-shard-0: test-tas-e2e-extended-shard-0

.PHONY: test-tas-was-e2e-extended-shard-1
test-tas-was-e2e-extended-shard-1: E2E_EXTRA_KUEUE_FEATURE_GATES=SchedulerLibraryIntegration=true
test-tas-was-e2e-extended-shard-1: test-tas-e2e-extended-shard-1

.PHONY: test-tas-was-e2e-baseline-helm
test-tas-was-e2e-baseline-helm: E2E_EXTRA_KUEUE_FEATURE_GATES=SchedulerLibraryIntegration=true
test-tas-was-e2e-baseline-helm: test-tas-e2e-baseline-helm

.PHONY: test-tas-was-e2e-extended-helm
test-tas-was-e2e-extended-helm: E2E_EXTRA_KUEUE_FEATURE_GATES=SchedulerLibraryIntegration=true
test-tas-was-e2e-extended-helm: test-tas-e2e-extended-helm

.PHONY: test-e2e-certmanager
test-e2e-certmanager: setup-e2e-env run-test-e2e-certmanager-$(E2E_KIND_VERSION:kindest/node:v%=%) ## Run the cert-manager e2e test suite.

## Label Taxonomy:
##   Features: admissionfairsharing, certs, failurerecoverypolicy, localqueuemetrics, managejobswithoutqueuename, objectretentionpolicies, podintegrationautoenablement, reconcile, visibility, waitforpodsready
## Examples:
##   Run only Admission Fair Sharing tests: GINKGO_ARGS="--label-filter=feature:admissionfairsharing" make test-e2e-sequential-baseline
##   Run only Certs tests: GINKGO_ARGS="--label-filter=feature:certs" make test-e2e-sequential-baseline
##   Run only Failure Recovery Policy tests: GINKGO_ARGS="--label-filter=feature:failurerecoverypolicy" make test-e2e-sequential-baseline
##   Run only shard 0 tests: make test-e2e-sequential-baseline-shard-0
##   Run only shard 1 tests: make test-e2e-sequential-baseline-shard-1
.PHONY: test-e2e-sequential-baseline
test-e2e-sequential-baseline: setup-e2e-env run-test-e2e-sequential-baseline-$(E2E_KIND_VERSION:kindest/node:v%=%) ## Run the baseline sequential e2e test suite.

.PHONY: test-e2e-sequential-baseline-shard-0
test-e2e-sequential-baseline-shard-0: GINKGO_ARGS=--label-filter=shard-0
test-e2e-sequential-baseline-shard-0: test-e2e-sequential-baseline

.PHONY: test-e2e-sequential-baseline-shard-1
test-e2e-sequential-baseline-shard-1: GINKGO_ARGS=--label-filter=shard-1
test-e2e-sequential-baseline-shard-1: test-e2e-sequential-baseline

.PHONY: test-e2e-sequential-baseline-helm
test-e2e-sequential-baseline-helm: E2E_USE_HELM=true
test-e2e-sequential-baseline-helm: test-e2e-sequential-baseline

# Assign Shard 0 variables to the shard-0 target
TEST_E2E_SEQUENTIAL_EXTENDED_SHARD_0_TARGETS := test-e2e-sequential-extended test-e2e-sequential-extended-shard-0
$(TEST_E2E_SEQUENTIAL_EXTENDED_SHARD_0_TARGETS): export JOBSET_VERSION := $(JOBSET_VERSION)
$(TEST_E2E_SEQUENTIAL_EXTENDED_SHARD_0_TARGETS): export APPWRAPPER_VERSION := $(APPWRAPPER_VERSION)
$(TEST_E2E_SEQUENTIAL_EXTENDED_SHARD_0_TARGETS): export LEADERWORKERSET_VERSION := $(LEADERWORKERSET_VERSION)

# Assign Shard 1 variables to the shard-1 target
TEST_E2E_SEQUENTIAL_EXTENDED_SHARD_1_TARGETS := test-e2e-sequential-extended test-e2e-sequential-extended-shard-1
$(TEST_E2E_SEQUENTIAL_EXTENDED_SHARD_1_TARGETS): export SPARKOPERATOR_VERSION := $(SPARKOPERATOR_VERSION)

## Label Taxonomy:
##   Features: managejobswithoutqueuename, workloadidentifierannotations, spark
## Examples:
##   Run only Spark Integration tests: GINKGO_ARGS="--label-filter=feature:spark" make test-e2e-sequential-extended
.PHONY: test-e2e-sequential-extended
test-e2e-sequential-extended: setup-e2e-env run-test-e2e-sequential-extended-$(E2E_KIND_VERSION:kindest/node:v%=%) ## Run the extended sequential e2e test suite.

.PHONY: test-e2e-sequential-extended-shard-0
test-e2e-sequential-extended-shard-0: GINKGO_ARGS=--label-filter=feature:managejobswithoutqueuename,feature:workloadidentifierannotations
test-e2e-sequential-extended-shard-0: setup-e2e-env run-test-e2e-sequential-extended-$(E2E_KIND_VERSION:kindest/node:v%=%)

.PHONY: test-e2e-sequential-extended-shard-1
test-e2e-sequential-extended-shard-1: GINKGO_ARGS=--label-filter=feature:spark
test-e2e-sequential-extended-shard-1: setup-e2e-env run-test-e2e-sequential-extended-$(E2E_KIND_VERSION:kindest/node:v%=%)

.PHONY: test-e2e-sequential-extended-helm
test-e2e-sequential-extended-helm: E2E_USE_HELM=true
test-e2e-sequential-extended-helm: test-e2e-sequential-extended
.PHONY: test-e2e-upgrade
test-e2e-upgrade: setup-e2e-env run-test-e2e-upgrade-$(E2E_KIND_VERSION:kindest/node:v%=%) ## Run the upgrade e2e test suite.

.PHONY: test-e2e-certmanager-upgrade
test-e2e-certmanager-upgrade: setup-e2e-env run-test-e2e-certmanager-upgrade-$(E2E_KIND_VERSION:kindest/node:v%=%) ## Run the cert-manager upgrade e2e test suite.

.PHONY: test-e2e-dra
test-e2e-dra: setup-e2e-env run-test-e2e-dra-$(E2E_KIND_VERSION:kindest/node:v%=%) ## Run the Dynamic Resource Allocation (DRA) e2e test suite.

.PHONY: test-e2e-multikueue-dra
test-e2e-multikueue-dra: setup-e2e-env run-test-e2e-multikueue-dra-$(E2E_KIND_VERSION:kindest/node:v%=%) ## Run the MultiKueue Dynamic Resource Allocation (DRA) e2e test suite.

run-test-e2e-baseline-%: K8S_VERSION = $(@:run-test-e2e-baseline-%=%)
run-test-e2e-baseline-%:
	@echo Running baseline e2e for k8s ${K8S_VERSION}
	E2E_KIND_VERSION="kindest/node:v$(K8S_VERSION)" KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) \
		ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(E2E_GINKGO_ARGS)" \
		E2E_MODE=$(E2E_MODE) \
		E2E_SKIP_REINSTALL=$(E2E_SKIP_REINSTALL) \
		E2E_ENFORCE_OPERATOR_UPDATE=$(E2E_ENFORCE_OPERATOR_UPDATE) \
		PROMETHEUS_OPERATOR_VERSION=$(PROMETHEUS_OPERATOR_VERSION) \
		KIND_CLUSTER_FILE="kind-cluster.yaml" E2E_TARGET_FOLDER="singlecluster/baseline" \
		E2E_CONFIG_FOLDER="baseline" \
		TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) \
		E2E_USE_HELM=$(E2E_USE_HELM) \
		./hack/testing/e2e-test.sh

run-test-e2e-extended-%: K8S_VERSION = $(@:run-test-e2e-extended-%=%)
run-test-e2e-extended-%:
	@echo Running extended e2e for k8s ${K8S_VERSION}
	E2E_KIND_VERSION="kindest/node:v$(K8S_VERSION)" KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) \
		ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(E2E_GINKGO_ARGS)" \
		E2E_MODE=$(E2E_MODE) \
		E2E_SKIP_REINSTALL=$(E2E_SKIP_REINSTALL) \
		E2E_ENFORCE_OPERATOR_UPDATE=$(E2E_ENFORCE_OPERATOR_UPDATE) \
		USE_RAY_FOR_TESTS=$(USE_RAY_FOR_TESTS) \
		KIND_CLUSTER_FILE="kind-cluster.yaml" E2E_TARGET_FOLDER="singlecluster/extended" \
		E2E_CONFIG_FOLDER="extended" \
		TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) \
		E2E_USE_HELM=$(E2E_USE_HELM) \
		./hack/testing/e2e-test.sh

run-test-multikueue-e2e-baseline-%: K8S_VERSION = $(@:run-test-multikueue-e2e-baseline-%=%)
run-test-multikueue-e2e-baseline-%:
	@echo Running baseline multikueue e2e for k8s ${K8S_VERSION}
	E2E_KIND_VERSION="kindest/node:v$(K8S_VERSION)" KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) \
		ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(E2E_GINKGO_ARGS)" \
		E2E_MODE=$(E2E_MODE) \
		E2E_SKIP_REINSTALL=$(E2E_SKIP_REINSTALL) \
		E2E_TARGET_FOLDER="multikueue/baseline" \
		E2E_CONFIG_FOLDER="multikueue/baseline" \
		TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) \
		E2E_USE_HELM=$(E2E_USE_HELM) \
		./hack/testing/e2e-multikueue-test.sh

run-test-multikueue-e2e-extended-%: K8S_VERSION = $(@:run-test-multikueue-e2e-extended-%=%)
run-test-multikueue-e2e-extended-%:
	@echo Running extended multikueue e2e for k8s ${K8S_VERSION}
	E2E_KIND_VERSION="kindest/node:v$(K8S_VERSION)" KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) \
		ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(E2E_GINKGO_ARGS)" \
		E2E_MODE=$(E2E_MODE) \
		E2E_SKIP_REINSTALL=$(E2E_SKIP_REINSTALL) \
		E2E_ENFORCE_OPERATOR_UPDATE=$(E2E_ENFORCE_OPERATOR_UPDATE) \
		USE_RAY_FOR_TESTS=$(USE_RAY_FOR_TESTS) \
		E2E_TARGET_FOLDER="multikueue/extended" \
		E2E_CONFIG_FOLDER=$(E2E_CONFIG_FOLDER) \
		TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) \
		E2E_USE_HELM=$(E2E_USE_HELM) \
		./hack/testing/e2e-multikueue-test.sh

run-test-tas-e2e-baseline-%: K8S_VERSION = $(@:run-test-tas-e2e-baseline-%=%)
run-test-tas-e2e-baseline-%:
	@echo Running tas e2e baseline for k8s ${K8S_VERSION}
	E2E_KIND_VERSION="kindest/node:v$(K8S_VERSION)" KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) \
		ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(E2E_GINKGO_ARGS)" \
		E2E_MODE=$(E2E_MODE) \
		E2E_SKIP_REINSTALL=$(E2E_SKIP_REINSTALL) \
		E2E_ENFORCE_OPERATOR_UPDATE=$(E2E_ENFORCE_OPERATOR_UPDATE) \
		KIND_CLUSTER_FILE="kind-cluster-tas.yaml" E2E_TARGET_FOLDER="tas/baseline" \
		E2E_CONFIG_FOLDER="baseline" \
		TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) \
		E2E_USE_HELM=$(E2E_USE_HELM) \
		./hack/testing/e2e-test.sh

run-test-tas-e2e-extended-%: K8S_VERSION = $(@:run-test-tas-e2e-extended-%=%)
run-test-tas-e2e-extended-%:
	@echo Running tas e2e extended for k8s ${K8S_VERSION}
	E2E_KIND_VERSION="kindest/node:v$(K8S_VERSION)" KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) \
		ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(E2E_GINKGO_ARGS)" \
		E2E_MODE=$(E2E_MODE) \
		E2E_SKIP_REINSTALL=$(E2E_SKIP_REINSTALL) \
		E2E_ENFORCE_OPERATOR_UPDATE=$(E2E_ENFORCE_OPERATOR_UPDATE) \
		USE_RAY_FOR_TESTS=$(USE_RAY_FOR_TESTS) \
		KIND_CLUSTER_FILE="kind-cluster-tas.yaml" E2E_TARGET_FOLDER="tas/extended" \
		E2E_CONFIG_FOLDER="extended" \
		TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) \
		E2E_USE_HELM=$(E2E_USE_HELM) \
		./hack/testing/e2e-test.sh

run-test-e2e-sequential-baseline-%: K8S_VERSION = $(@:run-test-e2e-sequential-baseline-%=%)
run-test-e2e-sequential-baseline-%:
	@echo Running sequential baseline e2e for k8s ${K8S_VERSION}
	E2E_KIND_VERSION="kindest/node:v$(K8S_VERSION)" KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) \
	ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(E2E_GINKGO_ARGS)" \
	E2E_MODE=$(E2E_MODE) \
	E2E_SKIP_REINSTALL=$(E2E_SKIP_REINSTALL) \
	E2E_ENFORCE_OPERATOR_UPDATE=$(E2E_ENFORCE_OPERATOR_UPDATE) \
	KIND_CLUSTER_FILE="kind-cluster.yaml" E2E_TARGET_FOLDER="sequential/baseline" \
	E2E_CONFIG_FOLDER="baseline" \
	TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) \
	E2E_USE_HELM=$(E2E_USE_HELM) \
	./hack/testing/e2e-test.sh

run-test-e2e-sequential-extended-%: K8S_VERSION = $(@:run-test-e2e-sequential-extended-%=%)
run-test-e2e-sequential-extended-%:
	@echo Running sequential extended e2e for k8s ${K8S_VERSION}
	E2E_KIND_VERSION="kindest/node:v$(K8S_VERSION)" KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) \
	ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(E2E_GINKGO_ARGS)" \
	E2E_MODE=$(E2E_MODE) \
	E2E_SKIP_REINSTALL=$(E2E_SKIP_REINSTALL) \
	E2E_ENFORCE_OPERATOR_UPDATE=$(E2E_ENFORCE_OPERATOR_UPDATE) \
	KIND_CLUSTER_FILE="kind-cluster.yaml" E2E_TARGET_FOLDER="sequential/extended" \
	E2E_CONFIG_FOLDER="extended" \
	TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) \
	E2E_USE_HELM=$(E2E_USE_HELM) \
	./hack/testing/e2e-test.sh

run-test-e2e-certmanager-%: K8S_VERSION = $(@:run-test-e2e-certmanager-%=%)
run-test-e2e-certmanager-%:
	@echo Running certmanager e2e for k8s ${K8S_VERSION}
	E2E_KIND_VERSION="kindest/node:v$(K8S_VERSION)" KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) \
		ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(E2E_GINKGO_ARGS)" \
		E2E_MODE=$(E2E_MODE) \
		E2E_SKIP_REINSTALL=$(E2E_SKIP_REINSTALL) \
		E2E_ENFORCE_OPERATOR_UPDATE=$(E2E_ENFORCE_OPERATOR_UPDATE) \
		KIND_CLUSTER_FILE="kind-cluster.yaml" E2E_TARGET_FOLDER="certmanager" \
		CERTMANAGER_VERSION=$(CERTMANAGER_VERSION) \
		PROMETHEUS_OPERATOR_VERSION=$(PROMETHEUS_OPERATOR_VERSION) \
		TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) \
		E2E_USE_HELM=$(E2E_USE_HELM) \
		./hack/testing/e2e-test.sh

run-test-e2e-upgrade-%: K8S_VERSION = $(@:run-test-e2e-upgrade-%=%)
run-test-e2e-upgrade-%:
	@echo Running upgrade e2e for k8s ${K8S_VERSION}
	E2E_KIND_VERSION="kindest/node:v$(K8S_VERSION)" KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) \
		ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(E2E_GINKGO_ARGS)" \
		E2E_MODE=$(E2E_MODE) \
		E2E_SKIP_REINSTALL=$(E2E_SKIP_REINSTALL) \
		E2E_ENFORCE_OPERATOR_UPDATE=$(E2E_ENFORCE_OPERATOR_UPDATE) \
		KIND_CLUSTER_FILE="kind-cluster.yaml" E2E_TARGET_FOLDER="upgrade" \
		KUEUE_UPGRADE_FROM_VERSION=$(KUEUE_UPGRADE_FROM_VERSION) \
		TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) \
		./hack/testing/e2e-test.sh

run-test-e2e-certmanager-upgrade-%: K8S_VERSION = $(@:run-test-e2e-certmanager-upgrade-%=%)
run-test-e2e-certmanager-upgrade-%:
	@echo Running upgrade e2e for k8s ${K8S_VERSION}
	E2E_KIND_VERSION="kindest/node:v$(K8S_VERSION)" KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) \
		ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(E2E_GINKGO_ARGS)" \
		E2E_MODE=$(E2E_MODE) \
		E2E_SKIP_REINSTALL=$(E2E_SKIP_REINSTALL) \
		E2E_ENFORCE_OPERATOR_UPDATE=$(E2E_ENFORCE_OPERATOR_UPDATE) \
		KIND_CLUSTER_FILE="kind-cluster.yaml" E2E_TARGET_FOLDER="upgrade" \
		KUEUE_UPGRADE_FROM_VERSION=$(KUEUE_UPGRADE_FROM_VERSION) \
		CERTMANAGER_VERSION=$(CERTMANAGER_VERSION) \
		TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) \
		./hack/testing/e2e-test.sh

run-test-e2e-dra-%: K8S_VERSION = $(@:run-test-e2e-dra-%=%)
run-test-e2e-dra-%:
	@echo Running DRA e2e for k8s ${K8S_VERSION}
	E2E_KIND_VERSION="kindest/node:v$(K8S_VERSION)" KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) \
		ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(E2E_GINKGO_ARGS)" \
		E2E_MODE=$(E2E_MODE) \
		E2E_SKIP_REINSTALL=$(E2E_SKIP_REINSTALL) \
		E2E_ENFORCE_OPERATOR_UPDATE=$(E2E_ENFORCE_OPERATOR_UPDATE) \
		KIND_CLUSTER_FILE="kind-cluster.yaml" E2E_TARGET_FOLDER="dra/whole-device" \
		DRA_EXAMPLE_DRIVER_VERSION=$(DRA_EXAMPLE_DRIVER_VERSION) \
		TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) \
		./hack/testing/e2e-test.sh

run-test-e2e-multikueue-dra-%: K8S_VERSION = $(@:run-test-e2e-multikueue-dra-%=%)
run-test-e2e-multikueue-dra-%:
	@echo Running multikueue DRA e2e for k8s ${K8S_VERSION}
	E2E_KIND_VERSION="kindest/node:v$(K8S_VERSION)" KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) \
		ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(E2E_GINKGO_ARGS)" \
		E2E_MODE=$(E2E_MODE) \
		E2E_SKIP_REINSTALL=$(E2E_SKIP_REINSTALL) \
		E2E_ENFORCE_OPERATOR_UPDATE=$(E2E_ENFORCE_OPERATOR_UPDATE) \
		DRA_EXAMPLE_DRIVER_VERSION=$(DRA_EXAMPLE_DRIVER_VERSION) \
		E2E_TARGET_FOLDER="multikueue/dra" \
		TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) \
		./hack/testing/e2e-multikueue-test.sh

.PHONY: test-e2e-dra-counter
test-e2e-dra-counter: setup-e2e-env run-test-e2e-dra-counter-$(E2E_KIND_VERSION:kindest/node:v%=%)

run-test-e2e-dra-counter-%: K8S_VERSION = $(@:run-test-e2e-dra-counter-%=%)
run-test-e2e-dra-counter-%:
	@echo Running DRA Partitionable Devices e2e for k8s ${K8S_VERSION}
	E2E_KIND_VERSION="kindest/node:v$(K8S_VERSION)" KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) \
		ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(E2E_GINKGO_ARGS)" \
		E2E_MODE=$(E2E_MODE) \
		E2E_SKIP_REINSTALL=$(E2E_SKIP_REINSTALL) \
		E2E_ENFORCE_OPERATOR_UPDATE=$(E2E_ENFORCE_OPERATOR_UPDATE) \
		KIND_CLUSTER_FILE="kind-cluster.yaml" E2E_TARGET_FOLDER="dra/counter" \
		DRA_EXAMPLE_DRIVER_VERSION=$(DRA_EXAMPLE_DRIVER_VERSION) \
		DRA_GPU_PARTITIONS=4 \
		TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) \
		./hack/testing/e2e-test.sh

.PHONY: test-e2e-dra-capacity
test-e2e-dra-capacity: setup-e2e-env run-test-e2e-dra-capacity-$(E2E_KIND_VERSION:kindest/node:v%=%)

run-test-e2e-dra-capacity-%: K8S_VERSION = $(@:run-test-e2e-dra-capacity-%=%)
run-test-e2e-dra-capacity-%:
	@echo Running DRA Consumable Capacity e2e for k8s ${K8S_VERSION}
	E2E_KIND_VERSION="kindest/node:v$(K8S_VERSION)" KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) \
		ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(E2E_GINKGO_ARGS)" \
		E2E_MODE=$(E2E_MODE) \
		E2E_SKIP_REINSTALL=$(E2E_SKIP_REINSTALL) \
		E2E_ENFORCE_OPERATOR_UPDATE=$(E2E_ENFORCE_OPERATOR_UPDATE) \
		KIND_CLUSTER_FILE="kind-cluster.yaml" E2E_TARGET_FOLDER="dra/capacity" \
		DRA_EXAMPLE_DRIVER_VERSION=$(DRA_EXAMPLE_DRIVER_VERSION) \
		DRA_GPU_ALLOW_MULTIPLE_ALLOCATIONS=true \
		TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) \
		./hack/testing/e2e-test.sh

run-test-e2e-multikueue-sequential-%: K8S_VERSION = $(@:run-test-e2e-multikueue-sequential-%=%)
run-test-e2e-multikueue-sequential-%:
	@echo Running multikueue sequential suite of e2e tests for k8s ${K8S_VERSION}
	E2E_KIND_VERSION="kindest/node:v$(K8S_VERSION)" KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) \
		ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(E2E_GINKGO_ARGS)" \
		E2E_MODE=$(E2E_MODE) \
		E2E_SKIP_REINSTALL=$(E2E_SKIP_REINSTALL) \
		E2E_ENFORCE_OPERATOR_UPDATE=$(E2E_ENFORCE_OPERATOR_UPDATE) \
		E2E_TARGET_FOLDER="multikueue/sequential" \
		E2E_CONFIG_FOLDER="multikueue/sequential" \
		TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) \
		CLUSTERPROFILE_VERSION=$(CLUSTERPROFILE_VERSION) \
		CLUSTERPROFILE_PLUGIN_IMAGE_VERSION=$(CLUSTERPROFILE_PLUGIN_IMAGE_VERSION) \
		E2E_USE_HELM=$(E2E_USE_HELM) \
		./hack/testing/e2e-multikueue-test.sh

# Run e2e tests against k/k main (latest CI build) with WAS enabled
K8S_MAIN_NODE_IMAGE ?= k8s-main:latest
.PHONY: test-e2e-k8s-main-was
test-e2e-k8s-main-was: setup-e2e-env kueuectl kind-k8s-main-image-build run-test-e2e-k8s-main-was

.PHONY: kind-k8s-main-image-build
kind-k8s-main-image-build: kind
	@echo "Fetching latest Kubernetes CI build version..."
	$(eval K8S_CI_VERSION := $(shell curl -sL https://dl.k8s.io/ci/latest.txt))
	@echo "Building kind node image from k/k main: $(K8S_CI_VERSION)"
	$(KIND) build node-image --image=$(K8S_MAIN_NODE_IMAGE) \
		"https://dl.k8s.io/ci/$(K8S_CI_VERSION)/kubernetes-server-linux-$(shell go env GOARCH).tar.gz"

.PHONY: run-test-e2e-k8s-main-was
run-test-e2e-k8s-main-was:
	@echo Running e2e for k8s main with WAS enabled
	E2E_KIND_VERSION="$(K8S_MAIN_NODE_IMAGE)" KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) \
		ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(E2E_GINKGO_ARGS)" \
		E2E_MODE=$(E2E_MODE) \
		E2E_SKIP_REINSTALL=$(E2E_SKIP_REINSTALL) \
		APPWRAPPER_VERSION=$(APPWRAPPER_VERSION) \
		JOBSET_VERSION=$(JOBSET_VERSION) \
		KUBEFLOW_VERSION=$(KUBEFLOW_VERSION) \
		KUBEFLOW_MPI_VERSION=$(KUBEFLOW_MPI_VERSION) \
		KUBEFLOW_TRAINER_VERSION=$(KUBEFLOW_TRAINER_VERSION) \
		LEADERWORKERSET_VERSION=$(LEADERWORKERSET_VERSION) \
		KUBERAY_VERSION=$(KUBERAY_VERSION) RAY_VERSION=$(RAY_VERSION) RAYMINI_VERSION=$(RAYMINI_VERSION) USE_RAY_FOR_TESTS="ray" \
		PROMETHEUS_OPERATOR_VERSION=$(PROMETHEUS_OPERATOR_VERSION) \
		KIND_CLUSTER_FILE="kind-cluster.yaml" E2E_TARGET_FOLDER="singlecluster" \
		TEST_LOG_LEVEL=$(TEST_LOG_LEVEL) \
		E2E_USE_HELM=$(E2E_USE_HELM) \
		WAS_ENABLED=true \
		./hack/testing/e2e-test.sh

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

ifdef SCALABILITY_MEM_PROFILE
SCALABILITY_EXTRA_ARGS += --withMemProfile=true
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

SCALABILITY_GENERATOR_CONFIG ?= $(PROJECT_DIR)/test/performance/scheduler/configs/baseline/generator.yaml

.PHONY: run-performance-scheduler
run-performance-scheduler: envtest performance-scheduler-runner minimalkueue
	mkdir -p "$(ARTIFACTS)/$@"
	KUBEBUILDER_ASSETS="$(KUBEBUILDER_ASSETS)" \
	$(SCALABILITY_RUNNER) \
		--o "$(ARTIFACTS)/$@" \
		--crds=$(PROJECT_DIR)/config/components/crd/bases \
		--generatorConfig=$(SCALABILITY_GENERATOR_CONFIG) \
		--minimalKueue=$(MINIMALKUEUE_RUNNER) $(SCALABILITY_EXTRA_ARGS) $(SCALABILITY_SCRAPE_ARGS)

.PHONY: test-performance-scheduler-once
test-performance-scheduler-once: gotestsum run-performance-scheduler
	$(GOTESTSUM) --junitfile $(ARTIFACTS)/junit.xml -- $(GO_TEST_FLAGS) ./test/performance/scheduler/checker  \
		--summary=$(ARTIFACTS)/run-performance-scheduler/summary.yaml \
		--cmdStats=$(ARTIFACTS)/run-performance-scheduler/minimalkueue.stats.yaml \
		--range=$(PROJECT_DIR)/test/performance/scheduler/configs/baseline/rangespec.yaml

PERFORMANCE_RETRY_COUNT?=2
.PHONY: test-performance-scheduler
test-performance-scheduler:
	ARTIFACTS="$(ARTIFACTS)/$@" ./hack/testing/performance-test.sh $(PERFORMANCE_RETRY_COUNT) test-performance-scheduler-once

.PHONY: run-performance-scheduler-in-cluster
run-performance-scheduler-in-cluster: envtest performance-scheduler-runner
	mkdir -p "$(ARTIFACTS)/$@"
	KUBEBUILDER_ASSETS="$(KUBEBUILDER_ASSETS)" \
	$(SCALABILITY_RUNNER) \
		--o "$(ARTIFACTS)/$@" \
		--generatorConfig=$(SCALABILITY_GENERATOR_CONFIG) \
		--qps=1000 --burst=2000 --timeout=15m $(SCALABILITY_SCRAPE_ARGS)

##@ Scheduler Performance Testing with TAS

SCALABILITY_TAS_GENERATOR_CONFIG ?= $(PROJECT_DIR)/test/performance/scheduler/configs/tas/generator.yaml
SCALABILITY_TAS_RANGE_FILE ?= $(PROJECT_DIR)/test/performance/scheduler/configs/tas/rangespec.yaml

.PHONY: run-tas-performance-scheduler
run-tas-performance-scheduler: envtest performance-scheduler-runner minimalkueue
	mkdir -p "$(ARTIFACTS)/$@"
	KUBEBUILDER_ASSETS="$(KUBEBUILDER_ASSETS)" \
	$(SCALABILITY_RUNNER) \
		--o "$(ARTIFACTS)/$@" \
		--crds=$(PROJECT_DIR)/config/components/crd/bases \
		--generatorConfig=$(SCALABILITY_TAS_GENERATOR_CONFIG) \
		--minimalKueue=$(MINIMALKUEUE_RUNNER) \
		--enableTAS=true --timeout=20m $(SCALABILITY_EXTRA_ARGS) $(SCALABILITY_SCRAPE_ARGS)

.PHONY: test-tas-performance-scheduler-once
test-tas-performance-scheduler-once: gotestsum run-tas-performance-scheduler
	$(GOTESTSUM) --junitfile $(ARTIFACTS)/junit.xml -- $(GO_TEST_FLAGS) ./test/performance/scheduler/checker  \
		--summary=$(ARTIFACTS)/run-tas-performance-scheduler/summary.yaml \
		--cmdStats=$(ARTIFACTS)/run-tas-performance-scheduler/minimalkueue.stats.yaml \
		--range=$(SCALABILITY_TAS_RANGE_FILE)

.PHONY: test-tas-performance-scheduler
test-tas-performance-scheduler:
	ARTIFACTS="$(ARTIFACTS)/$@" ./hack/testing/performance-test.sh $(PERFORMANCE_RETRY_COUNT) test-tas-performance-scheduler-once

.PHONY: run-tas-performance-scheduler-in-cluster
run-tas-performance-scheduler-in-cluster: envtest performance-scheduler-runner
	mkdir -p "$(ARTIFACTS)/$@"
	KUBEBUILDER_ASSETS="$(KUBEBUILDER_ASSETS)" \
	$(SCALABILITY_RUNNER) \
		--o "$(ARTIFACTS)/$@" \
		--generatorConfig=$(SCALABILITY_TAS_GENERATOR_CONFIG) \
		--enableTAS=true \
		--qps=1000 --burst=2000 --timeout=25m $(SCALABILITY_SCRAPE_ARGS)

##@ Scheduler Performance Testing - Large Scale

SCALABILITY_LARGE_SCALE_GENERATOR_CONFIG ?= $(PROJECT_DIR)/test/performance/scheduler/configs/large-scale/generator.yaml
SCALABILITY_LARGE_SCALE_RANGE_FILE ?= $(PROJECT_DIR)/test/performance/scheduler/configs/large-scale/rangespec.yaml

.PHONY: run-large-scale-performance-scheduler
run-large-scale-performance-scheduler: envtest performance-scheduler-runner minimalkueue
	mkdir -p "$(ARTIFACTS)/$@"
	KUBEBUILDER_ASSETS="$(KUBEBUILDER_ASSETS)" \
	$(SCALABILITY_RUNNER) \
		--o "$(ARTIFACTS)/$@" \
		--crds=$(PROJECT_DIR)/config/components/crd/bases \
		--generatorConfig=$(SCALABILITY_LARGE_SCALE_GENERATOR_CONFIG) \
		--minimalKueue=$(MINIMALKUEUE_RUNNER) \
		--timeout=30m $(SCALABILITY_EXTRA_ARGS) $(SCALABILITY_SCRAPE_ARGS)

.PHONY: test-large-scale-performance-scheduler-once
test-large-scale-performance-scheduler-once: gotestsum run-large-scale-performance-scheduler
	$(GOTESTSUM) --junitfile $(ARTIFACTS)/junit.xml -- $(GO_TEST_FLAGS) ./test/performance/scheduler/checker  \
		--summary=$(ARTIFACTS)/run-large-scale-performance-scheduler/summary.yaml \
		--cmdStats=$(ARTIFACTS)/run-large-scale-performance-scheduler/minimalkueue.stats.yaml \
		--range=$(SCALABILITY_LARGE_SCALE_RANGE_FILE)

.PHONY: test-large-scale-performance-scheduler
test-large-scale-performance-scheduler:
	ARTIFACTS="$(ARTIFACTS)/$@" ./hack/testing/performance-test.sh $(PERFORMANCE_RETRY_COUNT) test-large-scale-performance-scheduler-once

.PHONY: run-large-scale-performance-scheduler-in-cluster
run-large-scale-performance-scheduler-in-cluster: envtest performance-scheduler-runner
	mkdir -p "$(ARTIFACTS)/$@"
	KUBEBUILDER_ASSETS="$(KUBEBUILDER_ASSETS)" \
	$(SCALABILITY_RUNNER) \
		--o "$(ARTIFACTS)/$@" \
		--generatorConfig=$(SCALABILITY_LARGE_SCALE_GENERATOR_CONFIG) \
		--qps=1000 --burst=2000 --timeout=30m $(SCALABILITY_SCRAPE_ARGS)

.PHONY: ginkgo-top
ginkgo-top:
	cd $(TOOLS_DIR) && $(NETWORK_INSTALL_RETRY) $(GO_CMD) mod download && \
	$(GO_BUILD_ENV) $(GO_CMD) build -ldflags="$(LD_FLAGS)" -o $(BIN_DIR)/ginkgo-top ./ginkgo-top

.PHONY: setup-e2e-env
setup-e2e-env: kustomize yq dep-crds kind helm ginkgo ginkgo-top kubectl ## Setup environment for e2e tests without running tests.
	@echo "Setting up environment for e2e tests"

.PHONY: test-e2e-kueueviz-local
test-e2e-kueueviz-local: setup-e2e-env ## Run end-to-end tests for kueueviz without running kueue tests.
	CYPRESS_SCREENSHOTS_FOLDER=$(ARTIFACTS)/cypress/screenshots CYPRESS_VIDEOS_FOLDER=$(ARTIFACTS)/cypress/videos \
	ARTIFACTS=$(ARTIFACTS) KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) PROJECT_DIR=$(PROJECT_DIR)/ \
	KIND_CLUSTER_FILE="kind-cluster.yaml" IMAGE_TAG=$(IMAGE_TAG) ${PROJECT_DIR}/hack/testing/e2e-kueueviz-local.sh

.PHONY: test-e2e-kueueviz
test-e2e-kueueviz: setup-e2e-env ## Run end-to-end tests for kueueviz without running kueue tests.
	@echo Starting kueueviz end to end test in containers
	CYPRESS_SCREENSHOTS_FOLDER=$(ARTIFACTS)/cypress/screenshots CYPRESS_VIDEOS_FOLDER=$(ARTIFACTS)/cypress/videos \
	ARTIFACTS=$(ARTIFACTS) KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) PROJECT_DIR=$(PROJECT_DIR)/ \
	KIND_CLUSTER_FILE="kind-cluster.yaml" IMAGE_TAG=$(IMAGE_TAG) \
	${PROJECT_DIR}/hack/testing/e2e-kueueviz-backend.sh

.PHONY: verify-ci-build-times
verify-ci-build-times: ## Verify that CI build times are below threshold.
	python3 ./hack/tools/prow-runtimes/prow_runtimes.py --kueue-presubmits --limit 5 --only-success --only-merge-pool --threshold-stat=second_longest --threshold 14m
