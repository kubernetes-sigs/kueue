# Copyright 2026 The Kubernetes Authors.
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

##@ Verify

GO_FMT ?= gofmt
VERIFY_NPROCS ?= 1
# Paths whose content is expected to be fully reproducible from sources.
# The final step of `make verify` enforces that these paths have:
# - no unstaged/staged diffs (`git diff --exit-code`)
# - no untracked files (e.g. newly generated files not added to git)
PATHS_TO_VERIFY := config/components apis charts/kueue client-go site/ netlify.toml

.PHONY: verify
## Main target used by CI and local development.
##
## What it does:
## - Phase 1: regenerate everything that is checked into git (Go code, docs site data, Helm docs/manifests)
## - Phase 2: run verification checks (linters, formatting checks, helm rendering/unit tests, npm dep checks)
## - Phase 3: assert the repo is clean for $(PATHS_TO_VERIFY)
##
## Why it matters:
## A PR may compile locally while still missing generated artifacts (CRDs, docs, mocks, etc.).
## `make verify` is the "single command" that ensures you haven't forgotten to run generators and that
## the checked-in output matches what CI will expect.
##
## Notes:
## - The work is parallelized. Override parallelism with `VERIFY_NPROCS=<n> make verify`.
##
## How to extend `make verify`
##
## `make verify` is intentionally split into two broad phases:
## - `verify-tree-prereqs`: targets that *may write to the working tree* (codegen, docs generation, helm docs, etc.)
## - `verify-checks`: targets that should be *read-only* (linters, formatting verification, template rendering, unit tests)
##
## To add a new step:
## - If it GENERATES/UPDATES files checked into git: add it under one of the `verify-*-prereqs` targets.
## - If it ONLY VALIDATES without writing files: add it to `verify-checks`.
##
## Implementation location:
## - You can define the target in this file (`Makefile-verify.mk`) if it’s verify-specific,
##   or in another included fragment (`Makefile-test.mk`, etc.) if it logically belongs there.
## - Then, wire it into the appropriate aggregator target below.
verify: ## Ensure repo is clean after generation/formatting.
	$(MAKE) -j $(VERIFY_NPROCS) verify-tree-prereqs verify-checks
	git --no-pager diff --exit-code $(PATHS_TO_VERIFY)
	if git ls-files --exclude-standard --others $(PATHS_TO_VERIFY) | grep -q . ; then \
		echo "ERROR: untracked files found under: $(PATHS_TO_VERIFY)" >&2; \
		git ls-files --exclude-standard --others $(PATHS_TO_VERIFY) >&2; \
		exit 1; \
	fi

.PHONY: verify-go-prereqs
verify-go-prereqs: ## Prerequisites for Go-only checks.
verify-go-prereqs: gomod-verify generate-code generate-mocks

.PHONY: verify-docs-prereqs
verify-docs-prereqs: ## Prerequisites for docs/site checks.
verify-docs-prereqs: generate-apiref generate-kueuectl-docs generate-metrics-tables generate-featuregates sync-hugo-version

.PHONY: verify-helm-prereqs
verify-helm-prereqs: ## Prerequisites for Helm checks.
verify-helm-prereqs: compile-crd-manifests update-helm generate-helm-docs prepare-release-branch

.PHONY: verify-tree-prereqs
verify-tree-prereqs: ## Prerequisites to ensure repo is fully regenerated.
verify-tree-prereqs: verify-go-prereqs verify-docs-prereqs verify-helm-prereqs

.PHONY: verify-checks
## Read-only verification targets that should not mutate the repo.
## Add new check-only targets here.
verify-checks: ## Phase 2 (parallel): checks that should run after generation completes.
verify-checks: ci-lint lint-api fmt-verify shell-lint toc-verify helm-verify helm-unit-test npm-depcheck

.PHONY: gomod-verify
gomod-verify: ## Verify go.mod / go.sum are tidy and unchanged.
	$(GO_CMD) mod tidy
	git --no-pager diff --exit-code go.mod go.sum

.PHONY: ci-lint
ci-lint: verify-go-prereqs golangci-lint ## Run golangci-lint across all Go modules.
	find . \( -path ./site -o -path ./bin -o -path ./vendor \) -prune -false -o -name go.mod -exec dirname {} \; | xargs -I {} sh -c 'cd "{}" && $(GOLANGCI_LINT) run $(GOLANGCI_LINT_FIX) --timeout 15m0s --config "$(PROJECT_DIR)/.golangci.yaml"'

.PHONY: lint-fix
lint-fix: GOLANGCI_LINT_FIX=--fix
lint-fix: ci-lint ## Fix issues found by golangci-lint where possible.

.PHONY: lint-api
lint-api: verify-go-prereqs golangci-lint-kal ## Run API-specific linting with custom config.
	$(GOLANGCI_LINT_KAL) run -v --config $(PROJECT_DIR)/.golangci-kal.yml ${GOLANGCI_LINT_FIX}

.PHONY: lint-api-fix
lint-api-fix: GOLANGCI_LINT_FIX=--fix
lint-api-fix: lint-api ## Fix API linting issues where possible.

