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

export KIND_VERSION="${E2E_KIND_VERSION/"kindest/node:v"/}"

if [[ -n ${APPWRAPPER_VERSION:-} ]]; then
    export APPWRAPPER_MANIFEST=${ROOT_DIR}/dep-crds/appwrapper/config/default
    APPWRAPPER_IMAGE=quay.io/ibm/appwrapper:${APPWRAPPER_VERSION}
fi

if [[ -n ${JOBSET_VERSION:-} ]]; then
    export JOBSET_MANIFEST="https://github.com/kubernetes-sigs/jobset/releases/download/${JOBSET_VERSION}/manifests.yaml"
    export JOBSET_IMAGE=registry.k8s.io/jobset/jobset:${JOBSET_VERSION}
    export JOBSET_CRDS=${ROOT_DIR}/dep-crds/jobset-operator/
fi

if [[ -n ${KUBEFLOW_VERSION:-} ]]; then
    export KUBEFLOW_MANIFEST_ORIG=${ROOT_DIR}/dep-crds/training-operator/manifests/overlays/standalone/kustomization.yaml
    export KUBEFLOW_MANIFEST_PATCHED=${ROOT_DIR}/test/e2e/config/multikueue
    # Extract the Kubeflow Training Operator image version tag (newTag) from the manifest.
    # This is necessary because the image version tag does not follow the usual package versioning convention.
    KUBEFLOW_IMAGE_VERSION=$($YQ '.images[] | select(.name | contains("training-operator")) | .newTag' "${KUBEFLOW_MANIFEST_ORIG}")
    export KUBEFLOW_IMAGE_VERSION
    export KUBEFLOW_IMAGE=kubeflow/training-operator:${KUBEFLOW_IMAGE_VERSION}
fi

