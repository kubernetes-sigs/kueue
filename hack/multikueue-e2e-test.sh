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
export E2E_TEST_IMAGE=gcr.io/k8s-staging-perf-tests/sleep:v0.0.3
export LEADER_KIND_CLUSTER_NAME=${KIND_CLUSTER_NAME}-leader
export WORKER_KIND_CLUSTER_NAME=${KIND_CLUSTER_NAME}-worker

# $1 - cluster name
function cluster_cleanup {
	kubectl config use-context kind-$1
        kubectl logs -n kube-system kube-scheduler-$1-control-plane > $ARTIFACTS/$1-kube-scheduler.log || true
        kubectl logs -n kube-system kube-controller-manager-$1-control-plane > $ARTIFACTS/$1-kube-controller-manager.log || true
        kubectl logs -n kueue-system deployment/kueue-controller-manager > $ARTIFACTS/$1-kueue-controller-manager.log || true
        kubectl describe pods -n kueue-system > $ARTIFACTS/$1-kueue-system-pods.log || true
        kubectl describe pods > $ARTIFACTS/$1-default-pods.log || true
        $KIND delete cluster --name $1
}

function cleanup {
    if [ $CREATE_KIND_CLUSTER == 'true' ]
    then
        if [ ! -d "$ARTIFACTS" ]; then
            mkdir -p "$ARTIFACTS"
        fi

	cluster_cleanup $LEADER_KIND_CLUSTER_NAME
	cluster_cleanup $WORKER_KIND_CLUSTER_NAME
    fi
    (cd config/components/manager && $KUSTOMIZE edit set image controller=gcr.io/k8s-staging-kueue/kueue:main)
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

function startup {
    if [ $CREATE_KIND_CLUSTER == 'true' ]
    then
        if [ ! -d "$ARTIFACTS" ]; then
            mkdir -p "$ARTIFACTS"
        fi

	cluster_create $LEADER_KIND_CLUSTER_NAME $SOURCE_DIR/mk-leader-cluster.yaml

	# NOTE: for local setup, make sure that your firewalk allows tcp from leader to the GW ip
	# eg. ufw `sudo ufw allow from 172.18.0.0/16 proto tcp to 172.18.0.1`
	#
	# eg. iptables    `sudo iptables --append INPUT --protocol tcp --src 172.18.0.0/16 --dst 172.18.0.1 --jump ACCEPT
	#                  sudo iptables --append OUTPUT --protocol tcp --src 172.18.0.1 --dst 172.18.0./0/16 --jump ACCEPT`

	# have the worker forward the api to the docker gateway address instead of lo
	export GW=$(docker inspect ${LEADER_KIND_CLUSTER_NAME}-control-plane -f '{{.NetworkSettings.Networks.kind.Gateway}}')
	$YQ e '.networking.apiServerAddress=env(GW)'  $SOURCE_DIR/mk-worker-cluster.yaml > $ARTIFACTS/worker-cluster.yaml

	cluster_create $WORKER_KIND_CLUSTER_NAME $ARTIFACTS/worker-cluster.yaml

	# push the worker kubeconfig in a leader's secret
	$KIND get kubeconfig --name $WORKER_KIND_CLUSTER_NAME  > ${ARTIFACTS}/worker.kubeconfig
	kubectl config use-context kind-${LEADER_KIND_CLUSTER_NAME}
	kubectl create secret generic multikueue --from-file=${ARTIFACTS}/worker.kubeconfig
    fi
}

# $1 cluster
function cluster_kind_load {
        $KIND load docker-image $E2E_TEST_IMAGE --name $1
        $KIND load docker-image $IMAGE_TAG --name $1
}

function kind_load {
    if [ $CREATE_KIND_CLUSTER == 'true' ]
    then
        docker pull $E2E_TEST_IMAGE
        cluster_kind_load $LEADER_KIND_CLUSTER_NAME
        cluster_kind_load $WORKER_KIND_CLUSTER_NAME
    fi
}

function kueue_deploy {
    (cd config/components/manager && $KUSTOMIZE edit set image controller=$IMAGE_TAG)
    kubectl config use-context kind-${LEADER_KIND_CLUSTER_NAME}
    kubectl apply --server-side -k test/e2e/config

    kubectl config use-context kind-${WORKER_KIND_CLUSTER_NAME}
    kubectl apply --server-side -k test/e2e/config

    #wait for the manager to start
    kubectl config use-context kind-$LEADER_KIND_CLUSTER_NAME
    kubectl wait -n kueue-system deployment kueue-controller-manager --for condition=Available --timeout=120s
    kubectl config use-context kind-$WORKER_KIND_CLUSTER_NAME
    kubectl wait -n kueue-system deployment kueue-controller-manager --for condition=Available --timeout=120s
}

trap cleanup EXIT
startup
kind_load
kueue_deploy

#TODO: run some actual tests
#$GINKGO --junit-report=junit.xml --output-dir=$ARTIFACTS -v ./test/multikueue-e2e/...


#TODO: remove the following lines

# even if waiting for deployment, the webhook server might not be up, wait an extra 10 sec
sleep 10

kubectl config use-context kind-$LEADER_KIND_CLUSTER_NAME
kubectl apply -f $SOURCE_DIR/kubectl.yaml
kubectl wait pods kubectl --for condition=Ready --timeout=180s
kubectl exec kubectl -- kubectl run testpod --image=$E2E_TEST_IMAGE
kubectl config use-context kind-$WORKER_KIND_CLUSTER_NAME
kubectl wait pods testpod --for condition=Initialized --timeout=180s