.PHONY: fmt-verify
fmt-verify: verify-go-prereqs ## Verify Go code formatting (no changes allowed).
	@out=`$(GO_FMT) -l -d $$(find . \( -path ./vendor -o -path ./bin \) -prune -false -o -name '*.go' -print)`; \
	if [ -n "$$out" ]; then \
	    echo "$$out"; \
	    exit 1; \
	fi

.PHONY: shell-lint
shell-lint: ## Run shell script linting (via shellcheck).
	$(PROJECT_DIR)/hack/shellcheck/verify.sh

.PHONY: toc-verify
toc-verify: verify-docs-prereqs mdtoc ## Verify markdown TOCs are up-to-date.
	./hack/verify-toc.sh

.PHONY: helm-verify
helm-verify: verify-helm-prereqs helm helm-lint ## Validate Helm chart rendering with various configuration combinations.
# test default values
	$(HELM) template charts/kueue > /dev/null
# test nondefault options (kueueviz, prometheus, certmanager)
	$(HELM) template charts/kueue --set enableKueueViz=true --set enableCertManager=true --set enablePrometheus=true > /dev/null
# test added managedJobsNamespaceSelector option
	$(HELM) template charts/kueue --set managerConfig.controllerManagerConfigYaml="managedJobsNamespaceSelector:\n  matchExpressions:\n    - key: kubernetes.io/metadata.name\n      operator: In\n      values: [ kube-system ]" > /dev/null
# test priorityClassName option
	$(HELM) template charts/kueue --set controllerManager.manager.priorityClassName="system-cluster-critical" > /dev/null
# test controllerManager nodeSelector and tolerations
	$(HELM) template charts/kueue --set controllerManager.nodeSelector.nodetype=infra --set 'controllerManager.tolerations[0].key=node-role.kubernetes.io/master' --set 'controllerManager.tolerations[0].operator=Exists' --set 'controllerManager.tolerations[0].effect=NoSchedule' > /dev/null
# test kueueViz backend nodeSelector and tolerations
	$(HELM) template charts/kueue --set enableKueueViz=true --set kueueViz.backend.nodeSelector.nodetype=infra --set 'kueueViz.backend.tolerations[0].key=node-role.kubernetes.io/master' --set 'kueueViz.backend.tolerations[0].operator=Exists' --set 'kueueViz.backend.tolerations[0].effect=NoSchedule' > /dev/null
# test kueueViz frontend nodeSelector and tolerations
	$(HELM) template charts/kueue --set enableKueueViz=true --set kueueViz.frontend.nodeSelector.nodetype=infra --set 'kueueViz.frontend.tolerations[0].key=node-role.kubernetes.io/master' --set 'kueueViz.frontend.tolerations[0].operator=Exists' --set 'kueueViz.frontend.tolerations[0].effect=NoSchedule' > /dev/null
# test kueueViz priorityClassName options for backend
	$(HELM) template charts/kueue --set enableKueueViz=true --set kueueViz.backend.priorityClassName="system-cluster-critical" > /dev/null
# test kueueViz priorityClassName options for frontend
	$(HELM) template charts/kueue --set enableKueueViz=true --set kueueViz.backend.priorityClassName="system-cluster-critical" > /dev/null

.PHONY: helm-unit-test
helm-unit-test: verify-helm-prereqs helm helm-unittest-plugin ## Run Helm unit tests for the kueue chart.
	HELM_PLUGINS=$(BIN_DIR)/helm-plugins $(HELM) unittest charts/kueue --strict --debug

.PHONY: npm-depcheck
npm-depcheck: prepare-release-branch ## Verify frontend and e2e npm dependencies.
	$(PROJECT_DIR)/hack/depcheck/verify.sh $(PROJECT_DIR)/cmd/kueueviz/frontend
	$(PROJECT_DIR)/hack/depcheck/verify.sh $(PROJECT_DIR)/test/e2e/kueueviz

.PHONY: i18n-verify
i18n-verify: ## Verify localized docs are in sync with English. Usage: make i18n-verify [TARGET_LANG=zh-CN]
	@if [ -n "$(TARGET_LANG)" ]; then \
		if [ ! -d "$(PROJECT_DIR)/site/content/$(TARGET_LANG)/docs" ]; then \
			echo "Error: $(PROJECT_DIR)/site/content/$(TARGET_LANG)/docs does not exist"; \
			exit 1; \
		fi; \
		echo "Checking $(TARGET_LANG) docs sync status..."; \
		$(PROJECT_DIR)/site/scripts/lsync.sh "$(PROJECT_DIR)/site/content/$(TARGET_LANG)/docs/"; \
	else \
		for lang_dir in $(PROJECT_DIR)/site/content/*/; do \
			lang_code=$$(basename "$$lang_dir"); \
			if [ "$$lang_code" != "en" ] && [ -d "$$lang_dir/docs" ]; then \
				echo "Checking $$lang_code docs sync status..."; \
				$(PROJECT_DIR)/site/scripts/lsync.sh "$(PROJECT_DIR)/site/content/$$lang_code/docs/"; \
			fi; \
		done; \
	fi
