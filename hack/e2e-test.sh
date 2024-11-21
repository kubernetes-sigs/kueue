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

# shellcheck source=hack/e2e-common.sh
source "${SOURCE_DIR}/e2e-common.sh"

function cleanup {
    if [ "$CREATE_KIND_CLUSTER" == 'true' ]
    then
        if [ ! -d "$ARTIFACTS" ]; then
            mkdir -p "$ARTIFACTS"
        fi
        cluster_cleanup "$KIND_CLUSTER_NAME"
    fi
    #do the image restore here for the case when an error happened during deploy
    restore_managers_image
}

function startup {
    if [ "$CREATE_KIND_CLUSTER" == 'true' ]
    then
        if [ ! -d "$ARTIFACTS" ]; then
            mkdir -p "$ARTIFACTS"
        fi
        cluster_create "$KIND_CLUSTER_NAME"  "$SOURCE_DIR/$KIND_CLUSTER_FILE"
    fi
}

function kind_load {
    prepare_docker_images

    if [ "$CREATE_KIND_CLUSTER" == 'true' ]; then
	      cluster_kind_load "$KIND_CLUSTER_NAME"
    fi

    if [[ -v JOBSET_VERSION ]]; then
        install_jobset "$KIND_CLUSTER_NAME"
    fi
    if [[ -v KUBEFLOW_VERSION ]]; then
        install_kubeflow "$KIND_CLUSTER_NAME"
    fi
    if [[ -v KUBEFLOW_MPI_VERSION ]]; then
        install_mpi "$KIND_CLUSTER_NAME"
    fi
}

function kueue_deploy {
    (cd config/components/manager && $KUSTOMIZE edit set image controller="$IMAGE_TAG")
    cluster_kueue_deploy "$KIND_CLUSTER_NAME"
}

trap cleanup EXIT
startup
kind_load
kueue_deploy
# shellcheck disable=SC2086
$GINKGO $GINKGO_ARGS --junit-report=junit.xml --json-report=e2e.json --output-dir="$ARTIFACTS" -v ./test/e2e/$E2E_TARGET_FOLDER/...
