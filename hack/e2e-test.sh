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
ROOT_DIR=$SOURCE_DIR/..
export KUSTOMIZE=$ROOT_DIR/bin/kustomize
export GINKGO=$ROOT_DIR/bin/ginkgo
export KIND=$ROOT_DIR/bin/kind

function cleanup {
    if [ $CREATE_KIND_CLUSTER == 'true' ]
    then
        if [ ! -d "$ARTIFACTS" ]; then
            mkdir -p "$ARTIFACTS"
        fi
        kubectl logs -n kube-system kube-scheduler-kind-control-plane > $ARTIFACTS/kube-scheduler.log || true
        kubectl logs -n kube-system kube-controller-manager-kind-control-plane > $ARTIFACTS/kube-controller-manager.log || true
        kubectl logs -n kueue-system deployment/kueue-controller-manager > $ARTIFACTS/kueue-controller-manager.log || true
        kubectl describe pods -n kueue-system > $ARTIFACTS/kueue-system-pods.log || true
        $KIND delete cluster --name $KIND_CLUSTER_NAME
    fi
    (cd config/components/manager && $KUSTOMIZE edit set image controller=gcr.io/k8s-staging-kueue/kueue:main)
}

function startup {
    if [ $CREATE_KIND_CLUSTER == 'true' ]
    then
        if [ ! -d "$ARTIFACTS" ]; then
            mkdir -p "$ARTIFACTS"
        fi
        $KIND create cluster --name $KIND_CLUSTER_NAME --image $E2E_KIND_VERSION --config $SOURCE_DIR/kind-cluster.yaml --wait 1m -v 5  > $ARTIFACTS/kind-create.log 2>&1 \
		||  { echo "unable to start the kind cluster "; cat $ARTIFACTS/kind-create.log ; }
        kubectl get nodes > $ARTIFACTS/kind-nodes.log || true
        kubectl describe pods -n kube-system > $ARTIFACTS/kube-system-pods.log || true
    fi
}

function kind_load {
    if [ $CREATE_KIND_CLUSTER == 'true' ]
    then
        export E2E_IMAGES=$(grep '.Image(' ./test/e2e/e2e_test.go | cut -d '"' -f2 | sort | uniq)
        while read -r E2E_IMAGES; do
            docker pull $E2E_IMAGES
            $KIND load docker-image $E2E_IMAGES --name $KIND_CLUSTER_NAME
        done <<< "$E2E_IMAGES"
        $KIND load docker-image $IMAGE_TAG --name $KIND_CLUSTER_NAME
    fi
}

function kueue_deploy {
    (cd config/components/manager && $KUSTOMIZE edit set image controller=$IMAGE_TAG)
    kubectl apply --server-side -k test/e2e/config
}

trap cleanup EXIT
startup
kind_load
kueue_deploy
$GINKGO --junit-report=junit.xml --output-dir=$ARTIFACTS -v ./test/e2e/...
