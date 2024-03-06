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
KUEUE_ROOT=$(realpath $(dirname "${BASH_SOURCE[0]})"/..))
if [ -d "vendor" ]; then
  CODEGEN_PKG=$($GO_CMD list -m -f "vendor/k8s.io/code-generator" k8s.io/code-generator)
else
  CODEGEN_PKG=$($GO_CMD list -m -f "{{.Dir}}" k8s.io/code-generator)
fi

cd $(dirname "${BASH_SOURCE[0]}")/..
source "${CODEGEN_PKG}/kube_codegen.sh"

# TODO: remove the workaround when the issue is solved in the code-generator
# (https://github.com/kubernetes/code-generator/issues/165).
# Here, we create the soft link named "sigs.k8s.io" to the parent directory of
# Kueue to ensure the layout required by the kube_codegen.sh script.
ln -s .. sigs.k8s.io
trap "rm sigs.k8s.io" EXIT

# Generating conversion and defaults functions
kube::codegen::gen_helpers \
  --input-pkg-root sigs.k8s.io/kueue/apis \
  --output-base "${KUEUE_ROOT}" \
  --boilerplate ${KUEUE_ROOT}/hack/boilerplate.go.txt

# Generating OpenAPI for Kueue API extensions
kube::codegen::gen_openapi \
  --input-pkg-root sigs.k8s.io/kueue/apis/visibility \
  --output-pkg-root sigs.k8s.io/kueue/apis/visibility/v1alpha1 \
  --output-base "${KUEUE_ROOT}" \
  --update-report \
  --boilerplate "${KUEUE_ROOT}/hack/boilerplate.go.txt"

kube::codegen::gen_client \
  --input-pkg-root sigs.k8s.io/kueue/apis \
  --output-pkg-root sigs.k8s.io/kueue/client-go \
  --output-base "${KUEUE_ROOT}" \
  --boilerplate ${KUEUE_ROOT}/hack/boilerplate.go.txt \
  --with-watch \
  --with-applyconfig

# We need to clean up the go.mod file since code-generator adds temporary library to the go.mod file.
"${GO_CMD}" mod tidy
