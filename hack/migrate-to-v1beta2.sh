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

set -o errexit
set -o nounset
set -o pipefail

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

for kind in "${kinds[@]}"; do
  echo "Migrating $kind.kueue.x-k8s.io ..."

  # Get resources in namespace/name format
  resources=$(kubectl get "$kind.kueue.x-k8s.io" -A -o jsonpath='{range .items[*]}{.metadata.namespace}{"/"}{.metadata.name}{"\n"}{end}')

  if [[ -z "$resources" ]]; then
    echo "  → No $kind found, skipping"
    continue
  fi

  echo "$resources" | while read -r entry; do
    [[ -z "$entry" || "$entry" == "/" ]] && continue

    ns="${entry%/*}"
    name="${entry#*/}"

    if [[ -z "$ns" ]]; then
      # Cluster-scoped resource
      kubectl patch "$kind.kueue.x-k8s.io" "$name" --type=merge -p '{"apiVersion":"kueue.x-k8s.io/v1beta2"}'
    else
      # Namespaced resource
      kubectl patch "$kind.kueue.x-k8s.io" "$name" -n "$ns" --type=merge -p '{"apiVersion":"kueue.x-k8s.io/v1beta2"}'
    fi
  done

  echo "  → $kind migration complete"
done

echo "All migrations finished successfully!"
