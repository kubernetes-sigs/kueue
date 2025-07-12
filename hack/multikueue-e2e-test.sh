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

# shellcheck source=hack/e2e-common.sh
source "${SOURCE_DIR}/e2e-common.sh"

function cleanup {
    if [ "$CREATE_KIND_CLUSTER" == 'true' ]
    then
        if [ ! -d "$ARTIFACTS" ]; then
            mkdir -p "$ARTIFACTS"
        fi

        cluster_cleanup "$MANAGER_KIND_CLUSTER_NAME"
        cluster_cleanup "$WORKER1_KIND_CLUSTER_NAME"
        cluster_cleanup "$WORKER2_KIND_CLUSTER_NAME"
    fi
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

        cluster_create "$MANAGER_KIND_CLUSTER_NAME" "${ARTIFACTS}/manager-cluster.kind.yaml"
        cluster_create "$WORKER1_KIND_CLUSTER_NAME" "$SOURCE_DIR/multikueue/worker-cluster.kind.yaml"
        cluster_create "$WORKER2_KIND_CLUSTER_NAME" "$SOURCE_DIR/multikueue/worker-cluster.kind.yaml"
    fi
}

function kind_load {
    prepare_docker_images

    if [ "$CREATE_KIND_CLUSTER" == 'true' ]
    then
        cluster_kind_load "$MANAGER_KIND_CLUSTER_NAME"
        cluster_kind_load "$WORKER1_KIND_CLUSTER_NAME"
        cluster_kind_load "$WORKER2_KIND_CLUSTER_NAME"
    fi

    # JOBSET SETUP
    install_jobset "$MANAGER_KIND_CLUSTER_NAME"
    install_jobset "$WORKER1_KIND_CLUSTER_NAME"
    install_jobset "$WORKER2_KIND_CLUSTER_NAME"

    # APPWRAPPER SETUP
    install_appwrapper "$MANAGER_KIND_CLUSTER_NAME"
    install_appwrapper "$WORKER1_KIND_CLUSTER_NAME"
    install_appwrapper "$WORKER2_KIND_CLUSTER_NAME"

    # KUBEFLOW SETUP
    # In order for MPI-operator and Training-operator to work on the same cluster it is required that:
    # 1. 'kubeflow.org_mpijobs.yaml' is removed from base/crds/kustomization.yaml - https://github.com/kubeflow/training-operator/issues/1930
    # 2. Training-operator deployment is modified to enable all kubeflow jobs except for mpi -  https://github.com/kubeflow/training-operator/issues/1777
    install_kubeflow "$MANAGER_KIND_CLUSTER_NAME"
    install_kubeflow "$WORKER1_KIND_CLUSTER_NAME"
    install_kubeflow "$WORKER2_KIND_CLUSTER_NAME"
    
    ## MPI
    install_mpi "$MANAGER_KIND_CLUSTER_NAME"
    install_mpi "$WORKER1_KIND_CLUSTER_NAME"
    install_mpi "$WORKER2_KIND_CLUSTER_NAME"

    ## KUBERAY
    install_kuberay "$MANAGER_KIND_CLUSTER_NAME"
    install_kuberay "$WORKER1_KIND_CLUSTER_NAME"
    install_kuberay "$WORKER2_KIND_CLUSTER_NAME"
}

function multikueue_kueue_deploy {
    # We use a subshell to avoid overwriting the global cleanup trap, which also uses the EXIT signal.
    (
        set_managers_image
        trap restore_managers_image EXIT
        cluster_kueue_deploy "$MANAGER_KIND_CLUSTER_NAME"
        cluster_kueue_deploy "$WORKER1_KIND_CLUSTER_NAME"
        cluster_kueue_deploy "$WORKER2_KIND_CLUSTER_NAME"
    )
}

trap cleanup EXIT
startup
kind_load
multikueue_kueue_deploy

# shellcheck disable=SC2086
$GINKGO $GINKGO_ARGS --junit-report=junit.xml --json-report=e2e.json --output-dir="$ARTIFACTS" -v ./test/e2e/multikueue/...
