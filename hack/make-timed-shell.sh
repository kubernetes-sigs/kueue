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

write_command_timing_env() {
  local timing_env=$1

  cat >"${timing_env}" <<'EOF'
__make_timing_original_bash_env="${MAKE_TIMING_ORIGINAL_BASH_ENV:-}"
unset BASH_ENV
unset MAKE_TIMING_ORIGINAL_BASH_ENV

__make_timing_command_preview() {
  local command=$1

  command=${command//$'\n'/; }
  command=${command//$'\t'/ }
  if [[ "${#command}" -gt 240 ]]; then
    printf '%s...' "${command:0:237}"
  else
    printf '%s' "${command}"
  fi
}

__make_timing_min_seconds="${MAKE_TIMING_MIN_SECONDS:-1}"
if ! [[ "${__make_timing_min_seconds}" =~ ^[0-9]+$ ]]; then
  __make_timing_min_seconds=1
fi

__make_timing_last_command=""
__make_timing_last_start_epoch=""
__make_timing_last_start_time=""
__make_timing_in_trap=0

__make_timing_now() {
  local epoch_var=$1
  local time_var=$2
  local epoch
  local timestamp

  if printf -v epoch '%(%s)T' -1 2>/dev/null &&
    TZ=UTC printf -v timestamp '%(%Y-%m-%dT%H:%M:%SZ)T' -1 2>/dev/null; then
    printf -v "${epoch_var}" '%s' "${epoch}"
    printf -v "${time_var}" '%s' "${timestamp}"
    return 0
  fi

  epoch=$(date +%s)
  timestamp=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
  printf -v "${epoch_var}" '%s' "${epoch}"
  printf -v "${time_var}" '%s' "${timestamp}"
}

__make_timing_log_previous_command() {
  local status=$1
  local end_epoch
  local end_time
  local duration
  local preview

  if [[ -z "${__make_timing_last_command}" ]]; then
    return 0
  fi
  case "${__make_timing_last_command}" in
    trap\ __make_timing_* | trap\ -\ DEBUG | __make_timing_*)
      return 0
      ;;
  esac

  __make_timing_now end_epoch end_time
  duration=$((end_epoch - __make_timing_last_start_epoch))
  if [[ "${status}" -ne 0 || "${duration}" -ge "${__make_timing_min_seconds}" ]]; then
    preview=$(__make_timing_command_preview "${__make_timing_last_command}")
    printf 'make-command-timing: started=%s finished=%s status=%d duration=%ss command=%s\n' \
      "${__make_timing_last_start_time}" "${end_time}" "${status}" "${duration}" "${preview}" >&2
  fi
}

__make_timing_debug_trap() {
  local status=$?
  local next_command=${BASH_COMMAND}

  if [[ "${__make_timing_in_trap}" -eq 1 ]]; then
    return 0
  fi

  __make_timing_in_trap=1
  __make_timing_log_previous_command "${status}"
  __make_timing_last_command="${next_command}"
  __make_timing_now __make_timing_last_start_epoch __make_timing_last_start_time
  __make_timing_in_trap=0
  # Keep the trap non-interfering. Returning the previous command status can
  # trip errexit before constructs like `cmd || fallback` handle the failure.
  return 0
}

__make_timing_exit_trap() {
  local status=$?

  __make_timing_in_trap=1
  trap - DEBUG
  __make_timing_log_previous_command "${status}"
  return "${status}"
}

__make_timing_source_original_bash_env() {
  local expanded_bash_env=${__make_timing_original_bash_env}

  if [[ -z "${expanded_bash_env}" ]]; then
    return 0
  fi

  case "${expanded_bash_env}" in
    ~)
      expanded_bash_env=${HOME}
      ;;
    ~/*)
      expanded_bash_env="${HOME}/${expanded_bash_env#\~/}"
      ;;
  esac
  eval "expanded_bash_env=\"${expanded_bash_env}\""
  # shellcheck disable=SC1090
  source "${expanded_bash_env}"
}

__make_timing_install_traps() {
  if [[ -n "$(trap -p EXIT)" || -n "$(trap -p DEBUG)" ]]; then
    return 0
  fi

  trap __make_timing_exit_trap EXIT
  trap __make_timing_debug_trap DEBUG
}

__make_timing_source_original_bash_env
__make_timing_install_traps
EOF
}

cleanup_command_timing_env() {
  if [[ -n "${command_timing_env}" ]]; then
    rm -f "${command_timing_env}"
    command_timing_env=""
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

command_timing_env=""
if is_truthy "${MAKE_TIMING_COMMANDS:-0}"; then
  original_bash_env="${BASH_ENV:-}"
  command_timing_env=$(mktemp "${TMPDIR:-/tmp}/make-timing.XXXXXX")
  trap cleanup_command_timing_env EXIT
  write_command_timing_env "${command_timing_env}"
  MAKE_TIMING_ORIGINAL_BASH_ENV="${original_bash_env}" BASH_ENV="${command_timing_env}" \
    /usr/bin/env bash -o pipefail "$@"
  status=$?
else
  /usr/bin/env bash -o pipefail "$@"
  status=$?
fi

cleanup_command_timing_env
trap - EXIT

end_epoch=$(date +%s)
end_time=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
duration=$((end_epoch - start_epoch))
if [[ "${status}" -ne 0 || "${duration}" -ge "${min_seconds}" ]]; then
  printf 'make-timing: started=%s finished=%s status=%d duration=%ss command=%s\n' \
    "${start_time}" "${end_time}" "${status}" "${duration}" "${preview}" >&2
fi

exit "${status}"
