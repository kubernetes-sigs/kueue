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

export KUSTOMIZE="$ROOT_DIR"/bin/kustomize
export GINKGO="$ROOT_DIR"/bin/ginkgo
export KIND="$ROOT_DIR"/bin/kind
export YQ="$ROOT_DIR"/bin/yq

export JOBSET_MANIFEST="https://github.com/kubernetes-sigs/jobset/releases/download/${JOBSET_VERSION}/manifests.yaml"
export JOBSET_IMAGE=registry.k8s.io/jobset/jobset:${JOBSET_VERSION}
export JOBSET_CRDS=${ROOT_DIR}/dep-crds/jobset-operator/

#no matching semver tag unfortunately
export KUBEFLOW_IMAGE=kubeflow/training-operator:v1-855e096
export KUBEFLOW_CRDS=${ROOT_DIR}/dep-crds/training-operator
export KUBEFLOW_CRDS_BASE=${KUBEFLOW_CRDS}/base/crds
export KUBEFLOW_MANIFEST=${KUBEFLOW_CRDS}/overlays/standalone

export KUBEFLOW_MPI_MANIFEST="https://raw.githubusercontent.com/kubeflow/mpi-operator/${KUBEFLOW_MPI_VERSION}/deploy/v2beta1/mpi-operator.yaml" #"https://github.com/kubeflow/mpi-operator/manifests/overlays/standalone?ref=${KUBEFLOW_MPI_VERSION}"
export KUBEFLOW_MPI_IMAGE=mpioperator/mpi-operator:${KUBEFLOW_MPI_VERSION/#v}
export KUBEFLOW_MPI_CRD=${ROOT_DIR}/dep-crds/mpi-operator/kubeflow.org_mpijobs.yaml

# $1 - cluster name
function cluster_cleanup {
	kubectl config use-context "kind-$1"
        $KIND export logs "$ARTIFACTS" --name "$1" || true
        kubectl describe pods -n kueue-system > "$ARTIFACTS/$1-kueue-system-pods.log" || true
        kubectl describe pods > "$ARTIFACTS/$1-default-pods.log" || true
        $KIND delete cluster --name "$1"
}

# $1 cluster name
# $2 cluster kind config
function cluster_create {
        $KIND create cluster --name "$1" --image "$E2E_KIND_VERSION" --config "$2" --wait 1m -v 5  > "$ARTIFACTS/$1-create.log" 2>&1 \
		||  { echo "unable to start the $1 cluster "; cat "$ARTIFACTS/$1-create.log" ; }
	kubectl config use-context "kind-$1"
        kubectl get nodes > "$ARTIFACTS/$1-nodes.log" || true
        kubectl describe pods -n kube-system > "$ARTIFACTS/$1-system-pods.log" || true
}

# $1 cluster
function cluster_kind_load {
	cluster_kind_load_image "$1" "$E2E_TEST_IMAGE"
	cluster_kind_load_image "$1" "$IMAGE_TAG"
}

# $1 cluster
# $2 image
function cluster_kind_load_image {
        $KIND load docker-image "$2" --name "$1"
}

# $1 cluster
function cluster_kueue_deploy {
    kubectl config use-context "kind-${1}"
    kubectl apply --server-side -k test/e2e/config
}

#$1 - cluster name
function install_jobset {
    cluster_kind_load_image "${1}" "${JOBSET_IMAGE}"
    kubectl config use-context "kind-${1}"
    kubectl apply --server-side -f "${JOBSET_MANIFEST}"
}

function patch_kubeflow_manifest {
    # In order for MPI-operator and Training-operator to work on the same cluster it is required that:
    # 1. 'kubeflow.org_mpijobs.yaml' is removed from base/crds/kustomization.yaml - https://github.com/kubeflow/training-operator/issues/1930
    # Done already in Makefile-deps.mk target `kf-training-operator-crd`
    # 2. Training-operator deployment file is patched and manually enabled for all kubeflow jobs except for mpi -  https://github.com/kubeflow/training-operator/issues/1777
    KUBEFLOW_DEPLOYMENT="${KUBEFLOW_CRDS}/base/deployment.yaml"

    # Find the line after which to insert the args
    INSERT_LINE=$(grep -n "^ *- /manager" "${KUBEFLOW_DEPLOYMENT}" | head -n 1 | cut -d ':' -f 1)

    # Prepare patch with the args after the specified line
    # EOF must be indented most left - doesn't work otherwise
    ARGS_PATCH=$(cat <<EOF
          args:
            - --enable-scheme=tfjob
            - --enable-scheme=pytorchjob
            - --enable-scheme=xgboostjob
            - --enable-scheme=paddlejob
EOF
)
    # Apply the patch
    sed -i -e "${INSERT_LINE}r /dev/stdin" "${KUBEFLOW_DEPLOYMENT}" << EOF
${ARGS_PATCH}
EOF
}

#$1 - cluster name
function install_kubeflow {
    cluster_kind_load_image "${1}" "${KUBEFLOW_IMAGE}"
    kubectl config use-context "kind-${1}"
    kubectl apply -k "${KUBEFLOW_MANIFEST}"
}

#$1 - cluster name
function install_mpi {
    cluster_kind_load_image "${1}" "${KUBEFLOW_MPI_IMAGE/#v}"
    kubectl config use-context "kind-${1}"
    kubectl apply --server-side -f "${KUBEFLOW_MPI_MANIFEST}"
}

INITIAL_IMAGE=$($YQ '.images[] | select(.name == "controller") | [.newName, .newTag] | join(":")' config/components/manager/kustomization.yaml)
export INITIAL_IMAGE

function restore_managers_image {
    (cd config/components/manager && $KUSTOMIZE edit set image controller="$INITIAL_IMAGE")
}
