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
export E2E_TEST_IMAGE=gcr.io/k8s-staging-perf-tests/sleep:v0.1.0
export MANAGER_KIND_CLUSTER_NAME=${KIND_CLUSTER_NAME}-manager
export WORKER1_KIND_CLUSTER_NAME=${KIND_CLUSTER_NAME}-worker1
export WORKER2_KIND_CLUSTER_NAME=${KIND_CLUSTER_NAME}-worker2

source ${SOURCE_DIR}/e2e-common.sh

function cleanup {
    if [ $CREATE_KIND_CLUSTER == 'true' ]
    then
        if [ ! -d "$ARTIFACTS" ]; then
            mkdir -p "$ARTIFACTS"
        fi

        cluster_cleanup $MANAGER_KIND_CLUSTER_NAME
        cluster_cleanup $WORKER1_KIND_CLUSTER_NAME
        cluster_cleanup $WORKER2_KIND_CLUSTER_NAME
    fi
    #do the image restore here for the case when an error happened during deploy
    restore_managers_image
}


function startup {
    if [ $CREATE_KIND_CLUSTER == 'true' ]
    then
        if [ ! -d "$ARTIFACTS" ]; then
            mkdir -p "$ARTIFACTS"
        fi

        cluster_create "$MANAGER_KIND_CLUSTER_NAME" "$SOURCE_DIR/multikueue/manager-cluster.kind.yaml"
        cluster_create $WORKER1_KIND_CLUSTER_NAME "$SOURCE_DIR/multikueue/worker-cluster.kind.yaml"
        cluster_create $WORKER2_KIND_CLUSTER_NAME "$SOURCE_DIR/multikueue/worker-cluster.kind.yaml"
    fi
}

function kind_load {
    if [ $CREATE_KIND_CLUSTER == 'true' ]
    then
        docker pull $E2E_TEST_IMAGE
        cluster_kind_load $MANAGER_KIND_CLUSTER_NAME
        cluster_kind_load $WORKER1_KIND_CLUSTER_NAME
        cluster_kind_load $WORKER2_KIND_CLUSTER_NAME 


    # JOBSET SETUP
    docker pull registry.k8s.io/jobset/jobset:$JOBSET_VERSION
    install_jobset $MANAGER_KIND_CLUSTER_NAME
    install_jobset $WORKER1_KIND_CLUSTER_NAME
    install_jobset $WORKER2_KIND_CLUSTER_NAME
    fi
}

function kueue_deploy {
    (cd config/components/manager && $KUSTOMIZE edit set image controller=$IMAGE_TAG)

    cluster_kueue_deploy $MANAGER_KIND_CLUSTER_NAME
    cluster_kueue_deploy $WORKER1_KIND_CLUSTER_NAME
    cluster_kueue_deploy $WORKER2_KIND_CLUSTER_NAME
}

function prepare_secrets {
    kubectl config use-context kind-${WORKER1_KIND_CLUSTER_NAME}
    source ${SOURCE_DIR}/create-multikueue-kubeconfig.sh ${ARTIFACTS}/worker1.kubeconfig
    $YQ e ".clusters[0].cluster.server = \"https://${WORKER1_KIND_CLUSTER_NAME}-control-plane:6443\"" ${ARTIFACTS}/worker1.kubeconfig > ${ARTIFACTS}/worker1.kubeconfig.internal

    kubectl config use-context kind-${WORKER2_KIND_CLUSTER_NAME}
    source ${SOURCE_DIR}/create-multikueue-kubeconfig.sh ${ARTIFACTS}/worker2.kubeconfig
    $YQ e ".clusters[0].cluster.server = \"https://${WORKER2_KIND_CLUSTER_NAME}-control-plane:6443\"" ${ARTIFACTS}/worker2.kubeconfig > ${ARTIFACTS}/worker2.kubeconfig.internal

    kubectl config use-context kind-${MANAGER_KIND_CLUSTER_NAME}
    kubectl create secret generic multikueue1 -n kueue-system --from-file=kubeconfig=${ARTIFACTS}/worker1.kubeconfig.internal
    kubectl create secret generic multikueue2 -n kueue-system --from-file=kubeconfig=${ARTIFACTS}/worker2.kubeconfig.internal
}

trap cleanup EXIT
startup
kind_load
kueue_deploy
prepare_secrets

$GINKGO $GINKGO_ARGS --junit-report=junit.xml --output-dir=$ARTIFACTS -v ./test/e2e/multikueue/...
