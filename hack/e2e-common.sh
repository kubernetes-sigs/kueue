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
export HELM="$ROOT_DIR"/bin/helm
export KUEUE_NAMESPACE="${KUEUE_NAMESPACE:-kueue-system}"

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
fi

if [[ -n ${LEADERWORKERSET_VERSION:-} ]]; then
    export LEADERWORKERSET_MANIFEST="https://github.com/kubernetes-sigs/lws/releases/download/${LEADERWORKERSET_VERSION}/manifests.yaml"
    export LEADERWORKERSET_IMAGE=registry.k8s.io/lws/lws:${LEADERWORKERSET_VERSION}
fi

if [[ -n "${CERTMANAGER_VERSION:-}" ]]; then
    export CERTMANAGER_MANIFEST="https://github.com/cert-manager/cert-manager/releases/download/${CERTMANAGER_VERSION}/cert-manager.yaml"
fi

# agnhost image to use for testing.
E2E_TEST_AGNHOST_IMAGE_OLD_WITH_SHA=registry.k8s.io/e2e-test-images/agnhost:2.52@sha256:b173c7d0ffe3d805d49f4dfe48375169b7b8d2e1feb81783efd61eb9d08042e6
export E2E_TEST_AGNHOST_IMAGE_OLD=${E2E_TEST_AGNHOST_IMAGE_OLD_WITH_SHA%%@*}
E2E_TEST_AGNHOST_IMAGE_WITH_SHA=$(grep '^FROM' "${SOURCE_DIR}/agnhost/Dockerfile" | awk '{print $2}')
export E2E_TEST_AGNHOST_IMAGE=${E2E_TEST_AGNHOST_IMAGE_WITH_SHA%%@*}


# $1 cluster name
# $2 kubeconfig
function cluster_cleanup {
    kubectl config --kubeconfig="$2" use-context "kind-$1"
    
    $KIND export logs "$ARTIFACTS" --name "$1" || true
    kubectl describe pods --kubeconfig="$2" -n kueue-system > "$ARTIFACTS/$1-kueue-system-pods.log" || true
    kubectl describe pods --kubeconfig="$2" > "$ARTIFACTS/$1-default-pods.log" || true
    $KIND delete cluster --name "$1"
}

# $1 cluster name
# $2 cluster kind config
# $3 kubeconfig
function cluster_create {
    prepare_kubeconfig "$1" "$3"

    $KIND create cluster --name "$1" --image "$E2E_KIND_VERSION" --config "$2" --kubeconfig="$3" --wait 1m -v 5  > "$ARTIFACTS/$1-create.log" 2>&1 \
    ||  { echo "unable to start the $1 cluster "; cat "$ARTIFACTS/$1-create.log" ; }
 
    kubectl config --kubeconfig="$3" use-context "kind-$1"
    kubectl get nodes --kubeconfig="$3" > "$ARTIFACTS/$1-nodes.log" || true
    kubectl describe pods --kubeconfig="$3" -n kube-system > "$ARTIFACTS/$1-system-pods.log" || true
}

function prepare_docker_images {
    docker pull "$E2E_TEST_AGNHOST_IMAGE_OLD_WITH_SHA"
    docker pull "$E2E_TEST_AGNHOST_IMAGE_WITH_SHA"

    # We can load image by a digest but we cannot reference it by the digest that we pulled.
    # For more information https://github.com/kubernetes-sigs/kind/issues/2394#issuecomment-888713831.
    # Manually create tag for image with digest which is already pulled
    docker tag "$E2E_TEST_AGNHOST_IMAGE_OLD_WITH_SHA" "$E2E_TEST_AGNHOST_IMAGE_OLD"
    docker tag "$E2E_TEST_AGNHOST_IMAGE_WITH_SHA" "$E2E_TEST_AGNHOST_IMAGE"

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
        determine_kuberay_ray_image
        if [[ ${USE_RAY_FOR_TESTS:-} == "ray" ]]; then
            docker pull "${KUBERAY_RAY_IMAGE}"
        fi
    fi
    if [[ -n ${LEADERWORKERSET_VERSION:-} ]]; then
        docker pull "${LEADERWORKERSET_IMAGE}"
    fi
}

# $1 cluster
function cluster_kind_load {
    cluster_kind_load_image "$1" "${E2E_TEST_AGNHOST_IMAGE_OLD}"
    cluster_kind_load_image "$1" "${E2E_TEST_AGNHOST_IMAGE}"
    cluster_kind_load_image "$1" "$IMAGE_TAG"
}

