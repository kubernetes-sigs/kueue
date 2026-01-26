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

for kind in "${kinds[@]}"; do
  echo "Migrating $kind.kueue.x-k8s.io..."

  resources_cmd="kubectl get \"${kind}.kueue.x-k8s.io\" -A -o jsonpath='{range .items[*]}{.metadata.namespace}{\"/\"}{.metadata.name}{\"\n\"}{end}'"
  if $VERBOSE; then
    echo "  → ${resources_cmd}"
  fi

  # Get resources in namespace/name format
  resources=$(eval "$resources_cmd")
  # shellcheck disable=SC2181
  if [ $? -ne 0 ]; then
    echo "" >&2
    exit_code=1
    continue
  fi

  if [[ -z "${resources}" ]]; then
    echo "  → No $kind found, skipping."
    echo ""
    continue
  fi

  filtered=$(echo "${resources}" | grep -v '^$' | grep -v '^/$')
  total=$(echo "${filtered}" | wc -l | tr -d '[:space:]')

  if [[ -z "${total}" ]] || (( total == 0 )); then
    echo "  → No valid ${kind} found, skipping."
    echo ""
    continue
  fi

  echo "  → Found: ${total} object(s)"

  patched=0
  percent=0
  last_percent=-1

  while read -r entry; do
    ns="${entry%/*}"
    name="${entry#*/}"

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
      continue
    fi

    if $VERBOSE; then
      echo "       $output" >&2
    fi

    percent=$(( (patched * 100 + total / 2) / total ))
    message=$(printf "  → Progress: %3d%% (%d/%d)" "$percent" "$patched" "$total")

    if [[ -t 1 ]]; then
      printf "%s\r" "${message}"
    else
      if [[ "$percent" -ne "$last_percent" ]]; then
        printf "%s\n" "${message}"
        last_percent=$percent
      fi
    fi
  done <<< "$filtered"

  if [[ -t 1 ]]; then
    printf "  → Progress: %3d%% (%d/%d)\n" "${percent}" "${patched}" "${total}"
  fi

  if [ "$patched" -ne "$total" ]; then
    failures=$(( total - patched ))
    echo "  → Error: only ${patched}/${total} object(s) patched successfully (${failures} failure(s))!"
  fi

  echo ""
done

if [ $exit_code -eq 0 ]; then
  echo "All migrations finished successfully!"
else
  echo "Error: Migration completed with issues!" >&2
  exit $exit_code
fi
