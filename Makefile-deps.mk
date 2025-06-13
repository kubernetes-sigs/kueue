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

EXTERNAL_CRDS_DIR ?= $(PROJECT_DIR)/dep-crds

# Use go.mod go version as source.
GINKGO_VERSION ?= $(shell cd $(TOOLS_DIR); $(GO_CMD) list -m -f '{{.Version}}' github.com/onsi/ginkgo/v2)
GOLANGCI_LINT_VERSION ?= $(shell cd $(TOOLS_DIR); $(GO_CMD) list -m -f '{{.Version}}' github.com/golangci/golangci-lint/v2)
CONTROLLER_GEN_VERSION ?= $(shell cd $(TOOLS_DIR); $(GO_CMD) list -m -f '{{.Version}}' sigs.k8s.io/controller-tools)
KUSTOMIZE_VERSION ?= $(shell cd $(TOOLS_DIR); $(GO_CMD) list -m -f '{{.Version}}' sigs.k8s.io/kustomize/kustomize/v5)
ENVTEST_VERSION ?= $(shell cd $(TOOLS_DIR); $(GO_CMD) list -m -f '{{.Version}}' sigs.k8s.io/controller-runtime/tools/setup-envtest)
GOTESTSUM_VERSION ?= $(shell cd $(TOOLS_DIR); $(GO_CMD) list -m -f '{{.Version}}' gotest.tools/gotestsum)
KIND_VERSION ?= $(shell cd $(TOOLS_DIR); $(GO_CMD) list -m -f '{{.Version}}' sigs.k8s.io/kind)
YQ_VERSION ?= $(shell cd $(TOOLS_DIR); $(GO_CMD) list -m -f '{{.Version}}' github.com/mikefarah/yq/v4)
HELM_VERSION ?= $(shell cd $(TOOLS_DIR); $(GO_CMD) list -m -f '{{.Version}}' helm.sh/helm/v3)
GENREF_VERSION = $(shell cd $(TOOLS_DIR); $(GO_CMD) list -m -f '{{.Version}}' github.com/kubernetes-sigs/reference-docs/genref)
HUGO_VERSION ?= $(shell cd $(TOOLS_DIR); $(GO_CMD) list -m -f '{{.Version}}' github.com/gohugoio/hugo)
MDTOC_VERSION ?= $(shell cd $(TOOLS_DIR); $(GO_CMD) list -m -f '{{.Version}}' sigs.k8s.io/mdtoc)
HELM_DOCS_VERSION ?= $(shell cd $(TOOLS_DIR); $(GO_CMD) list -m -f '{{.Version}}' github.com/norwoodj/helm-docs)

# Versions for external controllers
JOBSET_VERSION = $(shell $(GO_CMD) list -m -f "{{.Version}}" sigs.k8s.io/jobset)
KUBEFLOW_VERSION = $(shell $(GO_CMD) list -m -f "{{.Version}}" github.com/kubeflow/training-operator)
KUBEFLOW_MPI_VERSION = $(shell $(GO_CMD) list -m -f "{{.Version}}" github.com/kubeflow/mpi-operator)
KUBERAY_VERSION = $(shell $(GO_CMD) list -m -f "{{.Version}}" github.com/ray-project/kuberay/ray-operator)
APPWRAPPER_VERSION = $(shell $(GO_CMD) list -m -f "{{.Version}}" github.com/project-codeflare/appwrapper)
LEADERWORKERSET_VERSION = $(shell $(GO_CMD) list -m -f "{{.Version}}" sigs.k8s.io/lws)
CERTMANAGER_VERSION=$(shell $(GO_CMD) list -m -f "{{.Version}}" github.com/cert-manager/cert-manager)

GOLANGCI_LINT = $(BIN_DIR)/golangci-lint
CONTROLLER_GEN = $(BIN_DIR)/controller-gen
KUSTOMIZE = $(BIN_DIR)/kustomize
GINKGO = $(BIN_DIR)/ginkgo
GOTESTSUM = $(BIN_DIR)/gotestsum
KIND = $(BIN_DIR)/kind
ENVTEST = $(BIN_DIR)/setup-envtest
YQ = $(BIN_DIR)/yq
HELM = $(BIN_DIR)/helm
GENREF = $(BIN_DIR)/genref
HUGO = $(BIN_DIR)/hugo
MDTOC = $(BIN_DIR)/mdtoc
HELM_DOCS = $(BIN_DIR)/helm-docs