# $1 cluster
# $2 kubeconfig
function kind_load {
    kubectl config --kubeconfig="$2" use-context "kind-$1"

    if [ "$CREATE_KIND_CLUSTER" == 'true' ]; then
	    cluster_kind_load "$1"
    fi
    if [[ -n ${APPWRAPPER_VERSION:-} ]]; then
        install_appwrapper "$1" "$2"
    fi
    if [[ -n ${JOBSET_VERSION:-} ]]; then
        install_jobset "$1" "$2"
    fi
    if [[ -n ${KUBEFLOW_VERSION:-} ]]; then
        # In order for MPI-operator and Training-operator to work on the same cluster it is required that:
        # 1. 'kubeflow.org_mpijobs.yaml' is removed from base/crds/kustomization.yaml - https://github.com/kubeflow/training-operator/issues/1930
        # 2. Training-operator deployment is modified to enable all kubeflow jobs except for mpi -  https://github.com/kubeflow/training-operator/issues/1777
        install_kubeflow "$1" "$2"
    fi
    if [[ -n ${KUBEFLOW_MPI_VERSION:-} ]]; then
        install_mpi "$1" "$2"
    fi
    if [[ -n ${LEADERWORKERSET_VERSION:-} ]]; then
        install_lws "$1" "$2"
    fi
    if [[ -n ${KUBERAY_VERSION:-} ]]; then
        install_kuberay "$1" "$2"
    fi
    if [[ -n ${CERTMANAGER_VERSION:-} ]]; then
        install_cert_manager "$2"
    fi
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
        echo "kind load docker-image '$2' --name '$1' --nodes '$worker_nodes'"
        $KIND load docker-image "$2" --name "$1" --nodes "$worker_nodes"
    fi
}

# $1 kubeconfig
function deploy_with_certmanager() {
    local crd_kust="${ROOT_DIR}/config/components/crd/kustomization.yaml"
    local default_kust="${ROOT_DIR}/config/default/kustomization.yaml"
    local crd_backup
    crd_backup="$(<"$crd_kust")"
    local default_backup
    default_backup="$(<"$default_kust")"

    (
        cd "${ROOT_DIR}/config/components/crd" || exit
        $KUSTOMIZE edit add patch --path "patches/cainjection_in_clusterqueues.yaml"
        $KUSTOMIZE edit add patch --path "patches/cainjection_in_cohorts.yaml"
        $KUSTOMIZE edit add patch --path "patches/cainjection_in_resourceflavors.yaml"
        $KUSTOMIZE edit add patch --path "patches/cainjection_in_workloads.yaml"
    )

    (
        cd "${ROOT_DIR}/config/default" || exit
        $KUSTOMIZE edit add patch --path "mutating_webhookcainjection_patch.yaml"
        $KUSTOMIZE edit add patch --path "validating_webhookcainjection_patch.yaml"
        $KUSTOMIZE edit add patch --path "cert_metrics_manager_patch.yaml" --kind Deployment
    )

    build_and_apply_kueue_manifests "$1" "${ROOT_DIR}/test/e2e/config/certmanager"

    printf "%s\n" "$crd_backup" > "$crd_kust"
    printf "%s\n" "$default_backup" > "$default_kust"
}

# $1 kubeconfig
function cluster_kueue_deploy {
    if [[ -n ${CERTMANAGER_VERSION:-} ]]; then
        kubectl -n cert-manager wait --for condition=ready pod \
            -l app.kubernetes.io/instance=cert-manager \
            --timeout=5m
        if [ "$E2E_USE_HELM" == 'true' ]; then
            helm_install "$1" "${ROOT_DIR}/test/e2e/config/certmanager/values.yaml"
        else
            deploy_with_certmanager "$1"
        fi
    elif [ "$E2E_USE_HELM" == 'true' ]; then
        helm_install "$1" "${ROOT_DIR}/test/e2e/config/default/values.yaml"
    else
        build_and_apply_kueue_manifests "$1" "${ROOT_DIR}/test/e2e/config/default"
    fi
}

# $1 kubeconfig
# $2 values file
function helm_install {
    $HELM install \
      -f "$2" \
      --set "controllerManager.manager.image.repository=${IMAGE_TAG%:*}" \
      --set "controllerManager.manager.image.tag=${IMAGE_TAG##*:}" \
      --create-namespace \
      --namespace kueue-system \
      --kubeconfig "$1" \
      kueue "${ROOT_DIR}/charts/kueue"
}

