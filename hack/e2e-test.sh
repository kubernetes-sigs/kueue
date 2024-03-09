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
ROOT_DIR="$SOURCE_DIR/.."
export KUSTOMIZE="$ROOT_DIR"/bin/kustomize
export GINKGO="$ROOT_DIR"/bin/ginkgo
export KIND="$ROOT_DIR"/bin/kind
export E2E_TEST_IMAGE=gcr.io/k8s-staging-perf-tests/sleep:v0.1.0

source ${SOURCE_DIR}/e2e-common.sh

function cleanup {
    if [ $CREATE_KIND_CLUSTER == 'true' ]
    then
        if [ ! -d "$ARTIFACTS" ]; then
            mkdir -p "$ARTIFACTS"
        fi
	cluster_cleanup $KIND_CLUSTER_NAME
    fi
    exit 0
}

function startup {
    if [ $CREATE_KIND_CLUSTER == 'true' ]
    then
        if [ ! -d "$ARTIFACTS" ]; then
            mkdir -p "$ARTIFACTS"
        fi
	cluster_create "$KIND_CLUSTER_NAME"  "$SOURCE_DIR/kind-cluster.yaml" 
    fi
}

function kind_load {
    if [ $CREATE_KIND_CLUSTER == 'true' ]
    then
        docker pull $E2E_TEST_IMAGE
	cluster_kind_load $KIND_CLUSTER_NAME
    fi
}

function kueue_deploy {
    (cd config/components/manager && $KUSTOMIZE edit set image controller=$IMAGE_TAG)
    cluster_kueue_deploy $KIND_CLUSTER_NAME
}

trap cleanup EXIT
startup
kind_load
kueue_deploy
$GINKGO $GINKGO_ARGS --junit-report=junit.xml --output-dir=$ARTIFACTS -v ./test/e2e/singlecluster/...
