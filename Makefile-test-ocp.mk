include Makefile
include Makefile-deps.mk
include Makefile-test.mk

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

.PHONY: test-e2e-ocp
test-e2e-ocp: kustomize ginkgo yq gomod-download dep-crds kueuectl ginkgo-top run-test-e2e-ocp-singlecluster
run-test-e2e-ocp-singlecluster:
	@echo "Running e2e tests on OpenShift cluster ($(shell oc whoami --show-server))"
		ARTIFACTS="$(ARTIFACTS)/$@" IMAGE_TAG=$(IMAGE_TAG) GINKGO_ARGS="$(GINKGO_ARGS)" \
		E2E_TARGET_FOLDER="singlecluster" \
		./hack/e2e-test-ocp.sh
	$(PROJECT_DIR)/bin/ginkgo-top -i $(ARTIFACTS)/$@/e2e.json > $(ARTIFACTS)/$@/e2e-top.yaml

