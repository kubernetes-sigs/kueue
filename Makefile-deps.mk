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
BIN_DIR ?= $(PROJECT_DIR)/bin
EXTERNAL_CRDS_DIR ?= $(PROJECT_DIR)/dep-crds

ifeq (,$(shell go env GOBIN))
	GOBIN=$(shell go env GOPATH)/bin
else
	GOBIN=$(shell go env GOBIN)
endif
GO_CMD ?= go

# Use go.mod go version as a single source of truth of Ginkgo version.
GINKGO_VERSION ?= $(shell $(GO_CMD) list -m -f '{{.Version}}' github.com/onsi/ginkgo/v2)

##@ Tools

GOLANGCI_LINT = $(PROJECT_DIR)/bin/golangci-lint
.PHONY: golangci-lint
golangci-lint: ## Download golangci-lint locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.57.2

CONTROLLER_GEN = $(PROJECT_DIR)/bin/controller-gen
.PHONY: controller-gen
controller-gen: ## Download controller-gen locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install sigs.k8s.io/controller-tools/cmd/controller-gen

KUSTOMIZE = $(PROJECT_DIR)/bin/kustomize
.PHONY: kustomize
kustomize: ## Download kustomize locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install sigs.k8s.io/kustomize/kustomize/v4@v4.5.7

ENVTEST = $(PROJECT_DIR)/bin/setup-envtest
.PHONY: envtest
envtest: ## Download envtest-setup locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install sigs.k8s.io/controller-runtime/tools/setup-envtest@v0.0.0-20240320141353-395cfc7486e6

GINKGO = $(PROJECT_DIR)/bin/ginkgo
.PHONY: ginkgo
ginkgo: ## Download ginkgo locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install github.com/onsi/ginkgo/v2/ginkgo@$(GINKGO_VERSION)

GOTESTSUM = $(PROJECT_DIR)/bin/gotestsum
.PHONY: gotestsum
gotestsum: ## Download gotestsum locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install gotest.tools/gotestsum@v1.8.2

KIND = $(PROJECT_DIR)/bin/kind
.PHONY: kind
kind: ## Download kind locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install sigs.k8s.io/kind@v0.20.0

YQ = $(PROJECT_DIR)/bin/yq
.PHONY: yq
yq: ## Download yq locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install github.com/mikefarah/yq/v4@v4.34.1

HELM = $(PROJECT_DIR)/bin/helm
.PHONY: helm
helm: ## Download helm locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin GO111MODULE=on $(GO_CMD) install helm.sh/helm/v3/cmd/helm@v3.12.1

GENREF = $(PROJECT_DIR)/bin/genref
.PHONY: genref
genref: ## Download genref locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin $(GO_CMD) install github.com/kubernetes-sigs/reference-docs/genref@v0.28.0

HUGO = $(PROJECT_DIR)/bin/hugo
.PHONY: hugo
hugo: ## Download hugo locally if necessary.
	@GOBIN=$(PROJECT_DIR)/bin CGO_ENABLED=1 $(GO_CMD) install -tags extended github.com/gohugoio/hugo@v0.124.1


##@ External CRDs

MPI_ROOT = $(shell $(GO_CMD) list -m -f "{{.Dir}}" github.com/kubeflow/mpi-operator)
.PHONY: mpi-operator-crd
mpi-operator-crd: ## Copy the CRDs from the mpi-operator to the dep-crds directory.
	mkdir -p $(EXTERNAL_CRDS_DIR)/mpi-operator/
	cp -f $(MPI_ROOT)/manifests/base/* $(EXTERNAL_CRDS_DIR)/mpi-operator/

KF_TRAINING_ROOT = $(shell $(GO_CMD) list -m -f "{{.Dir}}" github.com/kubeflow/training-operator)
.PHONY: kf-training-operator-crd
kf-training-operator-crd: ## Copy the CRDs from the training-operator to the dep-crds directory.
	mkdir -p $(EXTERNAL_CRDS_DIR)/training-operator/
	cp -f $(KF_TRAINING_ROOT)/manifests/base/crds/* $(EXTERNAL_CRDS_DIR)/training-operator/

RAY_ROOT = $(shell $(GO_CMD) list -m -f "{{.Dir}}" github.com/ray-project/kuberay/ray-operator)
.PHONY: ray-operator-crd
ray-operator-crd: ## Copy the CRDs from the ray-operator to the dep-crds directory.
	mkdir -p $(EXTERNAL_CRDS_DIR)/ray-operator/
	cp -f $(RAY_ROOT)/config/crd/bases/* $(EXTERNAL_CRDS_DIR)/ray-operator/

JOBSET_ROOT = $(shell $(GO_CMD) list -m -f "{{.Dir}}" sigs.k8s.io/jobset)
.PHONY: jobset-operator-crd
jobset-operator-crd: ## Copy the CRDs from the jobset-operator to the dep-crds directory.
	mkdir -p $(EXTERNAL_CRDS_DIR)/jobset-operator/
	cp -f $(JOBSET_ROOT)/config/components/crd/bases/* $(EXTERNAL_CRDS_DIR)/jobset-operator/

CLUSTER_AUTOSCALER_ROOT = $(shell $(GO_CMD) list -m -f "{{.Dir}}" k8s.io/autoscaler/cluster-autoscaler/apis)
.PHONY: cluster-autoscaler-crd
cluster-autoscaler-crd: ## Copy the CRDs from the cluster-autoscaler to the dep-crds directory.
	mkdir -p $(EXTERNAL_CRDS_DIR)/cluster-autoscaler/
	cp -f $(CLUSTER_AUTOSCALER_ROOT)/config/crd/* $(EXTERNAL_CRDS_DIR)/cluster-autoscaler/

.PHONY: dep-crds
dep-crds: mpi-operator-crd kf-training-operator-crd ray-operator-crd jobset-operator-crd cluster-autoscaler-crd ## Copy the CRDs from the external operators to the dep-crds directory.
	@echo "Copying CRDs from external operators to dep-crds directory"
