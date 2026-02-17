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
VERIFY_NPROCS ?= 8
# Output sync mode for parallel verification. Set to empty to disable.
# Requires GNU Make 4.0+. Values: target, line, recurse, or empty.
ifeq ($(shell uname),Darwin)
    VERIFY_OUTPUT_SYNC ?=
else
    VERIFY_OUTPUT_SYNC ?= target
endif
# Paths whose content is expected to be fully reproducible from sources.
# The final step of `make verify` enforces that these paths have:
# - no unstaged/staged diffs (`git diff --exit-code`)
# - no untracked files (e.g. newly generated files not added to git)
PATHS_TO_VERIFY := config/components apis charts/kueue client-go keps site/ netlify.toml $(MOCKS_DIR)

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
## - Output is grouped by target to make failures easier to find. Disable with `VERIFY_OUTPUT_SYNC= make verify`.
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
	$(MAKE) -j $(VERIFY_NPROCS) $(if $(VERIFY_OUTPUT_SYNC),--output-sync=$(VERIFY_OUTPUT_SYNC)) verify-checks
	git --no-pager diff --exit-code $(PATHS_TO_VERIFY)
	if git ls-files --exclude-standard --others $(PATHS_TO_VERIFY) | grep -q . ; then \
		echo "ERROR: untracked files found under: $(PATHS_TO_VERIFY)" >&2; \
		git ls-files --exclude-standard --others $(PATHS_TO_VERIFY) >&2; \
		exit 1; \
	fi

.PHONY: verify-go-prereqs
verify-go-prereqs: ## Prerequisites for Go-only checks.
verify-go-prereqs: generate-code generate-mocks

.PHONY: verify-docs-prereqs
verify-docs-prereqs: ## Prerequisites for docs/site checks.
verify-docs-prereqs: generate-apiref generate-kueuectl-docs generate-metrics-tables generate-featuregates sync-hugo-version toc-update

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
verify-checks: verify-ci-lint verify-lint-api verify-fmt-verify verify-shell-lint verify-helm-verify verify-helm-unit-test verify-npm-depcheck

# ---- Shared check recipes -------------------------------------------------
# Each recipe is stored in a variable so that both the lightweight standalone
# target (for local dev) and the verify-* wrapper (for `make verify`) share
# the exact same commands.  Only the prerequisites differ:
#
#   standalone  →  tool binary only          (fast, for local use)
#   verify-*    →  verify-tree-prereqs + …   (full generation first)
#
# A recipe-less wrapper like
#
#     verify-ci-lint: verify-tree-prereqs ci-lint   # WRONG – races under -j!
#
# lets ci-lint start while generate-code is still deleting / rewriting
# client-go/ files, because Make builds sibling prerequisites in parallel.
# Giving the wrapper its own recipe body is the only way to enforce ordering
# without recursive $(MAKE).

define _ci_lint_recipe
find . \( -path ./site -o -path ./bin -o -path ./vendor \) -prune -false -o -name go.mod -exec dirname {} \; | xargs -I {} sh -c 'cd "{}" && $(GOLANGCI_LINT) run $(GOLANGCI_LINT_FIX) --timeout 15m0s --config "$(PROJECT_DIR)/.golangci.yaml"'
endef

define _lint_api_recipe
$(GOLANGCI_LINT_KAL) run -v --config $(PROJECT_DIR)/.golangci-kal.yaml $(GOLANGCI_LINT_FIX)
endef

define _fmt_verify_recipe
@out=`$(GO_FMT) -l -d $$(find . \( -path ./vendor -o -path ./bin \) -prune -false -o -name '*.go' -print)`; \
if [ -n "$$out" ]; then \
    echo "$$out"; \
    exit 1; \
fi
endef

define _shell_lint_recipe
$(PROJECT_DIR)/hack/testing/shellcheck/verify.sh
endef

