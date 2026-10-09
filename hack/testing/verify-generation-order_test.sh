#!/usr/bin/env bash

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

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
TEST_DIR=$(mktemp -d)
trap 'rm -r "$TEST_DIR"' EXIT
ORDER_PROJECT_DIR=$(cd "$SCRIPT_DIR/../.." && pwd)
export ORDER_PROJECT_DIR
export ORDER_STEP="$TEST_DIR/step.sh"

cat > "$ORDER_STEP" <<'EOF'
#!/usr/bin/env bash
set -o errexit
set -o nounset
case "$1" in
  lint | artifacts | cli)
    touch "$ORDER_STATE/$1-started"
    ;;
esac
case "$1" in
  generate-code | generate-mocks | gomod | update-helm)
    # Keep writers active long enough to catch a missing Make prerequisite.
    sleep 0.1
    ;;
  prepare-release-branch)
    test -f "$ORDER_STATE/helm-docs"
    touch "$ORDER_STATE/generate-helm-docs"
    ;;
  generate-apiref)
    # Require the independent checks to finish while site generation is running.
    deadline=$((SECONDS + 5))
    until [[ -f "$ORDER_STATE/lint" && -f "$ORDER_STATE/artifacts" ]]; do
      if ((SECONDS >= deadline)); then
        echo "Independent checks waited for site generation" >&2
        exit 1
      fi
      sleep 0.05
    done
    ;;
  lint)
    for step in generate-code generate-mocks gomod gomod-verified; do
      test -f "$ORDER_STATE/$step"
    done
    ;;
  cli)
    for step in generate-code generate-mocks gomod gomod-verified verify-git-tag; do
      test -f "$ORDER_STATE/$step"
    done
    ;;
  artifacts)
    for step in generate-code generate-mocks gomod gomod-verified compile-crd-manifests update-helm generate-helm-docs prepare-release-branch; do
      test -f "$ORDER_STATE/$step"
    done
    ;;
esac
if [[ "$1" == "$ORDER_FAIL_STAGE" ]]; then
  exit 1
fi
touch "$ORDER_STATE/$1"
EOF

cat > "$TEST_DIR/go" <<'EOF'
#!/usr/bin/env bash
bash "$ORDER_STEP" gomod
EOF
chmod +x "$TEST_DIR/go"
cat > "$TEST_DIR/git" <<'EOF'
#!/usr/bin/env bash
bash "$ORDER_STEP" gomod-verified
EOF
chmod +x "$TEST_DIR/git"
export PATH="$TEST_DIR:$PATH"

# Load the real wrappers; replace only expensive leaf commands and check recipes.
cat > "$TEST_DIR/Makefile" <<'EOF'
PROJECT_DIR := $(ORDER_PROJECT_DIR)
TESTING_DIR := $(PROJECT_DIR)/hack/testing
GO_CMD := $(dir $(ORDER_STEP))go
include $(PROJECT_DIR)/hack/make/verify.mk
_ci_lint_recipe = bash "$(ORDER_STEP)" lint
_helm_chart_package_recipe = bash "$(ORDER_STEP)" helm-package
_prepare_manifests_recipe = bash "$(ORDER_STEP)" prepare-manifests
_artifacts_recipe = bash "$(ORDER_STEP)" artifacts
_cli_artifacts_recipe = bash "$(ORDER_STEP)" cli
.PHONY: all generate-code generate-mocks generate-apiref generate-kueuectl-docs generate-metrics-tables generate-featuregates sync-hugo-version toc-update compile-crd-manifests update-helm prepare-release-branch golangci-lint verify-git-tag clean-artifacts kustomize helm yq helm-docs
all: verify-ci-lint verify-artifacts verify-tree-prereqs
prepare-release-branch: helm-docs
generate-code generate-mocks generate-apiref generate-kueuectl-docs generate-metrics-tables generate-featuregates sync-hugo-version toc-update compile-crd-manifests update-helm prepare-release-branch golangci-lint verify-git-tag clean-artifacts kustomize helm yq helm-docs:
	bash "$(ORDER_STEP)" "$@"
EOF

for fail_stage in '' generate-code update-helm gomod gomod-verified; do
  export ORDER_FAIL_STAGE="$fail_stage"
  export ORDER_STATE="$TEST_DIR/state-${fail_stage:-success}"
  mkdir -p "$ORDER_STATE"
  if make --no-print-directory -s -k -j 8 -f "$TEST_DIR/Makefile" all VERIFY_CLI_ARTIFACTS=1 > "$TEST_DIR/make.log" 2>&1; then
    if [[ -n "$fail_stage" ]]; then
      echo "Generation failure was not propagated: $fail_stage" >&2
      exit 1
    fi
    test -f "$ORDER_STATE/generate-apiref"
    test -f "$ORDER_STATE/cli"
  elif [[ -z "$fail_stage" ]]; then
    cat "$TEST_DIR/make.log" >&2
    exit 1
  fi
  if [[ -n "$fail_stage" ]]; then
    test ! -f "$ORDER_STATE/artifacts-started"
    if [[ "$fail_stage" != update-helm ]]; then
      test ! -f "$ORDER_STATE/lint-started"
    fi
  fi
done

# 拆分后仍需保留 Helm/manifests，并且不再触发 CLI 编译。
export ORDER_FAIL_STAGE=''
export ORDER_STATE="$TEST_DIR/state-without-cli"
mkdir -p "$ORDER_STATE"
make --no-print-directory -s -j 8 -f "$TEST_DIR/Makefile" all VERIFY_CLI_ARTIFACTS=0 > "$TEST_DIR/make.log" 2>&1
for step in helm-package prepare-manifests artifacts generate-apiref; do
  test -f "$ORDER_STATE/$step"
done
test ! -f "$ORDER_STATE/cli-started"

# 独立 CLI job 等待全部 Go 写入者，并传播生成、模块、版本和编译失败。
for fail_stage in '' generate-code generate-mocks gomod gomod-verified verify-git-tag cli; do
  export ORDER_FAIL_STAGE="$fail_stage"
  export ORDER_STATE="$TEST_DIR/state-cli-${fail_stage:-success}"
  mkdir -p "$ORDER_STATE"
  if make --no-print-directory -s -k -j 8 -f "$TEST_DIR/Makefile" verify-cli-artifacts VERIFY_CLI_ARTIFACTS=0 > "$TEST_DIR/make.log" 2>&1; then
    if [[ -n "$fail_stage" ]]; then
      echo "CLI 验证未传播失败：$fail_stage" >&2
      exit 1
    fi
    test -f "$ORDER_STATE/cli"
  elif [[ -z "$fail_stage" ]]; then
    cat "$TEST_DIR/make.log" >&2
    exit 1
  fi
  if [[ -n "$fail_stage" && "$fail_stage" != cli ]]; then
    test ! -f "$ORDER_STATE/cli-started"
  fi
  test ! -f "$ORDER_STATE/helm-package"
  test ! -f "$ORDER_STATE/generate-apiref"
done

echo "Generation barriers and independent check overlap passed."
