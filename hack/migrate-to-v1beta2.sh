#!/usr/bin/env bash

# Copyright 2025 The Kubernetes Authors.
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

# This script migrates Kueue resources to the v1beta2 storage version.
# It handles both namespaced and cluster-scoped resources.

set -o nounset
set -o pipefail

DRY_RUN=""
VERBOSE=false
NAMESPACE_REGEX=""
KUBECTL_GET_API_VERSION="v1beta1"

show_help() {
  cat << EOF
Usage: $(basename "$0") [OPTIONS]

Migrates Kueue resources to v1beta2.

Options:
  --dry-run[=STRATEGY]                  Perform a dry run. STRATEGY can be 'client' (default) for client-side dry run
                                        or 'server' for server-side dry run.
  -v, --verbose                         Enable verbose output
  --namespace-regex=REGEX               Filter namespaces using this extended regular expression
                                        Examples:
                                          --namespace-regex="prod|staging"
                                          --namespace-regex="^team-"
                                          --namespace-regex="^(?!kube-system|default).*$"
  --kubectl-get-api-version=VERSION     Specify the API version ('v1beta1' or 'v1beta2').
                                        Default: v1beta1

                                        The selected version is used to list all resources, like Workloads, from the API server before updating them.
                                        It is preferred to use 'v1beta1' when most Workloads are still in 'v1beta1' (e.g. just after upgrade to Kueue 0.16+),
                                        to avoid too many calls to conversion webhooks when the API server lists workloads from etcd.
                                        On large deployments listing all workloads and going through conversion may cause timeouts for API
                                        server when communicating with etcd.
  -h, --help                            Show this help message and exit
EOF
}

# Parse flags
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run)
      DRY_RUN="--dry-run=client"
      shift
      ;;
    --dry-run=client|--dry-run=server)
      DRY_RUN="$1"
      shift
      ;;
    -v|--verbose)
      VERBOSE=true
      shift
      ;;
    --namespace-regex=*)
      NAMESPACE_REGEX="${1#--namespace-regex=}"
      shift
      ;;
    --kubectl-get-api-version=v1beta1|--kubectl-get-api-version=v1beta2)
      KUBECTL_GET_API_VERSION="${1#--kubectl-get-api-version=}"
      shift
      ;;
    --kubectl-get-api-version=*)
      echo "Error: Invalid --kubectl-get-api-version value: '${1#--kubectl-get-api-version=}'" >&2
      echo "       Allowed values: v1beta1 or v1beta2" >&2
      exit 1
      ;;
    -h|--help)
      show_help
      exit 0
      ;;
    *)
      # ignore unknown flags or stop here
      break
      ;;
  esac
done

if [[ -n "$DRY_RUN" ]]; then
    echo "Running in dry-run mode ($DRY_RUN) – no changes will be applied."
    echo ""
fi

namespaces=$(kubectl get ns -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | grep -E "$NAMESPACE_REGEX")
printf "Found namespaces: %s.\n\n" "$(echo "$namespaces" | paste -sd ',' -)"

kinds=(
  cohorts
  workloadpriorityclasses
  localqueues
  clusterqueues
  resourceflavors
  admissionchecks
  provisioningrequestconfigs
  multikueueclusters
  multikueueconfigs
  topologies
  workloads
)

patch_payload='{"apiVersion":"kueue.x-k8s.io/v1beta2"}'

exit_code=0

function apply_patches() {
  local kind=$1
  local ns=$2

  printf "  → Fetching resources%s.\n" "${ns:+ in namespace $ns}"

  local resources_cmd="kubectl get \"${kind}.${KUBECTL_GET_API_VERSION}.kueue.x-k8s.io\" -o jsonpath='{range .items[*]}{.metadata.name}{\"\n\"}{end}' ${ns:+-n \"$ns\"}"

  if $VERBOSE; then
    echo "  → ${resources_cmd}"
  fi

  # Get resources in namespace/name format
  # Skip "Warning: This version is deprecated. Use v1beta2 instead" from stderr.
  local resources
  resources=$(eval "${resources_cmd}" 2> >(grep -v -i -F "Warning: This version is deprecated. Use v1beta2 instead." >&2) | awk NF)

  # shellcheck disable=SC2181
  if [ $? -ne 0 ]; then
    echo "" >&2
    exit_code=1
    return
  fi

  if [[ -z "${resources}" ]]; then
    printf "  → No %s found%s, skipping.\n" "$kind" "${ns:+ in namespace $ns}"
    return
  fi

  filtered=$(echo "${resources}" | grep -v '^$' | grep -v '^/$')
  total=$(echo "${filtered}" | wc -l | tr -d '[:space:]')

  if [[ -z "${total}" ]] || (( total == 0 )); then
    echo "  → No valid ${kind} found, skipping."
    return
  fi

  printf "  → Found: %s object(s)%s.\n" "$total" "${ns:+ in namespace $ns}"

  patched=0
  percent=0
  last_percent=-1

  while read -r name; do
    patch_cmd=(kubectl patch "$kind.kueue.x-k8s.io" "$name")
    if [[ -n "$ns" ]]; then
      # Namespaced resource
      patch_cmd+=(-n "$ns")
    fi

    # shellcheck disable=SC2206
    patch_cmd+=(--type=merge -p "$patch_payload" $DRY_RUN)

    if $VERBOSE; then
        echo "    └─ ${patch_cmd[*]}"
    fi

    output=$("${patch_cmd[@]}")
    # shellcheck disable=SC2181
    if [ $? -eq 0 ]; then
      patched=$((patched + 1))
    else
      exit_code=1
      return
    fi

    if $VERBOSE; then
      echo "       $output" >&1
    fi

    percent=$(( (patched * 100 + total / 2) / total ))
    message=$(printf "  → Progress: %3d%% (%d/%d)" "$percent" "$patched" "$total")

    if [[ "${VERBOSE}" != "true" && -t 1 ]]; then
      printf "%s\r" "${message}"
    else
      if [[ "$percent" -ne "$last_percent" ]]; then
        printf "%s\n" "${message}"
        last_percent=$percent
      fi
    fi
  done <<< "$filtered"

  if [[ "${VERBOSE}" != "true" && -t 1 ]]; then
    printf "  → Progress: %3d%% (%d/%d)\n" "${percent}" "${patched}" "${total}"
  fi

  if [ "$patched" -ne "$total" ]; then
    failures=$(( total - patched ))
    echo "  → Error: only ${patched}/${total} object(s) patched successfully (${failures} failure(s))!"
  fi
}

for kind in "${kinds[@]}"; do
  echo "Migrating $kind.kueue.x-k8s.io..."

  namespaced=$(kubectl get --raw "/apis/kueue.x-k8s.io/v1beta1" 2>/dev/null | \
    jq -r --arg kind_lowercase "${kind,,}" \
      '.resources[] | select(.name==$kind_lowercase) | .namespaced')

  if $namespaced; then
    while read -r ns; do
      [[ -z "$ns" ]] && continue
      apply_patches "$kind" "$ns"
    done <<< "$namespaces"
  else
    apply_patches "$kind" ""
  fi

  echo ""
done

if [ $exit_code -eq 0 ]; then
  echo "All migrations finished successfully!"
else
  echo "Error: Migration completed with issues!" >&2
  exit $exit_code
fi