# $1 kubeconfig 
# $2 kustomization config
function build_and_apply_kueue_manifests {
    local build_output
    build_output=$($KUSTOMIZE build "$2")
    # shellcheck disable=SC2001 # bash parameter substitution does not work on macOS
    build_output=$(echo "$build_output" | sed "s/kueue-system/$KUEUE_NAMESPACE/g")
    echo "$build_output" | kubectl apply --kubeconfig="$1" --server-side -f -
}

# $1 cluster name
# $2 kubeconfig option
function install_appwrapper {
    cluster_kind_load_image "${1}" "${APPWRAPPER_IMAGE}"
    kubectl apply --kubeconfig="$2" --server-side -k "${APPWRAPPER_MANIFEST}"
}

# $1 cluster name
# $2 kubeconfig option
function install_jobset {
    cluster_kind_load_image "${1}" "${JOBSET_IMAGE}"
    kubectl apply --kubeconfig="$2" --server-side -f "${JOBSET_MANIFEST}"
}

# $1 cluster name
# $2 kubeconfig option
function install_kubeflow {
    cluster_kind_load_image "${1}" "${KUBEFLOW_IMAGE}"
    kubectl apply --kubeconfig="$2" --server-side -k "${KUBEFLOW_MANIFEST_PATCHED}"
}

# $1 cluster name
# $2 kubeconfig option
function install_mpi {
    cluster_kind_load_image "${1}" "${KUBEFLOW_MPI_IMAGE/#v}"
    kubectl apply --kubeconfig="$2" --server-side -f "${KUBEFLOW_MPI_MANIFEST}"
}

# $1 cluster name
# $2 kubeconfig option
function install_kuberay {
    cluster_kind_load_image "${1}" "${KUBERAY_RAY_IMAGE}"
    cluster_kind_load_image "${1}" "${KUBERAY_IMAGE}"
    # create used instead of apply - https://github.com/ray-project/kuberay/issues/504
    kubectl create --kubeconfig="$2" -k "${KUBERAY_MANIFEST}"
}

# $1 cluster name
# $2 kubeconfig option
function install_lws {
    cluster_kind_load_image "${1}" "${LEADERWORKERSET_IMAGE/#v}"
    kubectl apply --kubeconfig="$2" --server-side -f "${LEADERWORKERSET_MANIFEST}"
}

# $1 kubeconfig option
function install_cert_manager {
    kubectl apply --kubeconfig="$1" --server-side -f "${CERTMANAGER_MANIFEST}"
}

INITIAL_IMAGE=$($YQ '.images[] | select(.name == "controller") | [.newName, .newTag] | join(":")' config/components/manager/kustomization.yaml)
export INITIAL_IMAGE

function set_managers_image {
    (cd "${ROOT_DIR}/config/components/manager" && $KUSTOMIZE edit set image controller="$IMAGE_TAG")
}

function restore_managers_image {
    (cd "${ROOT_DIR}/config/components/manager" && $KUSTOMIZE edit set image controller="$INITIAL_IMAGE")
}

function kueue_deploy {
    # We use a subshell to avoid overwriting the global cleanup trap, which also uses the EXIT signal.
    (
        set_managers_image
        trap restore_managers_image EXIT
        cluster_kueue_deploy ""
    )
}

function determine_kuberay_ray_image {
    local RAY_IMAGE=rayproject/ray:${RAY_VERSION}
    local RAYMINI_IMAGE=us-central1-docker.pkg.dev/k8s-staging-images/kueue/ray-project-mini:${RAYMINI_VERSION}

    # Extra e2e images required for Kuberay
    local ray_image_to_use=""

    if [[ "${USE_RAY_FOR_TESTS:-}" == "ray" ]]; then
        ray_image_to_use="${RAY_IMAGE}"
    else
        ray_image_to_use="${RAYMINI_IMAGE}"
    fi

    if [[ -z "$ray_image_to_use" ]]; then
        echo "Error: Unable to determine the ray image to load." >&2
        return 1
    fi

    export KUBERAY_RAY_IMAGE="${ray_image_to_use}"
}

# $1 cluster name
# $2 kubeconfig file path
function prepare_kubeconfig {
    local kind_name=$1
    local kubeconfig=$2
    if [[ "$kubeconfig" != "" ]]; then
        cat <<EOF > "$kubeconfig"
        apiVersion: v1
        kind: Config
        preferences: {}
EOF
        kubectl config --kubeconfig="$kubeconfig" set-context "kind-$kind_name" \
        --cluster="$kind_name" \
        --user="$kind_name"
    fi
}
