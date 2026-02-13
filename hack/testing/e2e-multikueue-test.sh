#!/usr/bin/env bash

# Copyright 2023 The Kubernetes Authors.
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

SOURCE_DIR="$(cd "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT_DIR="$SOURCE_DIR/../.."

# E2E_TARGET_FOLDER allows running different test suites (multikueue, multikueue-dra)
E2E_TARGET_FOLDER=${E2E_TARGET_FOLDER:-multikueue}

export MANAGER_KIND_CLUSTER_NAME=${KIND_CLUSTER_NAME}-manager
export WORKER1_KIND_CLUSTER_NAME=${KIND_CLUSTER_NAME}-worker1
export WORKER2_KIND_CLUSTER_NAME=${KIND_CLUSTER_NAME}-worker2

export MANAGER_KUBECONFIG="${ARTIFACTS}/kubeconfig-$MANAGER_KIND_CLUSTER_NAME"
export WORKER1_KUBECONFIG="${ARTIFACTS}/kubeconfig-$WORKER1_KIND_CLUSTER_NAME"
export WORKER2_KUBECONFIG="${ARTIFACTS}/kubeconfig-$WORKER2_KIND_CLUSTER_NAME"

# shellcheck source=hack/testing/e2e-common.sh
source "${SOURCE_DIR}/e2e-common.sh"

function cleanup {
    if [ ! -d "$ARTIFACTS" ]; then
        mkdir -p "$ARTIFACTS"
    fi

    cluster_collect_artifacts "$MANAGER_KIND_CLUSTER_NAME" "$MANAGER_KUBECONFIG" &
    cluster_collect_artifacts "$WORKER1_KIND_CLUSTER_NAME" "$WORKER1_KUBECONFIG" &
    cluster_collect_artifacts "$WORKER2_KIND_CLUSTER_NAME" "$WORKER2_KUBECONFIG" &
    wait

    if e2e_should_delete_cluster; then
        cluster_cleanup "$MANAGER_KIND_CLUSTER_NAME" &
        cluster_cleanup "$WORKER1_KIND_CLUSTER_NAME" &
        cluster_cleanup "$WORKER2_KIND_CLUSTER_NAME" &
        wait
        return 0
    fi

    echo "Keeping kind clusters '$MANAGER_KIND_CLUSTER_NAME' '$WORKER1_KIND_CLUSTER_NAME' '$WORKER2_KIND_CLUSTER_NAME' (E2E_MODE=${E2E_MODE})."
    echo "To delete them:"
    echo "  kind delete clusters $MANAGER_KIND_CLUSTER_NAME $WORKER1_KIND_CLUSTER_NAME $WORKER2_KIND_CLUSTER_NAME"
}


function startup {
    if [ ! -d "$ARTIFACTS" ]; then
        mkdir -p "$ARTIFACTS"
    fi

    cp "${SOURCE_DIR}/multikueue/manager-cluster.kind.yaml" "$ARTIFACTS"

    IFS=. read -r -a varr <<< "$KIND_VERSION"
    minor=$(( varr[1] ))
    if [ "$minor" -eq 31 ]; then
        echo "Enable JobManagedBy feature in manager's kind config"
        $YQ e -i '.featureGates.JobManagedBy = true' "${ARTIFACTS}/manager-cluster.kind.yaml"
    fi

    ensure_kind_cluster "$MANAGER_KIND_CLUSTER_NAME" "${ARTIFACTS}/manager-cluster.kind.yaml" "$MANAGER_KUBECONFIG" &
    ensure_kind_cluster "$WORKER1_KIND_CLUSTER_NAME" "$SOURCE_DIR/multikueue/worker-cluster.kind.yaml" "$WORKER1_KUBECONFIG" &
    ensure_kind_cluster "$WORKER2_KIND_CLUSTER_NAME" "$SOURCE_DIR/multikueue/worker-cluster.kind.yaml" "$WORKER2_KUBECONFIG" &
}

function kueue_deploy {
    # We use a subshell to avoid overwriting the global cleanup trap, which also uses the EXIT signal.
    (
        set_managers_image
        trap restore_managers_image EXIT
        cluster_kueue_deploy "$MANAGER_KUBECONFIG"
        cluster_kueue_deploy "$WORKER1_KUBECONFIG"
        cluster_kueue_deploy "$WORKER2_KUBECONFIG"
    )
}

trap cleanup EXIT
startup 
prepare_docker_images
wait # for clusters creation
kind_load "$MANAGER_KIND_CLUSTER_NAME" "$MANAGER_KUBECONFIG" &
kind_load "$WORKER1_KIND_CLUSTER_NAME" "$WORKER1_KUBECONFIG" &
kind_load "$WORKER2_KIND_CLUSTER_NAME" "$WORKER2_KUBECONFIG" &
wait # for libraries installation
echo "Add contexts to default kubeconfig"
$KIND export kubeconfig --name "$MANAGER_KIND_CLUSTER_NAME"
$KIND export kubeconfig --name "$WORKER1_KIND_CLUSTER_NAME"
$KIND export kubeconfig --name "$WORKER2_KIND_CLUSTER_NAME"
kueue_deploy

if [ "$E2E_RUN_ONLY_ENV" = "true" ]; then
  read -rp "Do you want to cleanup? [Y/n] " reply
  if [[ "$reply" =~ ^[nN]$ ]]; then
    trap - EXIT
    echo "Skipping cleanup for kind clusters."
    echo -e "\nKind cluster cleanup:\n  kind delete clusters $MANAGER_KIND_CLUSTER_NAME $WORKER1_KIND_CLUSTER_NAME $WORKER2_KIND_CLUSTER_NAME"
  fi
  exit 0
fi

# shellcheck disable=SC2086
$GINKGO $GINKGO_ARGS --junit-report=junit.xml --json-report=e2e.json --output-dir="$ARTIFACTS" -v ./test/e2e/${E2E_TARGET_FOLDER}/...
"$ROOT_DIR/bin/ginkgo-top" -i "$ARTIFACTS/e2e.json" > "$ARTIFACTS/e2e-top.yaml"
