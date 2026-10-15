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
ROOT_DIR=$(cd "${SCRIPT_DIR}/../.." && pwd)
workflow="${ROOT_DIR}/.github/workflows/unit-coverage.yaml"

count=$(grep -c -E "^[[:space:]]*auth_re='[^']*'$" "${workflow}" || true)
if [[ "${count}" -ne 1 ]]; then
  echo "expected exactly one auth_re assignment in ${workflow}, found ${count}" >&2
  exit 1
fi

line=$(grep -E "^[[:space:]]*auth_re='[^']*'$" "${workflow}")
auth_re="${line#*\'}"
auth_re="${auth_re%\'}"
if [[ -z "${auth_re}" ]]; then
  echo "extracted auth_re is empty" >&2
  exit 1
fi

expect_match() {
  local text="$1"
  if ! printf '%s\n' "${text}" | grep -Eiq "${auth_re}"; then
    echo "auth_re did not match: ${text}" >&2
    exit 1
  fi
}

expect_miss() {
  local text="$1"
  if printf '%s\n' "${text}" | grep -Eiq "${auth_re}"; then
    echo "auth_re matched unexpectedly: ${text}" >&2
    exit 1
  fi
}

expect_match "token not found"
expect_match "HTTP Error 401"
expect_match "status code 401"
expect_match "unauthorized"
expect_match "unauthorised"
expect_match "invalid token"
expect_match "tokenless upload"
expect_match "Could not find a repository, try using repo upload token"

expect_miss "HTTP Error 403"
expect_miss "status code 403"
expect_miss "could not find a repository"
expect_miss "Repository not found"
expect_miss "coverage upload failed: connection reset"
