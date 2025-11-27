#!/usr/bin/env bash

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

set -o errexit
set -o nounset
set -o pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MOD_FILE="${REPO_ROOT}/hack/tools/go.mod"
K8S_TOOL_PKG="k8s.io/kubernetes/test/compatibility_lifecycle"
REFERENCE_DIR="${REPO_ROOT}/test/compatibility_lifecycle/reference"
REFERENCE_FILE="${REFERENCE_DIR}/versioned_feature_list.yaml"
SITE_DATA_DIR="${REPO_ROOT}/site/data/featuregates"
SITE_DATA_FILE="${SITE_DATA_DIR}/versioned_feature_list.yaml"

mkdir -p "${REFERENCE_DIR}"
mkdir -p "${SITE_DATA_DIR}"

go run -mod=mod -modfile="${MOD_FILE}" "${K8S_TOOL_PKG}" feature-gates update

cp "${REFERENCE_FILE}" "${SITE_DATA_FILE}"