define _helm_verify_recipe
$(HELM) lint charts/kueue
$(HELM) template charts/kueue > /dev/null
$(HELM) template charts/kueue --set enableKueueViz=true --set enableCertManager=true --set enablePrometheus=true > /dev/null
$(HELM) template charts/kueue --set managerConfig.controllerManagerConfigYaml="managedJobsNamespaceSelector:\n  matchExpressions:\n    - key: kubernetes.io/metadata.name\n      operator: In\n      values: [ kube-system ]" > /dev/null
$(HELM) template charts/kueue --set controllerManager.manager.priorityClassName="system-cluster-critical" > /dev/null
$(HELM) template charts/kueue --set controllerManager.nodeSelector.nodetype=infra --set 'controllerManager.tolerations[0].key=node-role.kubernetes.io/master' --set 'controllerManager.tolerations[0].operator=Exists' --set 'controllerManager.tolerations[0].effect=NoSchedule' > /dev/null
$(HELM) template charts/kueue --set enableKueueViz=true --set kueueViz.backend.nodeSelector.nodetype=infra --set 'kueueViz.backend.tolerations[0].key=node-role.kubernetes.io/master' --set 'kueueViz.backend.tolerations[0].operator=Exists' --set 'kueueViz.backend.tolerations[0].effect=NoSchedule' > /dev/null
$(HELM) template charts/kueue --set enableKueueViz=true --set kueueViz.frontend.nodeSelector.nodetype=infra --set 'kueueViz.frontend.tolerations[0].key=node-role.kubernetes.io/master' --set 'kueueViz.frontend.tolerations[0].operator=Exists' --set 'kueueViz.frontend.tolerations[0].effect=NoSchedule' > /dev/null
$(HELM) template charts/kueue --set enableKueueViz=true --set kueueViz.backend.priorityClassName="system-cluster-critical" > /dev/null
$(HELM) template charts/kueue --set enableKueueViz=true --set kueueViz.backend.priorityClassName="system-cluster-critical" > /dev/null
endef

define _helm_unit_test_recipe
HELM_PLUGINS=$(BIN_DIR)/helm-plugins $(HELM) unittest charts/kueue --strict --debug
endef

define _npm_depcheck_recipe
$(PROJECT_DIR)/hack/testing/depcheck/verify.sh $(PROJECT_DIR)/cmd/kueueviz/frontend
$(PROJECT_DIR)/hack/testing/depcheck/verify.sh $(PROJECT_DIR)/test/e2e/kueueviz
endef

# ---- verify-* wrappers (generation prereqs + shared recipe) ---------------

.PHONY: verify-ci-lint
verify-ci-lint: verify-tree-prereqs gomod-verify golangci-lint ## CI-style golangci-lint (includes generation + go.mod checks)
	$(_ci_lint_recipe)

.PHONY: verify-lint-api
verify-lint-api: verify-tree-prereqs gomod-verify golangci-lint-kal ## CI-style API lint (includes generation + go.mod checks)
	$(_lint_api_recipe)

.PHONY: verify-fmt-verify
verify-fmt-verify: verify-tree-prereqs ## Verify formatting after generation
	$(_fmt_verify_recipe)

.PHONY: verify-shell-lint
verify-shell-lint: verify-tree-prereqs ## Shell lint after generation
	$(_shell_lint_recipe)

.PHONY: verify-helm-verify
verify-helm-verify: verify-tree-prereqs helm ## Helm verification after generation
	$(_helm_verify_recipe)

.PHONY: verify-helm-unit-test
verify-helm-unit-test: verify-tree-prereqs helm helm-unittest-plugin ## Helm unit tests after generation
	$(_helm_unit_test_recipe)

.PHONY: verify-npm-depcheck
verify-npm-depcheck: verify-tree-prereqs prepare-release-branch ## Depcheck after generation
	$(_npm_depcheck_recipe)

# ---- Standalone targets (lightweight, for local use) ----------------------

.PHONY: gomod-verify
gomod-verify: ## Verify go.mod / go.sum are tidy and unchanged.
gomod-verify: verify-go-prereqs
	$(GO_CMD) mod tidy
	git --no-pager diff --exit-code go.mod go.sum

.PHONY: ci-lint
ci-lint: ## Run golangci-lint across all Go modules.
ci-lint: golangci-lint
	$(_ci_lint_recipe)

.PHONY: lint-fix
lint-fix: ## Fix issues found by golangci-lint where possible.
lint-fix: GOLANGCI_LINT_FIX=--fix
lint-fix: ci-lint

.PHONY: lint-api
lint-api: golangci-lint-kal ## Run API-specific linting with custom config.
	$(_lint_api_recipe)

.PHONY: lint-api-fix
lint-api-fix: GOLANGCI_LINT_FIX=--fix
lint-api-fix: lint-api ## Fix API linting issues where possible.

.PHONY: fmt-verify
fmt-verify: ## Verify Go code formatting (no changes allowed).
	$(_fmt_verify_recipe)

.PHONY: shell-lint
shell-lint: ## Run shell script linting (via shellcheck).
	$(_shell_lint_recipe)

.PHONY: helm-verify
helm-verify: helm helm-lint ## Validate Helm chart rendering with various configuration combinations.
	$(_helm_verify_recipe)

.PHONY: helm-unit-test
helm-unit-test: helm helm-unittest-plugin ## Run Helm unit tests for the kueue chart.
	$(_helm_unit_test_recipe)

.PHONY: npm-depcheck
npm-depcheck: ## Verify frontend and e2e npm dependencies.
	$(_npm_depcheck_recipe)

.PHONY: verify-website-links
verify-website-links: ## Check for broken internal links on the public website.
	$(PROJECT_DIR)/hack/testing/linkchecker/verify.sh

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
