#!/usr/bin/env bash

# Copyright 2023 The Kubernetes Authors.
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

GO_CMD=${1:-go}
CURRENT_DIR=$(dirname "${BASH_SOURCE[0]}")
KUEUE_ROOT=$(realpath "${CURRENT_DIR}/..")
KUEUE_PKG="sigs.k8s.io/kueue"
CODEGEN_PKG=$(cd "${TOOLS_DIR}"; $GO_CMD list -m -mod=readonly -f "{{.Dir}}" k8s.io/code-generator)

cd "$CURRENT_DIR/.."

# shellcheck source=/dev/null
source "${CODEGEN_PKG}/kube_codegen.sh"

# Generating conversion and defaults functions
kube::codegen::gen_helpers \
  --boilerplate "${KUEUE_ROOT}/hack/boilerplate.go.txt" \
  "${KUEUE_ROOT}/apis"

# Generating OpenAPI for Kueue API extensions
kube::codegen::gen_openapi \
  --boilerplate "${KUEUE_ROOT}/hack/boilerplate.go.txt" \
  --output-dir "${KUEUE_ROOT}/apis/visibility/openapi" \
  --output-pkg "${KUEUE_PKG}/apis/visibility/openapi" \
  --update-report \
  "${KUEUE_ROOT}/apis/visibility"

kube::codegen::gen_client \
  --boilerplate "${KUEUE_ROOT}/hack/boilerplate.go.txt" \
  --output-dir "${KUEUE_ROOT}/client-go" \
  --output-pkg "${KUEUE_PKG}/client-go" \
  --with-watch \
  --with-applyconfig \
  "${KUEUE_ROOT}/apis"