MPI_ROOT = $(shell $(GO_CMD) list -m -mod=readonly -f "{{.Dir}}" github.com/kubeflow/mpi-operator)
KF_TRAINING_ROOT = $(shell $(GO_CMD) list -m -mod=readonly -f "{{.Dir}}" github.com/kubeflow/training-operator)
RAY_ROOT = $(shell $(GO_CMD) list -m -mod=readonly -f "{{.Dir}}" github.com/ray-project/kuberay/ray-operator)
JOBSET_ROOT = $(shell $(GO_CMD) list -m -mod=readonly -f "{{.Dir}}" sigs.k8s.io/jobset)
CLUSTER_AUTOSCALER_ROOT = $(shell $(GO_CMD) list -m -mod=readonly -f "{{.Dir}}" k8s.io/autoscaler/cluster-autoscaler/apis)
APPWRAPPER_ROOT = $(shell $(GO_CMD) list -m -mod=readonly -f "{{.Dir}}" github.com/project-codeflare/appwrapper)
LEADERWORKERSET_ROOT = $(shell $(GO_CMD) list -m -mod=readonly -f "{{.Dir}}" sigs.k8s.io/lws)

##@ Tools

.PHONY: golangci-lint
golangci-lint: ## Download golangci-lint locally if necessary.
	@GOBIN=$(BIN_DIR) GO111MODULE=on $(GO_CMD) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

.PHONY: controller-gen
controller-gen: ## Download controller-gen locally if necessary.
	@GOBIN=$(BIN_DIR) GO111MODULE=on $(GO_CMD) install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

.PHONY: kustomize
kustomize: ## Download kustomize locally if necessary.
	@GOBIN=$(BIN_DIR) GO111MODULE=on $(GO_CMD) install sigs.k8s.io/kustomize/kustomize/v5@$(KUSTOMIZE_VERSION)

.PHONY: envtest
envtest: ## Download envtest-setup locally if necessary.
	@GOBIN=$(BIN_DIR) GO111MODULE=on $(GO_CMD) install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_VERSION)

.PHONY: ginkgo
ginkgo: ## Download ginkgo locally if necessary.
	@GOBIN=$(BIN_DIR) GO111MODULE=on $(GO_CMD) install github.com/onsi/ginkgo/v2/ginkgo@$(GINKGO_VERSION)

.PHONY: gotestsum
gotestsum: ## Download gotestsum locally if necessary.
	@GOBIN=$(BIN_DIR) GO111MODULE=on $(GO_CMD) install gotest.tools/gotestsum@$(GOTESTSUM_VERSION)

.PHONY: kind
kind: ## Download kind locally if necessary.
	@GOBIN=$(BIN_DIR) GO111MODULE=on $(GO_CMD) install sigs.k8s.io/kind@$(KIND_VERSION)

.PHONY: yq
yq: ## Download yq locally if necessary.
	@GOBIN=$(BIN_DIR) GO111MODULE=on $(GO_CMD) install github.com/mikefarah/yq/v4@$(YQ_VERSION)

.PHONY: helm
helm: ## Download helm and helm-unittest locally if necessary.
	@GOBIN=$(BIN_DIR) GO111MODULE=on $(GO_CMD) install helm.sh/helm/v3/cmd/helm@$(HELM_VERSION)
	@if ! $(HELM) plugin list | grep -q unittest; then \
		$(HELM) plugin install https://github.com/helm-unittest/helm-unittest.git; \
	fi

.PHONY: genref
genref: ## Download genref locally if necessary.
	@GOBIN=$(BIN_DIR) $(GO_CMD) install github.com/kubernetes-sigs/reference-docs/genref@$(GENREF_VERSION)

.PHONY: hugo
hugo: ## Download hugo locally if necessary.
	@GOBIN=$(BIN_DIR) CGO_ENABLED=1 $(GO_CMD) install -tags extended github.com/gohugoio/hugo@$(HUGO_VERSION)

.PHONY: mdtoc
mdtoc: ## Download mdtoc locally if necessary.
	@GOBIN=$(BIN_DIR) CGO_ENABLED=1 $(GO_CMD) install sigs.k8s.io/mdtoc@$(MDTOC_VERSION)

