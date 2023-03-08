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

export KUSTOMIZE=$PWD/bin/kustomize
export GINKGO=$PWD/bin/ginkgo
export KIND=$PWD/bin/kind

function cleanup {
    if [ $CREATE_KIND_CLUSTER == 'true' ]
    then
        $KIND delete cluster --name $KIND_CLUSTER_NAME || { echo "You need to run make kind-image-build before this script"; exit -1; }
    fi
    (cd config/components/manager && $KUSTOMIZE edit set image controller=gcr.io/k8s-staging-kueue/kueue:main)
}

function startup {
    if [ $CREATE_KIND_CLUSTER == 'true' ]
    then
        $KIND create cluster --name $KIND_CLUSTER_NAME --image $E2E_KIND_VERSION
    fi
}

function kind_load {
    if [ $CREATE_KIND_CLUSTER == 'true' ]
    then
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
