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



# $1 - cluster name
function cluster_cleanup {
	kubectl config use-context kind-$1
        $KIND export logs $ARTIFACTS --name $1 || true
        kubectl describe pods -n kueue-system > $ARTIFACTS/$1-kueue-system-pods.log || true
        kubectl describe pods > $ARTIFACTS/$1-default-pods.log || true
        $KIND delete cluster --name $1
}

# $1 cluster name
# $2 cluster kind config
function cluster_create {
        $KIND create cluster --name $1 --image $E2E_KIND_VERSION --config $2 --wait 1m -v 5  > $ARTIFACTS/$1-create.log 2>&1 \
		||  { echo "unable to start the $1 cluster "; cat $ARTIFACTS/$1-create.log ; }
	kubectl config use-context kind-$1
        kubectl get nodes > $ARTIFACTS/$1-nodes.log || true
        kubectl describe pods -n kube-system > $ARTIFACTS/$1-system-pods.log || true
}

# $1 cluster
function cluster_kind_load {
        $KIND load docker-image $E2E_TEST_IMAGE --name $1
        $KIND load docker-image $IMAGE_TAG --name $1
}

# $1 cluster
function cluster_kueue_deploy {
    kubectl config use-context kind-${1}
    kubectl apply --server-side -k test/e2e/config
}

