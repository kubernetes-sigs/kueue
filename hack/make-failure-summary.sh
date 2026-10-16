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

# Inline, the echo below would show in make-timed-shell.sh previews of passing runs.
#
# Usage: make-failure-summary.sh <make> [args...]

set -o errexit
set -o nounset
set -o pipefail

log=$(mktemp)
trap 'rm -f "${log}"' EXIT

status=0
"$@" 2>&1 | tee "${log}" || status=$?
if [[ "${status}" -ne 0 ]]; then
  echo "ERROR: failed make targets:" >&2
  grep -E 'make\[[0-9]+\]: \*\*\* \[' "${log}" >&2 || true
fi
exit "${status}"
