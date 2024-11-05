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
TOOLS_DIR := $(PROJECT_DIR)/hack/internal/tools
BIN_DIR ?= $(PROJECT_DIR)/bin
EXTERNAL_CRDS_DIR ?= $(PROJECT_DIR)/dep-crds

ifeq (,$(shell go env GOBIN))
	GOBIN=$(shell go env GOPATH)/bin
else
	GOBIN=$(shell go env GOBIN)
endif
GO_CMD ?= go

# Use go.mod go version as source.
GINKGO_VERSION ?= $(shell cd $(TOOLS_DIR); $(GO_CMD) list -m -f '{{.Version}}' github.com/onsi/ginkgo/v2)
GOLANGCI_LINT_VERSION ?= $(shell cd $(TOOLS_DIR); $(GO_CMD) list -m -f '{{.Version}}' github.com/golangci/golangci-lint)
CONTROLLER_GEN_VERSION ?= $(shell cd $(TOOLS_DIR); $(GO_CMD) list -m -f '{{.Version}}' sigs.k8s.io/controller-tools)
KUSTOMIZE_VERSION ?= $(shell cd $(TOOLS_DIR); $(GO_CMD) list -m -f '{{.Version}}' sigs.k8s.io/kustomize/kustomize/v5)
ENVTEST_VERSION ?= $(shell cd $(TOOLS_DIR); $(GO_CMD) list -m -f '{{.Version}}' sigs.k8s.io/controller-runtime/tools/setup-envtest)
GOTESTSUM_VERSION ?= $(shell cd $(TOOLS_DIR); $(GO_CMD) list -m -f '{{.Version}}' gotest.tools/gotestsum)
KIND_VERSION ?= $(shell cd $(TOOLS_DIR); $(GO_CMD) list -m -f '{{.Version}}' sigs.k8s.io/kind)
YQ_VERSION ?= $(shell cd $(TOOLS_DIR); $(GO_CMD) list -m -f '{{.Version}}' github.com/mikefarah/yq/v4)
HELM_VERSION ?= $(shell cd $(TOOLS_DIR); $(GO_CMD) list -m -f '{{.Version}}' helm.sh/helm/v3)
HUGO_VERSION ?= $(shell cd $(TOOLS_DIR); $(GO_CMD) list -m -f '{{.Version}}' github.com/gohugoio/hugo)
MDTOC_VERSION ?= $(shell cd $(TOOLS_DIR); $(GO_CMD) list -m -f '{{.Version}}' sigs.k8s.io/mdtoc)

##@ Tools

GOLANGCI_LINT = $(PROJECT_DIR)/bin/golangci-lint
.PHONY: golangci-lint
golangci-lint: ## Download golangci-lint locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

CONTROLLER_GEN = $(PROJECT_DIR)/bin/controller-gen
.PHONY: controller-gen
controller-gen: ## Download controller-gen locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

KUSTOMIZE = $(PROJECT_DIR)/bin/kustomize
.PHONY: kustomize
kustomize: ## Download kustomize locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install sigs.k8s.io/kustomize/kustomize/v5@$(KUSTOMIZE_VERSION)

ENVTEST = $(PROJECT_DIR)/bin/setup-envtest
.PHONY: envtest
envtest: ## Download envtest-setup locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_VERSION)

GINKGO = $(PROJECT_DIR)/bin/ginkgo
.PHONY: ginkgo
ginkgo: ## Download ginkgo locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install github.com/onsi/ginkgo/v2/ginkgo@$(GINKGO_VERSION)

GOTESTSUM = $(PROJECT_DIR)/bin/gotestsum
.PHONY: gotestsum
gotestsum: ## Download gotestsum locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install gotest.tools/gotestsum@$(GOTESTSUM_VERSION)

KIND = $(PROJECT_DIR)/bin/kind
.PHONY: kind
kind: ## Download kind locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install sigs.k8s.io/kind@$(KIND_VERSION)

YQ = $(PROJECT_DIR)/bin/yq
.PHONY: yq
yq: ## Download yq locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install github.com/mikefarah/yq/v4@$(YQ_VERSION)

HELM = $(PROJECT_DIR)/bin/helm
.PHONY: helm
helm: ## Download helm locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install helm.sh/helm/v3/cmd/helm@$(HELM_VERSION)

GENREF = $(PROJECT_DIR)/bin/genref
.PHONY: genref
genref: ## Download genref locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin $(GO_CMD) install github.com/kubernetes-sigs/reference-docs/genref@v0.28.0

HUGO = $(PROJECT_DIR)/bin/hugo
.PHONY: hugo
hugo: ## Download hugo locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin CGO_ENABLED=1 $(GO_CMD) install -tags extended github.com/gohugoio/hugo@$(HUGO_VERSION)

