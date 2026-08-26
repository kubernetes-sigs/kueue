#!/usr/bin/env bash

# Copyright 2022 The Kubernetes Authors.
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

# shellcheck source=hack/testing/e2e-common.sh
source "${SOURCE_DIR}/e2e-common.sh"

function cleanup {
    local exit_code=$?

    # Deleting the cluster under an in-flight "kind create" SIGKILLs "kubeadm join",
    # which surfaces as "exit status 137" and hides the real failure (#14673).
    # Waiting is the only option: "timeout" escapes our process group and
    # "kubeadm" runs inside the node container.
    if [ -n "${STARTUP_PID:-}" ] && kill -0 "$STARTUP_PID" 2>/dev/null; then
        echo "Exiting (code ${exit_code}) while the cluster bring-up is still running; waiting for it to finish before teardown."
        wait "$STARTUP_PID" || true
    fi

    if [ ! -d "$ARTIFACTS" ]; then
        mkdir -p "$ARTIFACTS"
    fi

    cluster_collect_artifacts "$KIND_CLUSTER_NAME" ""

    if e2e_should_delete_cluster; then
        cluster_cleanup "$KIND_CLUSTER_NAME"
    else
        echo "Keeping kind cluster '$KIND_CLUSTER_NAME' (E2E_MODE=${E2E_MODE})."
        echo "To delete it:"
        echo "  kind delete clusters $KIND_CLUSTER_NAME"
    fi
}

function startup {
    if [ ! -d "$ARTIFACTS" ]; then
        mkdir -p "$ARTIFACTS"
    fi
    ensure_kind_cluster "$KIND_CLUSTER_NAME" "$SOURCE_DIR/$KIND_CLUSTER_FILE" ""
}

trap cleanup EXIT
build_kind_node_image
startup &
STARTUP_PID=$!
prepare_docker_images
# A bare "wait" reports success no matter how the background job exited, which
# would let the run continue without a cluster.
wait "$STARTUP_PID"
STARTUP_PID=""
kind_load "$KIND_CLUSTER_NAME" ""
kueue_deploy

if [[ -n ${PROMETHEUS_OPERATOR_VERSION:-} && ("$GINKGO_ARGS" =~ feature:prometheus || ! "$GINKGO_ARGS" =~ "--label-filter") ]]; then
    deploy_kueue_prometheus_config ""
fi

run_e2e_ginkgo --json-report=e2e.json --output-dir="$ARTIFACTS" -v ./test/e2e/"${E2E_TARGET_FOLDER}"/...
"$ROOT_DIR/bin/ginkgo-top" -i "$ARTIFACTS/e2e.json" > "$ARTIFACTS/e2e-top.yaml"
