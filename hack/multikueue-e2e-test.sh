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
ROOT_DIR=$SOURCE_DIR/..
export KUSTOMIZE=$ROOT_DIR/bin/kustomize
export GINKGO=$ROOT_DIR/bin/ginkgo
export KIND=$ROOT_DIR/bin/kind
export YQ=$ROOT_DIR/bin/yq
export E2E_TEST_IMAGE=gcr.io/k8s-staging-perf-tests/sleep:v0.1.0
export MANAGER_KIND_CLUSTER_NAME=${KIND_CLUSTER_NAME}-manager
export WORKER1_KIND_CLUSTER_NAME=${KIND_CLUSTER_NAME}-worker1
export WORKER2_KIND_CLUSTER_NAME=${KIND_CLUSTER_NAME}-worker2

export JOBSET_MANIFEST=https://github.com/kubernetes-sigs/jobset/releases/download/${JOBSET_VERSION}/manifests.yaml
export JOBSET_IMAGE=registry.k8s.io/jobset/jobset:${JOBSET_VERSION}
export JOBSET_CRDS=${ROOT_DIR}/dep-crds/jobset-operator/

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
    (cd config/components/manager && $KUSTOMIZE edit set image controller=gcr.io/k8s-staging-kueue/kueue:main)
}


function startup {
    if [ $CREATE_KIND_CLUSTER == 'true' ]
    then
        if [ ! -d "$ARTIFACTS" ]; then
            mkdir -p "$ARTIFACTS"
        fi

	cluster_create $MANAGER_KIND_CLUSTER_NAME $SOURCE_DIR/multikueue/manager-cluster.kind.yaml

	# NOTE: for local setup, make sure that your firewall allows tcp from manager to the GW ip
	# eg. ufw `sudo ufw allow from 172.18.0.0/16 proto tcp to 172.18.0.1`
	#
	# eg. iptables    `sudo iptables --append INPUT --protocol tcp --src 172.18.0.0/16 --dst 172.18.0.1 --jump ACCEPT
	#                  sudo iptables --append OUTPUT --protocol tcp --src 172.18.0.1 --dst 172.18.0./0/16 --jump ACCEPT`

	# have the worker forward the api to the docker gateway address instead of lo
	export GW=$(docker inspect ${MANAGER_KIND_CLUSTER_NAME}-control-plane -f '{{.NetworkSettings.Networks.kind.Gateway}}')
	$YQ e '.networking.apiServerAddress=env(GW)'  $SOURCE_DIR/multikueue/worker-cluster.kind.yaml > $ARTIFACTS/worker-cluster.yaml


	cluster_create $WORKER1_KIND_CLUSTER_NAME $ARTIFACTS/worker-cluster.yaml
	
	cluster_create $WORKER2_KIND_CLUSTER_NAME $ARTIFACTS/worker-cluster.yaml

	# push the worker kubeconfig in a manager's secret
	$KIND get kubeconfig --name $WORKER1_KIND_CLUSTER_NAME  > ${ARTIFACTS}/worker1.kubeconfig
	$KIND get kubeconfig --name $WORKER2_KIND_CLUSTER_NAME  > ${ARTIFACTS}/worker2.kubeconfig
	kubectl config use-context kind-${MANAGER_KIND_CLUSTER_NAME}
	kubectl create namespace kueue-system
	kubectl create secret generic multikueue1 -n kueue-system --from-file=kubeconfig=${ARTIFACTS}/worker1.kubeconfig
	kubectl create secret generic multikueue2 -n kueue-system --from-file=kubeconfig=${ARTIFACTS}/worker2.kubeconfig
    fi
}


#$1 - cluster name
function istall_jobset {
	kubectl config use-context kind-${1}
	kubectl apply --server-side -f https://github.com/kubernetes-sigs/jobset/releases/download/$JOBSET_VERSION/manifests.yaml
}

function kind_load {
    if [ $CREATE_KIND_CLUSTER == 'true' ]
    then
        docker pull $E2E_TEST_IMAGE
        cluster_kind_load $MANAGER_KIND_CLUSTER_NAME
        cluster_kind_load $WORKER1_KIND_CLUSTER_NAME
        cluster_kind_load $WORKER2_KIND_CLUSTER_NAME 


	# JOBSET SETUP
	# MANAGER
	kubectl config use-context kind-${MANAGER_KIND_CLUSTER_NAME}
	kubectl apply --server-side -f ${JOBSET_CRDS}/*

	#WORKERS
	docker pull registry.k8s.io/jobset/jobset:$JOBSET_VERSION

	cluster_kind_load_image $WORKER1_KIND_CLUSTER_NAME $JOBSET_IMAGE
	kubectl config use-context kind-${WORKER1_KIND_CLUSTER_NAME}
	kubectl apply --server-side -f $JOBSET_MANIFEST

	cluster_kind_load_image $WORKER2_KIND_CLUSTER_NAME $JOBSET_IMAGE
	kubectl config use-context kind-${WORKER2_KIND_CLUSTER_NAME}
	kubectl apply --server-side -f $JOBSET_MANIFEST


    fi
}

function kueue_deploy {
    (cd config/components/manager && $KUSTOMIZE edit set image controller=$IMAGE_TAG)

    cluster_kueue_deploy $MANAGER_KIND_CLUSTER_NAME
    cluster_kueue_deploy $WORKER1_KIND_CLUSTER_NAME
    cluster_kueue_deploy $WORKER2_KIND_CLUSTER_NAME
}

trap cleanup EXIT
startup
kind_load
kueue_deploy 

$GINKGO $GINKGO_ARGS --junit-report=junit.xml --output-dir=$ARTIFACTS -v ./test/e2e/multikueue/... 
