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

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN_DIR="${REPO_ROOT}/bin"
COMPAT_LIFECYCLE_BIN="${BIN_DIR}/compatibility-lifecycle"
COMPAT_LIFECYCLE_BIN_STAMP="${BIN_DIR}/compatibility-lifecycle.version"
K8S_REPO_URL="${K8S_REPO_URL:-https://github.com/kubernetes/kubernetes.git}"
# Keep this pinned unless you have a reason to bump it (tooling output can change).
K8S_REPO_REF="${K8S_REPO_REF:-v1.34.2}"
K8S_CACHE_DIR="${BIN_DIR}/.cache/compatibility_lifecycle/kubernetes-${K8S_REPO_REF}"
REFERENCE_DIR="${REPO_ROOT}/test/compatibility_lifecycle/reference"
REFERENCE_FILE="${REFERENCE_DIR}/versioned_feature_list.yaml"
SITE_DATA_DIR="${REPO_ROOT}/site/data/featuregates"
SITE_DATA_FILE="${SITE_DATA_DIR}/versioned_feature_list.yaml"

mkdir -p "${REFERENCE_DIR}"
mkdir -p "${SITE_DATA_DIR}"
mkdir -p "${BIN_DIR}"
mkdir -p "$(dirname "${K8S_CACHE_DIR}")"

need_build=true
if [[ -x "${COMPAT_LIFECYCLE_BIN}" && -f "${COMPAT_LIFECYCLE_BIN_STAMP}" ]]; then
  if [[ "$(cat "${COMPAT_LIFECYCLE_BIN_STAMP}")" == "${K8S_REPO_REF}" ]]; then
    need_build=false
  fi
fi

if [[ "${need_build}" == "true" ]]; then
  if [[ ! -d "${K8S_CACHE_DIR}/.git" ]]; then
    rm -rf "${K8S_CACHE_DIR}"
    git clone --depth=1 --branch "${K8S_REPO_REF}" "${K8S_REPO_URL}" "${K8S_CACHE_DIR}"
  fi

  # Build the command from the pinned Kubernetes checkout.
  pushd "${K8S_CACHE_DIR}" >/dev/null
  go build -o "${COMPAT_LIFECYCLE_BIN}" ./test/compatibility_lifecycle
  popd >/dev/null

  echo -n "${K8S_REPO_REF}" > "${COMPAT_LIFECYCLE_BIN_STAMP}"
fi

mkdir -p "${REPO_ROOT}/staging"
pushd "${REPO_ROOT}" >/dev/null
"${COMPAT_LIFECYCLE_BIN}" feature-gates update
popd >/dev/null

cp "${REFERENCE_FILE}" "${SITE_DATA_FILE}"