MDTOC = $(PROJECT_DIR)/bin/mdtoc
.PHONY: mdtoc
mdtoc: ## Download mdtoc locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin CGO_ENABLED=1 $(GO_CMD) install sigs.k8s.io/mdtoc@$(MDTOC_VERSION)


##@ External CRDs

MPI_ROOT = $(shell $(GO_CMD) list -m -mod=readonly -f "{{.Dir}}" github.com/kubeflow/mpi-operator)
.PHONY: mpi-operator-crd
mpi-operator-crd: ## Copy the CRDs from the mpi-operator to the dep-crds directory.
	mkdir -p $(EXTERNAL_CRDS_DIR)/mpi-operator/
	cp -f $(MPI_ROOT)/manifests/base/* $(EXTERNAL_CRDS_DIR)/mpi-operator/

KF_TRAINING_ROOT = $(shell $(GO_CMD) list -m -mod=readonly -f "{{.Dir}}" github.com/kubeflow/training-operator)
.PHONY: kf-training-operator-crd
kf-training-operator-crd: ## Copy the CRDs from the training-operator to the dep-crds directory.
	## Removing kubeflow.org_mpijobs.yaml is required as the version of MPIJob is conflicting between training-operator and mpi-operator - in integration tests.
	mkdir -p $(EXTERNAL_CRDS_DIR)/training-operator-crds/
	find $(KF_TRAINING_ROOT)/manifests/base/crds/* -type f -not -name "kubeflow.org_mpijobs.yaml" -exec cp -pf {} $(EXTERNAL_CRDS_DIR)/training-operator-crds/ \;

.PHONY: kf-training-operator-manifests
kf-training-operator-manifests: ## Copy whole manifests folder from the training-operator to the dep-crds directory.
	## Full version of the manifest is required for e2e multikueue tests.
	if [ -d "$(EXTERNAL_CRDS_DIR)/training-operator" ]; then \
		chmod -R u+w "$(EXTERNAL_CRDS_DIR)/training-operator" && \
		rm -rf "$(EXTERNAL_CRDS_DIR)/training-operator"; \
	fi
	mkdir -p "$(EXTERNAL_CRDS_DIR)/training-operator"
	cp -rf "$(KF_TRAINING_ROOT)/manifests" "$(EXTERNAL_CRDS_DIR)/training-operator"

RAY_ROOT = $(shell $(GO_CMD) list -m -mod=readonly -f "{{.Dir}}" github.com/ray-project/kuberay/ray-operator)
.PHONY: ray-operator-crd
ray-operator-crd: ## Copy the CRDs from the ray-operator to the dep-crds directory.
	mkdir -p $(EXTERNAL_CRDS_DIR)/ray-operator/
	cp -f $(RAY_ROOT)/config/crd/bases/* $(EXTERNAL_CRDS_DIR)/ray-operator/

JOBSET_ROOT = $(shell $(GO_CMD) list -m -mod=readonly -f "{{.Dir}}" sigs.k8s.io/jobset)
.PHONY: jobset-operator-crd
jobset-operator-crd: ## Copy the CRDs from the jobset-operator to the dep-crds directory.
	mkdir -p $(EXTERNAL_CRDS_DIR)/jobset-operator/
	cp -f $(JOBSET_ROOT)/config/components/crd/bases/* $(EXTERNAL_CRDS_DIR)/jobset-operator/

CLUSTER_AUTOSCALER_ROOT = $(shell $(GO_CMD) list -m -mod=readonly -f "{{.Dir}}" k8s.io/autoscaler/cluster-autoscaler/apis)
.PHONY: cluster-autoscaler-crd
cluster-autoscaler-crd: ## Copy the CRDs from the cluster-autoscaler to the dep-crds directory.
	mkdir -p $(EXTERNAL_CRDS_DIR)/cluster-autoscaler/
	cp -f $(CLUSTER_AUTOSCALER_ROOT)/config/crd/* $(EXTERNAL_CRDS_DIR)/cluster-autoscaler/

.PHONY: dep-crds
dep-crds: mpi-operator-crd kf-training-operator-crd ray-operator-crd jobset-operator-crd cluster-autoscaler-crd kf-training-operator-manifests ## Copy the CRDs from the external operators to the dep-crds directory.
	@echo "Copying CRDs from external operators to dep-crds directory"

.PHONY: kueuectl-docs
kueuectl-docs:
	$(GO_BUILD_ENV) $(GO_CMD) build -ldflags="$(LD_FLAGS)" -o $(PROJECT_DIR)/bin/kueuectl-docs ./cmd/kueuectl-docs/main.go