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

# Fails when a resource granted to the manager in config/components/rbac/role.yaml
# is not granted by any editor or viewer ClusterRole in the same directory.
# Those are the roles batch-admin and batch-user aggregate, so without them
# users silently get no access to the resource. Only the rules are compared;
# verbs and aggregation labels are not checked.

set -o errexit
set -o nounset
set -o pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
YQ="${YQ:-${ROOT_DIR}/bin/yq}"
RBAC_DIR="${ROOT_DIR}/config/components/rbac"

# API groups whose resources need no editor/viewer ClusterRole.
excluded_groups=(
  # Used by the controller itself, not submitted by users.
  admissionregistration.k8s.io apiextensions.k8s.io events.k8s.io flowcontrol.apiserver.k8s.io
  # Only read or created by Kueue.
  autoscaling.x-k8s.io node.k8s.io resource.k8s.io scheduling.k8s.io
)

# Individual resources that need no editor/viewer ClusterRole.
excluded_resources=(
  # Only read or created by Kueue.
  /limitranges /namespaces /nodes /podtemplates apps/replicasets
  # The pod, deployment and statefulset integrations have never shipped
  # editor/viewer roles (unlike batch/jobs). TODO(<issue>): decide.
  /pods apps/deployments apps/statefulsets
  # Referenced by TrainJobs, not submitted to Kueue.
  trainer.kubeflow.org/trainingruntimes trainer.kubeflow.org/clustertrainingruntimes
  # Alpha APIs without editor/viewer roles. TODO(<issue>): decide.
  kueue.x-k8s.io/capacityproviders kueue.x-k8s.io/dynamicquotaorchestrators
)

# grants <file>...: prints every top-level "group/resource" the rules grant.
grants() {
  # shellcheck disable=SC2016 # $g is a yq variable, not a shell one
  "${YQ}" '.rules[] | .apiGroups[] as $g | .resources[] | select(test("/") | not) | $g + "/" + .' "$@" | sort -u
}

# is_excluded <group/resource>
is_excluded() {
  local entry
  for entry in "${excluded_groups[@]}"; do
    [[ "${1%/*}" == "${entry}" ]] && return 0
  done
  for entry in "${excluded_resources[@]}"; do
    [[ "$1" == "${entry}" ]] && return 0
  done
  return 1
}

# report <group/resource> <editor|viewer>
report() {
  echo "ERROR: role.yaml grants $1 to the manager, but no *_$2_role.yaml grants it." \
    "Add config/components/rbac/<name>_$2_role.yaml, listed in kustomization.yaml and with the same aggregation labels as the roles of similar resources," \
    "or exclude it in hack/testing/rbac/verify.sh with the reason." >&2
}

manager=$(grants "${RBAC_DIR}/role.yaml")
editors=$(grants "${RBAC_DIR}"/*_editor_role.yaml)
viewers=$(grants "${RBAC_DIR}"/*_viewer_role.yaml)

failed=0
checked=0
while read -r resource; do
  [[ -z "${resource}" ]] && continue
  is_excluded "${resource}" && continue
  checked=$((checked + 1))
  if ! grep -qxF -- "${resource}" <<<"${editors}"; then
    report "${resource}" editor
    failed=1
  fi
  if ! grep -qxF -- "${resource}" <<<"${viewers}"; then
    report "${resource}" viewer
    failed=1
  fi
done <<<"${manager}"

if [[ ${checked} -eq 0 ]]; then
  echo "ERROR: no resources to check in ${RBAC_DIR}/role.yaml" >&2
  exit 1
fi

exit "${failed}"
