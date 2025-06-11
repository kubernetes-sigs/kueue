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
ROOT_DIR="$SOURCE_DIR/.."
export MANAGER_KIND_CLUSTER_NAME=${KIND_CLUSTER_NAME}-manager
export WORKER1_KIND_CLUSTER_NAME=${KIND_CLUSTER_NAME}-worker1
export WORKER2_KIND_CLUSTER_NAME=${KIND_CLUSTER_NAME}-worker2

export MANAGER_KUBECONFIG="${ARTIFACTS}/kubeconfig-$MANAGER_KIND_CLUSTER_NAME"
export WORKER1_KUBECONFIG="${ARTIFACTS}/kubeconfig-$WORKER1_KIND_CLUSTER_NAME"
export WORKER2_KUBECONFIG="${ARTIFACTS}/kubeconfig-$WORKER2_KIND_CLUSTER_NAME"

# shellcheck source=hack/e2e-common.sh
source "${SOURCE_DIR}/e2e-common.sh"

function cleanup {
    if [ "$CREATE_KIND_CLUSTER" == 'true' ]
    then
        if [ ! -d "$ARTIFACTS" ]; then
            mkdir -p "$ARTIFACTS"
        fi

        cluster_cleanup "$MANAGER_KIND_CLUSTER_NAME" "$MANAGER_KUBECONFIG" &
        cluster_cleanup "$WORKER1_KIND_CLUSTER_NAME" "$WORKER1_KUBECONFIG" &
        cluster_cleanup "$WORKER2_KIND_CLUSTER_NAME" "$WORKER2_KUBECONFIG" &
    fi
    #do the image restore here for the case when an error happened during deploy
    restore_managers_image
    wait
}


function startup {
    if [ "$CREATE_KIND_CLUSTER" == 'true' ]
    then
        if [ ! -d "$ARTIFACTS" ]; then
            mkdir -p "$ARTIFACTS"
        fi

        cp "${SOURCE_DIR}/multikueue/manager-cluster.kind.yaml" "$ARTIFACTS"

        #Enable the JobManagedBy feature gates for k8s 1.30+ versions 
        IFS=. read -r -a varr <<< "$KIND_VERSION"
        minor=$(( varr[1] ))        
        if [ "$minor" -ge 30 ]; then
            echo "Enable JobManagedBy feature in manager's kind config"
            $YQ e -i '.featureGates.JobManagedBy = true' "${ARTIFACTS}/manager-cluster.kind.yaml"
        fi

        cluster_create "$MANAGER_KIND_CLUSTER_NAME" "${ARTIFACTS}/manager-cluster.kind.yaml" "$MANAGER_KUBECONFIG" &
        cluster_create "$WORKER1_KIND_CLUSTER_NAME" "$SOURCE_DIR/multikueue/worker-cluster.kind.yaml" "$WORKER1_KUBECONFIG" &
        cluster_create "$WORKER2_KIND_CLUSTER_NAME" "$SOURCE_DIR/multikueue/worker-cluster.kind.yaml" "$WORKER2_KUBECONFIG" &
    fi
}

function kueue_deploy {
    (cd "${ROOT_DIR}/config/components/manager" && $KUSTOMIZE edit set image controller="$IMAGE_TAG")

    cluster_kueue_deploy "$MANAGER_KUBECONFIG"
    cluster_kueue_deploy "$WORKER1_KUBECONFIG"
    cluster_kueue_deploy "$WORKER2_KUBECONFIG"
}

trap cleanup EXIT
startup 
prepare_docker_images
wait # for clusters creation
kind_load "$MANAGER_KIND_CLUSTER_NAME" "$MANAGER_KUBECONFIG" &
kind_load "$WORKER1_KIND_CLUSTER_NAME" "$WORKER1_KUBECONFIG" &
kind_load "$WORKER2_KIND_CLUSTER_NAME" "$WORKER2_KUBECONFIG" &
wait # for libraries installation
# Merge kubeconfigs to one file to be used in tests now
export KUBECONFIG="$MANAGER_KUBECONFIG:$WORKER1_KUBECONFIG:$WORKER2_KUBECONFIG"
kueue_deploy

if [ "$E2E_RUN_ONLY_ENV" == 'true' ]; then
  read -rp "Press Enter to cleanup."
else
  # shellcheck disable=SC2086
  $GINKGO $GINKGO_ARGS --junit-report=junit.xml --json-report=e2e.json --output-dir="$ARTIFACTS" -v ./test/e2e/multikueue/...
  "$ROOT_DIR/bin/ginkgo-top" -i "$ARTIFACTS/e2e.json" > "$ARTIFACTS/e2e-top.yaml"
fi
