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

# E2E_TARGET_FOLDER allows running different test suites (multikueue/baseline, multikueue/dra, multikueue/sequential)
E2E_TARGET_FOLDER=${E2E_TARGET_FOLDER:-multikueue/baseline}

export MANAGER_KIND_CLUSTER_NAME=${KIND_CLUSTER_NAME}-manager
export WORKER1_KIND_CLUSTER_NAME=${KIND_CLUSTER_NAME}-worker1
export WORKER2_KIND_CLUSTER_NAME=${KIND_CLUSTER_NAME}-worker2

export MANAGER_KUBECONFIG="${ARTIFACTS}/kubeconfig-$MANAGER_KIND_CLUSTER_NAME"
export WORKER1_KUBECONFIG="${ARTIFACTS}/kubeconfig-$WORKER1_KIND_CLUSTER_NAME"
export WORKER2_KUBECONFIG="${ARTIFACTS}/kubeconfig-$WORKER2_KIND_CLUSTER_NAME"

# shellcheck source=hack/testing/e2e-common.sh
source "${SOURCE_DIR}/e2e-common.sh"

function cleanup {
    local exit_code=$?
    e2e_timed_log_unfinished "${exit_code}"
    # Finish background work before deleting clusters or collecting their logs.
    wait || true

    if [ ! -d "$ARTIFACTS" ]; then
        mkdir -p "$ARTIFACTS"
    fi

    cluster_collect_artifacts "$MANAGER_KIND_CLUSTER_NAME" "$MANAGER_KUBECONFIG" &
    cluster_collect_artifacts "$WORKER1_KIND_CLUSTER_NAME" "$WORKER1_KUBECONFIG" &
    cluster_collect_artifacts "$WORKER2_KIND_CLUSTER_NAME" "$WORKER2_KUBECONFIG" &
    wait

    if e2e_should_delete_cluster; then
        # Run cleanup sequentially to prevent kubeconfig lock collisions
        cluster_cleanup "$MANAGER_KIND_CLUSTER_NAME" 
        cluster_cleanup "$WORKER1_KIND_CLUSTER_NAME" 
        cluster_cleanup "$WORKER2_KIND_CLUSTER_NAME"
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
    STARTUP_PIDS=("$!")
    ensure_kind_cluster "$WORKER1_KIND_CLUSTER_NAME" "$SOURCE_DIR/multikueue/worker-cluster.kind.yaml" "$WORKER1_KUBECONFIG" &
    STARTUP_PIDS+=("$!")
    ensure_kind_cluster "$WORKER2_KIND_CLUSTER_NAME" "$SOURCE_DIR/multikueue/worker-cluster.kind.yaml" "$WORKER2_KUBECONFIG" &
    STARTUP_PIDS+=("$!")
}

function kueue_deploy {
    # We use a subshell to avoid overwriting the global cleanup trap, which also uses the EXIT signal.
    (
        set_managers_image
        trap restore_managers_image EXIT
        # cert-manager installation temporarily patches shared kustomizations.
        if [[ -n ${CERTMANAGER_VERSION:-} && ${E2E_USE_HELM:-false} != true ]]; then
            cluster_kueue_deploy "$MANAGER_KUBECONFIG"
            cluster_kueue_deploy "$WORKER1_KUBECONFIG"
            cluster_kueue_deploy "$WORKER2_KUBECONFIG"
            exit 0
        fi
        # Each deployment uses its own kubeconfig. Keep the shared image override
        # until every child has finished, including when one deployment fails.
        cluster_kueue_deploy "$MANAGER_KUBECONFIG" &
        deploy_pids=("$!")
        cluster_kueue_deploy "$WORKER1_KUBECONFIG" &
        deploy_pids+=("$!")
        cluster_kueue_deploy "$WORKER2_KUBECONFIG" &
        deploy_pids+=("$!")
        failed=0
        for job in "${deploy_pids[@]}"; do
            wait "$job" || failed=1
        done
        exit "$failed"
    )
}

trap cleanup EXIT
# Warm the compiler cache while the clusters start. Match Ginkgo run's default
# linker flags, but let the final source-based run honor all GINKGO_ARGS overrides.
(
    if [[ "$E2E_TARGET_FOLDER" == "multikueue/baseline" ]]; then
        export GOMAXPROCS="${GOMAXPROCS:-1}"
        export GOFLAGS="${GOFLAGS:--p=4}"
    fi
    e2e_timed compile_e2e "$GINKGO" build -ldflags="-w -s" -r ./test/e2e/"${E2E_TARGET_FOLDER}"
) &
COMPILE_PID=$!
e2e_timed build_kind_node_image build_kind_node_image
startup
e2e_timed prepare_docker_images prepare_docker_images
# Do not wait for compilation here: it can also overlap image loading and deployment.
for job in "${STARTUP_PIDS[@]}"; do
    e2e_timed wait_for_cluster wait "$job"
done # wait for clusters creation

kind_load "$MANAGER_KIND_CLUSTER_NAME" "$MANAGER_KUBECONFIG" &
LOAD_PIDS=("$!")
kind_load "$WORKER1_KIND_CLUSTER_NAME" "$WORKER1_KUBECONFIG" &
LOAD_PIDS+=("$!")
kind_load "$WORKER2_KIND_CLUSTER_NAME" "$WORKER2_KUBECONFIG" &
LOAD_PIDS+=("$!")
for job in "${LOAD_PIDS[@]}"; do
    e2e_timed wait_for_dependencies wait "$job"
done # wait for dependency installation
echo "Add contexts to default kubeconfig"
$KIND export kubeconfig --name "$MANAGER_KIND_CLUSTER_NAME"
$KIND export kubeconfig --name "$WORKER1_KIND_CLUSTER_NAME"
$KIND export kubeconfig --name "$WORKER2_KIND_CLUSTER_NAME"
e2e_timed kueue_deploy kueue_deploy

e2e_timed wait_for_compile wait "$COMPILE_PID"
# Source discovery excludes stale binaries and preserves custom build flags such
# as --race. With default flags, compilation reuses the cache warmed above.
e2e_timed ginkgo run_e2e_ginkgo --json-report=e2e.json --output-dir="$ARTIFACTS" -v \
    ./test/e2e/"${E2E_TARGET_FOLDER}"/...
"${E2E_BIN_DIR}/ginkgo-top" -i "$ARTIFACTS/e2e.json" > "$ARTIFACTS/e2e-top.yaml"