if [[ -n ${KUBEFLOW_MPI_VERSION:-} ]]; then
    export KUBEFLOW_MPI_MANIFEST="https://raw.githubusercontent.com/kubeflow/mpi-operator/${KUBEFLOW_MPI_VERSION}/deploy/v2beta1/mpi-operator.yaml"
    export KUBEFLOW_MPI_IMAGE=mpioperator/mpi-operator:${KUBEFLOW_MPI_VERSION/#v}
fi

if [[ -n ${KUBERAY_VERSION:-} ]]; then
    export KUBERAY_MANIFEST="${ROOT_DIR}/dep-crds/ray-operator/default/"
    export KUBERAY_IMAGE=quay.io/kuberay/operator:${KUBERAY_VERSION}
    export KUBERAY_RAY_IMAGE=rayproject/ray:2.9.0
    export KUBERAY_RAY_IMAGE_ARM=rayproject/ray:2.9.0-aarch64
fi

if [[ -n ${LEADERWORKERSET_VERSION:-} ]]; then
    export LEADERWORKERSET_MANIFEST="https://github.com/kubernetes-sigs/lws/releases/download/${LEADERWORKERSET_VERSION}/manifests.yaml"
    export LEADERWORKERSET_IMAGE=registry.k8s.io/lws/lws:${LEADERWORKERSET_VERSION}
fi

if [[ -n "${CERTMANAGER_VERSION:-}" ]]; then
    export CERTMANAGER_MANIFEST="https://github.com/cert-manager/cert-manager/releases/download/${CERTMANAGER_VERSION}/cert-manager.yaml"
fi

# agnhost image to use for testing.
export E2E_TEST_AGNHOST_IMAGE_OLD=registry.k8s.io/e2e-test-images/agnhost:2.52@sha256:b173c7d0ffe3d805d49f4dfe48375169b7b8d2e1feb81783efd61eb9d08042e6
E2E_TEST_AGNHOST_IMAGE_OLD_WITHOUT_SHA=${E2E_TEST_AGNHOST_IMAGE_OLD%%@*}
export E2E_TEST_AGNHOST_IMAGE=registry.k8s.io/e2e-test-images/agnhost:2.53@sha256:99c6b4bb4a1e1df3f0b3752168c89358794d02258ebebc26bf21c29399011a85
E2E_TEST_AGNHOST_IMAGE_WITHOUT_SHA=${E2E_TEST_AGNHOST_IMAGE%%@*}


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

function prepare_docker_images {
    docker pull "$E2E_TEST_AGNHOST_IMAGE_OLD"
    docker pull "$E2E_TEST_AGNHOST_IMAGE"

    # We can load image by a digest but we cannot reference it by the digest that we pulled.
    # For more information https://github.com/kubernetes-sigs/kind/issues/2394#issuecomment-888713831.
    # Manually create tag for image with digest which is already pulled
    docker tag $E2E_TEST_AGNHOST_IMAGE_OLD "$E2E_TEST_AGNHOST_IMAGE_OLD_WITHOUT_SHA"
    docker tag $E2E_TEST_AGNHOST_IMAGE "$E2E_TEST_AGNHOST_IMAGE_WITHOUT_SHA"

    if [[ -n ${APPWRAPPER_VERSION:-} ]]; then
        docker pull "${APPWRAPPER_IMAGE}"
    fi
    if [[ -n ${JOBSET_VERSION:-} ]]; then
        docker pull "${JOBSET_IMAGE}"
    fi
    if [[ -n ${KUBEFLOW_VERSION:-} ]]; then
        docker pull "${KUBEFLOW_IMAGE}"
    fi
    if [[ -n ${KUBEFLOW_MPI_VERSION:-} ]]; then
        docker pull "${KUBEFLOW_MPI_IMAGE}"
    fi
    if [[ -n ${KUBERAY_VERSION:-} ]]; then
        docker pull "${KUBERAY_IMAGE}"

        # Extra e2e images required for Kuberay
        unamestr=$(uname)
        if [[ "$unamestr" == 'Linux' ]]; then
            docker pull "${KUBERAY_RAY_IMAGE}"
        elif [[ "$unamestr" == 'Darwin' ]]; then
            docker pull "${KUBERAY_RAY_IMAGE_ARM}"
        fi
    fi
    if [[ -n ${LEADERWORKERSET_VERSION:-} ]]; then
        docker pull "${LEADERWORKERSET_IMAGE}"
    fi
}

# $1 cluster
function cluster_kind_load {
    cluster_kind_load_image "$1" "${E2E_TEST_AGNHOST_IMAGE_OLD_WITHOUT_SHA}"
    cluster_kind_load_image "$1" "${E2E_TEST_AGNHOST_IMAGE_WITHOUT_SHA}"
    cluster_kind_load_image "$1" "$IMAGE_TAG"
}

# $1 cluster
# $2 image
function cluster_kind_load_image {
    # check if the command to get worker nodes could succeeded
    if ! $KIND get nodes --name "$1" > /dev/null 2>&1; then
        echo "Failed to retrieve nodes for cluster '$1'."
        return 1
    fi
    # filter out 'control-plane' node, use only worker nodes to load image
    worker_nodes=$($KIND get nodes --name "$1" | grep -v 'control-plane' | paste -sd "," -)
    if [[ -n "$worker_nodes" ]]; then
        $KIND load docker-image "$2" --name "$1" --nodes "$worker_nodes"
    fi
}

# Wait until all cert-manager deployments are available.
function wait_for_cert_manager_ready() {
    echo "Waiting for cert-manager components to be ready..."
    local deployments=(cert-manager cert-manager-cainjector cert-manager-webhook)
    for dep in "${deployments[@]}"; do
        echo "Waiting for deployment '$dep'..."
        if ! kubectl wait --for=condition=Available deployment/"$dep" -n cert-manager --timeout=300s; then
            echo "Timeout waiting for deployment '$dep' to become available."
            exit 1
        fi
    done
    echo "All cert-manager components are ready."
}

# $1 cluster
function cluster_kueue_deploy {
    kubectl config use-context "kind-${1}"
    if [[ -n ${CERTMANAGER_VERSION:-} ]]; then
       wait_for_cert_manager_ready
       kubectl apply --server-side -k test/e2e/config/certmanager
    else
       kubectl apply --server-side -k test/e2e/config/default  
    fi
}

#$1 - cluster name
function install_appwrapper {
    cluster_kind_load_image "${1}" "${APPWRAPPER_IMAGE}"
    kubectl config use-context "kind-${1}"
    kubectl apply -k "${APPWRAPPER_MANIFEST}"
}

#$1 - cluster name
function install_jobset {
    cluster_kind_load_image "${1}" "${JOBSET_IMAGE}"
    kubectl config use-context "kind-${1}"
    kubectl apply --server-side -f "${JOBSET_MANIFEST}"
}

#$1 - cluster name
function install_kubeflow {
    cluster_kind_load_image "${1}" "${KUBEFLOW_IMAGE}"
    kubectl config use-context "kind-${1}"
    kubectl apply --server-side -k "${KUBEFLOW_MANIFEST_PATCHED}"
}

#$1 - cluster name
function install_mpi {
    cluster_kind_load_image "${1}" "${KUBEFLOW_MPI_IMAGE/#v}"
    kubectl config use-context "kind-${1}"
    kubectl apply --server-side -f "${KUBEFLOW_MPI_MANIFEST}"
}

#$1 - cluster name
function install_kuberay {
    # Extra e2e images required for Kuberay
    unamestr=$(uname)
    if [[ "$unamestr" == 'Linux' ]]; then
        cluster_kind_load_image "${1}" "${KUBERAY_RAY_IMAGE}"
    elif [[ "$unamestr" == 'Darwin' ]]; then
        cluster_kind_load_image "${1}" "${KUBERAY_RAY_IMAGE_ARM}"
    fi 

    cluster_kind_load_image "${1}" "${KUBERAY_IMAGE}"
    kubectl config use-context "kind-${1}"
    # create used instead of apply - https://github.com/ray-project/kuberay/issues/504
    kubectl create -k "${KUBERAY_MANIFEST}"
}

function install_lws {
    cluster_kind_load_image "${1}" "${LEADERWORKERSET_IMAGE/#v}"
    kubectl config use-context "kind-${1}"
    kubectl apply --server-side -f "${LEADERWORKERSET_MANIFEST}"
}

function install_cert_manager {
    kubectl config use-context "kind-${1}"
    kubectl apply --server-side -f "${CERTMANAGER_MANIFEST}"
}

INITIAL_IMAGE=$($YQ '.images[] | select(.name == "controller") | [.newName, .newTag] | join(":")' config/components/manager/kustomization.yaml)
export INITIAL_IMAGE

function restore_managers_image {
    (cd config/components/manager && $KUSTOMIZE edit set image controller="$INITIAL_IMAGE")
}
