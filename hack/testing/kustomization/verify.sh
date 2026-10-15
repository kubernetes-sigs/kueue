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

# Fails when a manifest that the Helm chart picks up from hack/processing-plan.yaml
# is not listed in its kustomization.yaml resources. Such a file silently ships
# with Helm installs but not with manifest installs.

set -o errexit
set -o nounset
set -o pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
YQ="${YQ:-${ROOT_DIR}/bin/yq}"
PLAN="${ROOT_DIR}/hack/processing-plan.yaml"

shopt -s nullglob

failed=0

# is_excluded <name> <comma-separated patterns>: matches like filepath.Match in
# the yaml-processor that renders the Helm chart.
is_excluded() {
  local name=$1 pattern
  local -a patterns
  IFS=',' read -r -a patterns <<<"$2"
  for pattern in "${patterns[@]}"; do
    # shellcheck disable=SC2053 # pattern is intentionally unquoted to glob-match
    [[ "${name}" == ${pattern} ]] && return 0
  done
  return 1
}

# check <path> <comma-separated excludes>: every file matching <path>, except
# the excluded ones, must be listed in the nearest kustomization.yaml resources.
check() {
  local path=${1#./} excludes=$2
  local kust_dir listed file rel
  local -a files
  # shellcheck disable=SC2206 # path is intentionally expanded as a glob
  files=("${ROOT_DIR}"/${path})
  if [[ ${#files[@]} -eq 0 ]]; then
    echo "ERROR: ${path} in hack/processing-plan.yaml matches no files" >&2
    failed=1
    return
  fi
  kust_dir=$(dirname "${ROOT_DIR}/${path}")
  while [[ ! -f "${kust_dir}/kustomization.yaml" ]]; do
    if [[ "${kust_dir}" == "${ROOT_DIR}" ]]; then
      echo "ERROR: no kustomization.yaml found for ${path}" >&2
      failed=1
      return
    fi
    kust_dir=$(dirname "${kust_dir}")
  done
  listed=$("${YQ}" '.resources[] | sub("^\./"; "")' "${kust_dir}/kustomization.yaml")
  for file in "${files[@]}"; do
    is_excluded "${file##*/}" "${excludes}" && continue
    rel=${file#"${kust_dir}/"}
    if ! grep -qxF -- "${rel}" <<<"${listed}"; then
      echo "ERROR: ${file#"${ROOT_DIR}/"} is not listed in ${kust_dir#"${ROOT_DIR}/"}/kustomization.yaml resources" >&2
      failed=1
    fi
  done
}

entries=$("${YQ}" '.files[] | .path + " " + ((.excludes // []) | join(","))' "${PLAN}")
if [[ -z "${entries}" ]]; then
  echo "ERROR: no file paths found in ${PLAN}" >&2
  exit 1
fi

while read -r path excludes; do
  check "${path}" "${excludes}"
done <<<"${entries}"

exit "${failed}"