.PHONY: helm-docs
helm-docs: ## Download helm-docs locally if necessary.
	@GOBIN=$(BIN_DIR) CGO_ENABLED=1 $(GO_CMD) install github.com/norwoodj/helm-docs/cmd/helm-docs@$(HELM_DOCS_VERSION)

##@ External CRDs

.PHONY: mpi-operator-crd
mpi-operator-crd: ## Copy the CRDs from the mpi-operator to the dep-crds directory.
	mkdir -p $(EXTERNAL_CRDS_DIR)/mpi-operator/
	cp -f $(MPI_ROOT)/manifests/base/* $(EXTERNAL_CRDS_DIR)/mpi-operator/

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

.PHONY: ray-operator-crd
ray-operator-crd: ## Copy the CRDs from the ray-operator to the dep-crds directory.
	mkdir -p $(EXTERNAL_CRDS_DIR)/ray-operator-crds/
	cp -f $(RAY_ROOT)/config/crd/bases/* $(EXTERNAL_CRDS_DIR)/ray-operator-crds/

.PHONY: ray-operator-manifests
ray-operator-manifests: ## Copy the whole manifests content from the ray-operator to the dep-crds directory.
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


.PHONY: jobset-operator-crd
jobset-operator-crd: ## Copy the CRDs from the jobset-operator to the dep-crds directory.
	mkdir -p $(EXTERNAL_CRDS_DIR)/jobset-operator/
	cp -f $(JOBSET_ROOT)/config/components/crd/bases/* $(EXTERNAL_CRDS_DIR)/jobset-operator/

.PHONY: cluster-autoscaler-crd
cluster-autoscaler-crd: ## Copy the CRDs from the cluster-autoscaler to the dep-crds directory.
	mkdir -p $(EXTERNAL_CRDS_DIR)/cluster-autoscaler/
	cp -f $(CLUSTER_AUTOSCALER_ROOT)/config/crd/* $(EXTERNAL_CRDS_DIR)/cluster-autoscaler/

.PHONY: appwrapper-crd
appwrapper-crd: ## Copy the CRDs from the appwrapper to the dep-crds directory.
	mkdir -p $(EXTERNAL_CRDS_DIR)/appwrapper-crds/
	cp -f $(APPWRAPPER_ROOT)/config/crd/bases/* $(EXTERNAL_CRDS_DIR)/appwrapper-crds/

.PHONY: appwrapper-manifests
appwrapper-manifests: kustomize ## Copy whole manifests folder from the appwrapper controller to the dep-crds directory.
	## Full version of the manifest required for e2e tests.
	if [ -d "$(EXTERNAL_CRDS_DIR)/appwrapper" ]; then \
		chmod -R u+w "$(EXTERNAL_CRDS_DIR)/appwrapper" && \
		rm -rf "$(EXTERNAL_CRDS_DIR)/appwrapper"; \
	fi
	mkdir -p "$(EXTERNAL_CRDS_DIR)/appwrapper"
	cp -rf "$(APPWRAPPER_ROOT)/config" "$(EXTERNAL_CRDS_DIR)/appwrapper"
	cd "$(EXTERNAL_CRDS_DIR)/appwrapper/config/manager" && chmod u+w kustomization.yaml && $(KUSTOMIZE) edit set image controller=quay.io/ibm/appwrapper:${APPWRAPPER_VERSION} && chmod u-w kustomization.yaml

.PHONY: leaderworkerset-operator-crd
leaderworkerset-operator-crd: ## Copy the CRDs from the leaderworkerset-operator to the dep-crds directory.
	mkdir -p $(EXTERNAL_CRDS_DIR)/leaderworkerset-operator/
	cp -f $(LEADERWORKERSET_ROOT)/config/crd/bases/* $(EXTERNAL_CRDS_DIR)/leaderworkerset-operator/

.PHONY: dep-crds
dep-crds: mpi-operator-crd kf-training-operator-crd ray-operator-crd jobset-operator-crd leaderworkerset-operator-crd cluster-autoscaler-crd appwrapper-crd appwrapper-manifests kf-training-operator-manifests ray-operator-manifests## Copy the CRDs from the external operators to the dep-crds directory.
	@echo "Copying CRDs from external operators to dep-crds directory"

.PHONY: kueuectl-docs
kueuectl-docs:
	$(GO_BUILD_ENV) $(GO_CMD) build -ldflags="$(LD_FLAGS)" -o $(BIN_DIR)/kueuectl-docs ./cmd/kueuectl-docs/main.go
