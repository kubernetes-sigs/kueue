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

set -o nounset
set -o pipefail

is_truthy() {
  case "${1:-}" in
    1 | true | TRUE | yes | YES | on | ON)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

command_preview() {
  local command=$1

  command=${command//$'\n'/; }
  command=${command//$'\t'/ }
  if [[ "${#command}" -gt 240 ]]; then
    printf '%s...' "${command:0:237}"
  else
    printf '%s' "${command}"
  fi
}

if ! is_truthy "${MAKE_TIMING:-}"; then
  exec /usr/bin/env bash -o pipefail "$@"
fi

command=""
if [[ "$#" -gt 0 ]]; then
  command="${!#}"
fi
preview=$(command_preview "${command}")
min_seconds="${MAKE_TIMING_MIN_SECONDS:-1}"
if ! [[ "${min_seconds}" =~ ^[0-9]+$ ]]; then
  min_seconds=1
fi

start_epoch=$(date +%s)
start_time=$(date -u '+%Y-%m-%dT%H:%M:%SZ')

/usr/bin/env bash -o pipefail "$@"
status=$?

end_epoch=$(date +%s)
end_time=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
duration=$((end_epoch - start_epoch))
if [[ "${status}" -ne 0 || "${duration}" -ge "${min_seconds}" ]]; then
  printf 'make-timing: started=%s finished=%s status=%d duration=%ss command=%s\n' \
    "${start_time}" "${end_time}" "${status}" "${duration}" "${preview}" >&2
fi

exit "${status}"
