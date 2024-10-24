#!/usr/bin/env bash

# Copyright 2024 The Kubernetes Authors.
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

export GINKGO="$ROOT_DIR"/bin/ginkgo
export KIND="$ROOT_DIR"/bin/kind

export E2E_TEST_BASH_IMAGE=registry.k8s.io/alpine-with-bash:1.0@sha256:0955672451201896cf9e2e5ce30bec0c7f10757af33bf78b7a6afa5672c596f5

# $1 cluster name
function cluster_create {
    $KIND create cluster --name "$1" --image "$E2E_KIND_VERSION" --wait 1m -v 5  > "$ARTIFACTS/$1-create.log" 2>&1 \
		||  { echo "unable to start the $1 cluster "; cat "$ARTIFACTS/$1-create.log" ; }
	kubectl config use-context "kind-$1"
    kubectl get nodes > "$ARTIFACTS/$1-nodes.log" || true
    kubectl describe pods -n kube-system > "$ARTIFACTS/$1-system-pods.log" || true
}

# $1 - cluster name
function cluster_cleanup {
	kubectl config use-context "kind-$1"
    $KIND export logs "$ARTIFACTS" --name "$1" || true
    kubectl describe pods -n kueue-system > "$ARTIFACTS/$1-kueue-system-pods.log" || true
    kubectl describe pods > "$ARTIFACTS/$1-default-pods.log" || true
    $KIND delete cluster --name "$1"
}

function startup {
    if [ "$CREATE_KIND_CLUSTER" == 'true' ]
    then
        if [ ! -d "$ARTIFACTS" ]; then
            mkdir -p "$ARTIFACTS"
        fi
	cluster_create "$KIND_CLUSTER_NAME"
    fi
}

function kind_load {
    if [ "$CREATE_KIND_CLUSTER" == 'true' ]
    then
        cluster_kind_load "$KIND_CLUSTER_NAME"
    fi
}

# $1 cluster
function cluster_kind_load {
    docker pull "${E2E_TEST_BASH_IMAGE}"
    e2e_test_bash_image_without_sha=${E2E_TEST_BASH_IMAGE%%@*}
    # We can load image by a digest but we cannot reference it by the digest that we pulled.
    # For more information https://github.com/kubernetes-sigs/kind/issues/2394#issuecomment-888713831.
    # Manually create tag for image with digest which is already pulled
    docker tag $E2E_TEST_BASH_IMAGE "$e2e_test_bash_image_without_sha"
    $KIND load docker-image "${e2e_test_bash_image_without_sha}" --name "$1"
}

function cleanup {
    if [ "$CREATE_KIND_CLUSTER" == 'true' ]
    then
        if [ ! -d "$ARTIFACTS" ]; then
            mkdir -p "$ARTIFACTS"
        fi
    uninstall_kjobctl
	cluster_cleanup "$KIND_CLUSTER_NAME"
    fi
}

function install_kjobctl {
    cd "$SOURCE_DIR"/.. && make install
}

function uninstall_kjobctl {
    cd "$SOURCE_DIR"/.. && make uninstall
}

trap cleanup EXIT
startup
kind_load
install_kjobctl
# shellcheck disable=SC2086
$GINKGO $GINKGO_ARGS --junit-report=junit.xml --json-report=e2e.json --output-dir="$ARTIFACTS" -v ./test/e2e/...
