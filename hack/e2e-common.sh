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

# E2E_MODE controls e2e cluster lifecycle behavior:
# - ci  (default): create cluster(s), run tests, delete cluster(s)
# - dev           : create if missing / reuse if existing, run tests, keep cluster(s)
export E2E_MODE="${E2E_MODE:-ci}"
if [[ "${E2E_MODE}" != "ci" && "${E2E_MODE}" != "dev" ]]; then
    echo "Invalid E2E_MODE='${E2E_MODE}'. Supported values: ci|dev" >&2
    exit 2
fi

export KIND_VERSION="${E2E_KIND_VERSION/"kindest/node:v"/}"

if [[ -n ${APPWRAPPER_VERSION:-} && ("$GINKGO_ARGS" =~ feature:appwrapper || ! "$GINKGO_ARGS" =~ "--label-filter") ]]; then
    export APPWRAPPER_MANIFEST=${ROOT_DIR}/dep-crds/appwrapper/config/default
    export APPWRAPPER_IMAGE=quay.io/ibm/appwrapper:${APPWRAPPER_VERSION}
fi

if [[ -n ${JOBSET_VERSION:-} && ("$GINKGO_ARGS" =~ feature:(jobset|tas|trainjob) || ! "$GINKGO_ARGS" =~ "--label-filter") ]]; then
    export JOBSET_MANIFEST="https://github.com/kubernetes-sigs/jobset/releases/download/${JOBSET_VERSION}/manifests.yaml"
    export JOBSET_IMAGE=registry.k8s.io/jobset/jobset:${JOBSET_VERSION}
    export JOBSET_CRDS=${ROOT_DIR}/dep-crds/jobset-operator/
fi

if [[ -n ${KUBEFLOW_VERSION:-} && ("$GINKGO_ARGS" =~ feature:(jaxjob|pytorchjob) || ! "$GINKGO_ARGS" =~ "--label-filter") ]]; then
    export KUBEFLOW_MANIFEST_ORIG=${ROOT_DIR}/dep-crds/training-operator/manifests/overlays/standalone/kustomization.yaml
    export KUBEFLOW_MANIFEST_PATCHED=${ROOT_DIR}/test/e2e/config/multikueue
    # Extract the Kubeflow Training Operator image version tag (newTag) from the manifest.
    # This is necessary because the image version tag does not follow the usual package versioning convention.
    KUBEFLOW_IMAGE_VERSION=$($YQ '.images[] | select(.name | contains("training-operator")) | .newTag' "${KUBEFLOW_MANIFEST_ORIG}")
    export KUBEFLOW_IMAGE_VERSION
    export KUBEFLOW_IMAGE=kubeflow/training-operator:${KUBEFLOW_IMAGE_VERSION}
fi

if [[ -n ${KUBEFLOW_TRAINER_VERSION:-} && ("$GINKGO_ARGS" =~ feature:(tas|trainjob) || ! "$GINKGO_ARGS" =~ "--label-filter") ]]; then
    export KUBEFLOW_TRAINER_MANIFEST=${ROOT_DIR}/dep-crds/kf-trainer/manifests
    # Extract the Kubeflow Trainer controller manager image version tag (newTag) from the manifest.
    # This is necessary because the image version tag does not follow the usual package versioning convention.
    KF_TRAINER_IMAGE_VERSION=$($YQ '.images[] | select(.name | contains("trainer-controller-manager")) | .newTag' "${KUBEFLOW_TRAINER_MANIFEST}/overlays/manager/kustomization.yaml")
    export KF_TRAINER_IMAGE_VERSION
    export KF_TRAINER_IMAGE=ghcr.io/kubeflow/trainer/trainer-controller-manager:${KF_TRAINER_IMAGE_VERSION}
fi

if [[ -n ${KUBEFLOW_MPI_VERSION:-} ]]; then
    export KUBEFLOW_MPI_MANIFEST="https://raw.githubusercontent.com/kubeflow/mpi-operator/${KUBEFLOW_MPI_VERSION}/deploy/v2beta1/mpi-operator.yaml"
    export KUBEFLOW_MPI_IMAGE=mpioperator/mpi-operator:${KUBEFLOW_MPI_VERSION/#v}
fi

if [[ -n ${KUBERAY_VERSION:-} && ("$GINKGO_ARGS" =~ feature:kuberay || ! "$GINKGO_ARGS" =~ "--label-filter") ]]; then
    export KUBERAY_MANIFEST="${ROOT_DIR}/dep-crds/ray-operator/default/"
    export KUBERAY_IMAGE=quay.io/kuberay/operator:${KUBERAY_VERSION}
fi

if [[ -n ${LEADERWORKERSET_VERSION:-} && ("$GINKGO_ARGS" =~ feature:leaderworkerset || ! "$GINKGO_ARGS" =~ "--label-filter") ]]; then
    export LEADERWORKERSET_MANIFEST="https://github.com/kubernetes-sigs/lws/releases/download/${LEADERWORKERSET_VERSION}/manifests.yaml"
    export LEADERWORKERSET_IMAGE=registry.k8s.io/lws/lws:${LEADERWORKERSET_VERSION}
fi

if [[ -n "${CERTMANAGER_VERSION:-}" ]]; then
    export CERTMANAGER_MANIFEST="https://github.com/cert-manager/cert-manager/releases/download/${CERTMANAGER_VERSION}/cert-manager.yaml"
fi

if [[ -n "${CLUSTERPROFILE_VERSION:-}" ]]; then
    export CLUSTERPROFILE_CRD=${ROOT_DIR}/dep-crds/clusterprofile/multicluster.x-k8s.io_clusterprofiles.yaml
    export CLUSTERPROFILE_PLUGIN_IMAGE=us-central1-docker.pkg.dev/k8s-staging-images/kueue/secretreader-plugin:${CLUSTERPROFILE_PLUGIN_IMAGE_VERSION}
fi

if [[ -n "${DRA_EXAMPLE_DRIVER_VERSION:-}" ]]; then
    export DRA_EXAMPLE_DRIVER_REPO=https://github.com/kubernetes-sigs/dra-example-driver.git
fi

if [[ -n "${KUEUE_UPGRADE_FROM_VERSION:-}" ]]; then
    export KUEUE_OLD_VERSION_MANIFEST="https://github.com/kubernetes-sigs/kueue/releases/download/${KUEUE_UPGRADE_FROM_VERSION}/manifests.yaml"
fi

# agnhost image to use for testing.
E2E_TEST_AGNHOST_IMAGE_OLD_WITH_SHA=registry.k8s.io/e2e-test-images/agnhost:2.52@sha256:b173c7d0ffe3d805d49f4dfe48375169b7b8d2e1feb81783efd61eb9d08042e6
export E2E_TEST_AGNHOST_IMAGE_OLD=${E2E_TEST_AGNHOST_IMAGE_OLD_WITH_SHA%%@*}
E2E_TEST_AGNHOST_IMAGE_WITH_SHA=$(grep '^FROM' "${SOURCE_DIR}/agnhost/Dockerfile" | awk '{print $2}')
export E2E_TEST_AGNHOST_IMAGE=${E2E_TEST_AGNHOST_IMAGE_WITH_SHA%%@*}


# $1 cluster name
# $2 kubeconfig
function cluster_cleanup {
    local name=$1
    local kubeconfig=$2

    kubectl config --kubeconfig="$kubeconfig" use-context "kind-${name}"
    $KIND delete cluster --name "$name"
}

# $1 cluster name
# $2 kubeconfig
function cluster_collect_artifacts {
    local name=$1
    local kubeconfig=${2:-}

    local kubeconfig_args=()
    if [[ -n "${kubeconfig}" ]]; then
        kubeconfig_args=(--kubeconfig="${kubeconfig}")
    fi

    kubectl config "${kubeconfig_args[@]}" use-context "kind-${name}" || true
    $KIND export logs "$ARTIFACTS" --name "$name" || true
    kubectl describe pods "${kubeconfig_args[@]}" -n kueue-system > "$ARTIFACTS/${name}-kueue-system-pods.log" || true
    kubectl describe pods "${kubeconfig_args[@]}" > "$ARTIFACTS/${name}-default-pods.log" || true
}

# $1 cluster name
function kind_cluster_exists {
    $KIND get clusters 2>/dev/null | grep -Fxq "$1"
}

# $1 cluster name
# $2 kubeconfig file path (optional)
function kind_write_kubeconfig {
    if [[ -n "${2:-}" ]]; then
        mkdir -p "$(dirname "$2")"
        $KIND get kubeconfig --name "$1" > "$2"
    fi
}

# $1 cluster name
# $2 cluster kind config
# $3 kubeconfig file path (optional)
function ensure_kind_cluster {
    local name=$1
    local cfg=$2
    local kubeconfig=$3

    if [[ "${E2E_MODE}" == "dev" ]]; then
        if kind_cluster_exists "$name"; then
            echo "Reusing kind cluster: $name (E2E_MODE=dev)"
            kind_write_kubeconfig "$name" "$kubeconfig"
            return 0
        fi

        echo "Creating kind cluster (E2E_MODE=dev): $name"
        cluster_create "$name" "$cfg" "$kubeconfig"
        return 0
    fi

    # CI mode: always start from a clean cluster.
    if kind_cluster_exists "$name"; then
        echo "Deleting existing kind cluster for a clean CI run: $name"
        cluster_cleanup "$name" "$kubeconfig"
    fi
    cluster_create "$name" "$cfg" "$kubeconfig"
}

# $1 cluster name
function e2e_should_delete_cluster {
    [[ "${E2E_MODE}" != "dev" ]]
}

# Patches a kind config file with DRA-specific settings.
function patch_kind_config_for_dra {
    local patched_config
    patched_config=$(mktemp)
    cp "$1" "$patched_config"

    $YQ -i '.featureGates.DynamicResourceAllocation = true' "$patched_config"
    $YQ -i '.containerdConfigPatches += ["[plugins.\"io.containerd.grpc.v1.cri\"]\n  enable_cdi = true"]' "$patched_config"
    $YQ -i '(.nodes[] | select(.role == "control-plane")).kubeadmConfigPatchesJSON6902 += [{
      group: "kubeadm.k8s.io",
      version: "v1beta4",
      kind: "ClusterConfiguration",
      patch: load_str("<<-EOF")
    	- op: add
    	  path: /apiServer/extraArgs
    	  value:
    	  - name: enable-aggregator-routing
    	    value: "true"
    	  - name: runtime-config
    	    value: "resource.k8s.io/v1=true"
    	  - name: v
    	    value: "3"
    	- op: add
    	  path: /scheduler/extraArgs
    	  value:
    	  - name: v
    	    value: "3"
    	- op: add
    	  path: /controllerManager/extraArgs
    	  value:
    	  - name: v
    	    value: "3"
    	EOF
}]' "$patched_config"

    echo "$patched_config"
}

# $1 cluster name
# $2 cluster kind config
# $3 kubeconfig
function cluster_create {
    prepare_kubeconfig "$1" "$3"

    local kind_config="$2"

    if [[ -n ${DRA_EXAMPLE_DRIVER_VERSION:-} ]]; then
        kind_config=$(patch_kind_config_for_dra "$2")
        # shellcheck disable=SC2064 # Intentionally expand now to capture the temp file path
        trap "rm -f '$kind_config'" RETURN
        echo "Using patched kind config for DRA:"
        cat "$kind_config"
    fi

    $KIND create cluster --name "$1" --image "$E2E_KIND_VERSION" --config "$kind_config" --kubeconfig="$3" --wait 1m -v 5  > "$ARTIFACTS/$1-create.log" 2>&1 \
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

    if [[ -n ${APPWRAPPER_VERSION:-} && ("$GINKGO_ARGS" =~ feature:appwrapper || ! "$GINKGO_ARGS" =~ "--label-filter") ]]; then
        docker pull "${APPWRAPPER_IMAGE}"
    fi
    if [[ -n ${JOBSET_VERSION:-} && ("$GINKGO_ARGS" =~ feature:(jobset|tas|trainjob) || ! "$GINKGO_ARGS" =~ "--label-filter") ]]; then
        docker pull "${JOBSET_IMAGE}"
    fi
    if [[ -n ${KUBEFLOW_VERSION:-} && ("$GINKGO_ARGS" =~ feature:(jaxjob|pytorchjob) || ! "$GINKGO_ARGS" =~ "--label-filter") ]]; then
        docker pull "${KUBEFLOW_IMAGE}"
    fi
    if [[ -n ${KUBEFLOW_TRAINER_VERSION:-} && ("$GINKGO_ARGS" =~ feature:(tas|trainjob) || ! "$GINKGO_ARGS" =~ "--label-filter") ]]; then
        docker pull "${KF_TRAINER_IMAGE}"
    fi

    if [[ -n ${KUBEFLOW_MPI_VERSION:-} ]]; then
        docker pull "${KUBEFLOW_MPI_IMAGE}"
    fi
    if [[ -n ${KUBERAY_VERSION:-} && ("$GINKGO_ARGS" =~ feature:kuberay || ! "$GINKGO_ARGS" =~ "--label-filter") ]]; then
        docker pull "${KUBERAY_IMAGE}"
        determine_kuberay_ray_image
        if [[ ${USE_RAY_FOR_TESTS:-} == "ray" ]]; then
            docker pull "${KUBERAY_RAY_IMAGE}"
        fi
    fi
    if [[ -n ${LEADERWORKERSET_VERSION:-} && ("$GINKGO_ARGS" =~ feature:leaderworkerset || ! "$GINKGO_ARGS" =~ "--label-filter") ]]; then
        docker pull "${LEADERWORKERSET_IMAGE}"
    fi
    if [[ -n ${KUEUE_UPGRADE_FROM_VERSION:-} ]]; then
        local current_image="${IMAGE_TAG%:*}:${KUEUE_UPGRADE_FROM_VERSION}"
        docker pull "${current_image}"
    fi
}

# $1 cluster
function cluster_kind_load {
    cluster_kind_load_image "$1" "${E2E_TEST_AGNHOST_IMAGE_OLD}"
    cluster_kind_load_image "$1" "${E2E_TEST_AGNHOST_IMAGE}"
    cluster_kind_load_image "$1" "$IMAGE_TAG"
    if [[ -n ${KUEUE_UPGRADE_FROM_VERSION:-} ]]; then
        local old_image="${IMAGE_TAG%:*}:${KUEUE_UPGRADE_FROM_VERSION}"
        cluster_kind_load_image "$1" "${old_image}"
    fi
    if [[ -n "${CLUSTERPROFILE_VERSION:-}" ]]; then
        cluster_kind_load_image "$1" "${CLUSTERPROFILE_PLUGIN_IMAGE}"
    fi
}

# $1 cluster
# $2 kubeconfig
function kind_load {
    kubectl config --kubeconfig="$2" use-context "kind-$1"

    if kind_cluster_exists "$1"; then
        cluster_kind_load "$1"
    fi
    if [[ -n ${APPWRAPPER_VERSION:-} && ("$GINKGO_ARGS" =~ feature:appwrapper || ! "$GINKGO_ARGS" =~ "--label-filter") ]]; then
        install_appwrapper "$1" "$2"
    fi
    if [[ -n ${JOBSET_VERSION:-} && ("$GINKGO_ARGS" =~ feature:(jobset|tas|trainjob) || ! "$GINKGO_ARGS" =~ "--label-filter") ]]; then
        install_jobset "$1" "$2"
    fi
    if [[ -n ${KUBEFLOW_VERSION:-} && ("$GINKGO_ARGS" =~ feature:(jaxjob|pytorchjob) || ! "$GINKGO_ARGS" =~ "--label-filter") ]]; then
        # In order for MPI-operator and Training-operator to work on the same cluster it is required that:
        # 1. 'kubeflow.org_mpijobs.yaml' is removed from base/crds/kustomization.yaml - https://github.com/kubeflow/training-operator/issues/1930
        # 2. Training-operator deployment is modified to enable all kubeflow jobs except for mpi -  https://github.com/kubeflow/training-operator/issues/1777
        install_kubeflow "$1" "$2"
    fi

    if [[ -n ${KUBEFLOW_TRAINER_VERSION:-} && ("$GINKGO_ARGS" =~ feature:(tas|trainjob) || ! "$GINKGO_ARGS" =~ "--label-filter") ]]; then
        install_kubeflow_trainer "$1" "$2"
    fi

    if [[ -n ${KUBEFLOW_MPI_VERSION:-} ]]; then
        install_mpi "$1" "$2"
    fi
    if [[ -n ${LEADERWORKERSET_VERSION:-} && ("$GINKGO_ARGS" =~ feature:leaderworkerset || ! "$GINKGO_ARGS" =~ "--label-filter") ]]; then
        install_lws "$1" "$2"
    fi
    if [[ -n ${KUBERAY_VERSION:-} && ("$GINKGO_ARGS" =~ feature:kuberay || ! "$GINKGO_ARGS" =~ "--label-filter") ]]; then
        install_kuberay "$1" "$2"
    fi
    if [[ -n ${CERTMANAGER_VERSION:-} ]]; then
        install_cert_manager "$2"
    fi
    if [[ -n ${CLUSTERPROFILE_VERSION:-} ]]; then
        install_multicluster "$2"
    fi
    if [[ -n ${DRA_EXAMPLE_DRIVER_VERSION:-} ]]; then
        install_dra_example_driver "$1" "$2"
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
    worker_nodes=$($KIND get nodes --name "$1" | grep -v 'control-plane')
    if [[ -n "$worker_nodes" ]]; then
        # Use docker save + ctr import directly to avoid the --all-platforms
        # issue with multi-arch images in DinD environments.
        # See: https://github.com/kubernetes-sigs/kind/issues/3795
        echo "Loading image '$2' to cluster '$1'"
        while IFS= read -r node; do
            echo "  Loading image to node: $node"
            if ! docker save "$2" | docker exec -i "$node" ctr --namespace=k8s.io images import --digests --snapshotter=overlayfs -; then
                echo "Failed to load image '$2' to node '$node'"
                return 1
            fi
        done <<< "$worker_nodes"
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
        $KUSTOMIZE edit add patch --path "patches/cainjection_in_localqueues.yaml"
        $KUSTOMIZE edit add patch --path "patches/cainjection_in_resourceflavors.yaml"
        $KUSTOMIZE edit add patch --path "patches/cainjection_in_workloads.yaml"
    )

    (
        cd "${ROOT_DIR}/config/default" || exit
        $KUSTOMIZE edit add patch --path "mutating_webhookcainjection_patch.yaml"
        $KUSTOMIZE edit add patch --path "validating_webhookcainjection_patch.yaml"
        $KUSTOMIZE edit add patch --path "cert_metrics_manager_patch.yaml" --kind Deployment
        $KUSTOMIZE edit add patch --path "cert_visibility_manager_patch.yaml" --kind Deployment
        $KUSTOMIZE edit add patch --path "apiservice_cainjection_patch.yaml" --kind APIService
        $KUSTOMIZE edit add patch --path "apiservice_insecure_removal.yaml" --kind APIService
    )

    build_and_apply_kueue_manifests "$1" "${ROOT_DIR}/test/e2e/config/certmanager"

    printf "%s\n" "$crd_backup" > "$crd_kust"
    printf "%s\n" "$default_backup" > "$default_kust"
}

# $1 kubeconfig
function cluster_kueue_deploy {
    # Handle upgrade test mode
    if [[ -n ${KUEUE_UPGRADE_FROM_VERSION:-} ]]; then
        upgrade_test_flow "$1"
        return
    fi
    # Normal deployment flows
    if [[ -n ${CERTMANAGER_VERSION:-} ]]; then
        kubectl -n cert-manager wait --for condition=ready pod \
            -l app.kubernetes.io/instance=cert-manager \
            --timeout=5m
        if [ "$E2E_USE_HELM" == 'true' ]; then
            helm_install "$1" "${ROOT_DIR}/test/e2e/config/certmanager/values.yaml"
        else
            deploy_with_certmanager "$1"
        fi
    elif [[ -n ${DRA_EXAMPLE_DRIVER_VERSION:-} ]]; then
        build_and_apply_kueue_manifests "$1" "${ROOT_DIR}/test/e2e/config/dra"
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
function install_kubeflow_trainer {
    cluster_kind_load_image "${1}" "${KF_TRAINER_IMAGE}"
    (
        # Kustomize patches don't work on the Kustomization file itself, they work on the Kubernetes resources that the Kustomization generates.
        # So in case the jobset controller was already installed, we need to remove the resource from the kustomization file to avoid controller duplications
        # We do it always since is cheap and more readable than dealing with conditionals
        manifests_temp_dir=$(mktemp -d) && trap 'rm -rf "$manifests_temp_dir"' EXIT
        cp -r "${KUBEFLOW_TRAINER_MANIFEST}"/* "$manifests_temp_dir/" && chmod -R 777 "$manifests_temp_dir"
        if [[ -n ${JOBSET_VERSION:-} ]]; then
            $YQ eval 'del(.resources[] | select(. == "../../third-party/jobset"))' -i "$manifests_temp_dir/overlays/manager/kustomization.yaml"
        fi
        kubectl apply --kubeconfig="$2" --server-side -k "$manifests_temp_dir/overlays/manager"
    )
    # In order to install the training runtimes we need to wait for the ClusterTrainingRuntime webhook to be ready
    kubectl wait --kubeconfig="$2" deploy/kubeflow-trainer-controller-manager -n kubeflow-system --for=condition=available --timeout=5m
    kubectl apply --kubeconfig="$2" --server-side -k "${KUBEFLOW_TRAINER_MANIFEST}/overlays/runtimes"
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

# $1 kubeconfig option
function install_multicluster {
    kubectl apply --kubeconfig="$1" --server-side -f "${CLUSTERPROFILE_CRD}"
}

# $1 cluster name
# $2 kubeconfig option
function install_dra_example_driver {
    local dra_driver_temp_dir
    dra_driver_temp_dir=$(mktemp -d)
    # shellcheck disable=SC2064 # Intentionally expand now to capture the temp dir path
    trap "rm -rf '$dra_driver_temp_dir'" RETURN
    git clone --depth 1 --branch "${DRA_EXAMPLE_DRIVER_VERSION}" "${DRA_EXAMPLE_DRIVER_REPO}" "$dra_driver_temp_dir"

    local dra_image_repo="dra-example-driver"
    local dra_image_tag="local"

    local go_version
    go_version=$(grep '^go ' "$dra_driver_temp_dir/go.mod" | awk '{print $2}' | cut -d. -f1,2)

    echo "Building dra-example-driver image from source (Go ${go_version})..."
    # Patch Makefile to ensure static build with CGO_ENABLED=0
    sed 's/CGO_LDFLAGS_ALLOW/CGO_ENABLED=0 CGO_LDFLAGS_ALLOW/' "$dra_driver_temp_dir/Makefile" > "$dra_driver_temp_dir/Makefile.tmp" \
        && mv "$dra_driver_temp_dir/Makefile.tmp" "$dra_driver_temp_dir/Makefile"
    docker build -t "${dra_image_repo}:${dra_image_tag}" \
        --build-arg GOLANG_VERSION="${go_version}" \
        --build-arg BASE_IMAGE=gcr.io/distroless/static:latest \
        -f "$dra_driver_temp_dir/deployments/container/Dockerfile" \
        "$dra_driver_temp_dir"

    cluster_kind_load_image "${1}" "${dra_image_repo}:${dra_image_tag}"

    $HELM upgrade -i \
      --create-namespace \
      --namespace dra-example-driver \
      --kubeconfig="$2" \
      --set image.repository="${dra_image_repo}" \
      --set image.tag="${dra_image_tag}" \
      --set kubeletPlugin.containers.plugin.securityContext.privileged=true \
      --wait \
      dra-example-driver \
      "$dra_driver_temp_dir/deployments/helm/dra-example-driver"
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

# Upgrade test flow: install old version, create resources, upgrade to current
# $1 kubeconfig
function upgrade_test_flow {
    local old_version="${KUEUE_UPGRADE_FROM_VERSION}"
    local old_image="${IMAGE_TAG%:*}:${old_version}"

    echo "Upgrade Test: $old_version -> current"
    echo "Old image: $old_image"
    echo "New image: $IMAGE_TAG"
    
    # Step 1: Install old version
    echo "Installing $old_version..."
    echo "  Manifest URL: ${KUEUE_OLD_VERSION_MANIFEST}"
    echo "  Downloading and modifying manifests..."
    
    # Download and modify manifests inline
    curl -sL "${KUEUE_OLD_VERSION_MANIFEST}" | \
      sed "s|registry.k8s.io/kueue/kueue:${old_version}|${old_image}|g" | \
      sed 's|imagePullPolicy: Always|imagePullPolicy: IfNotPresent|g' | \
      kubectl apply --server-side -f -

    kubectl wait --for=condition=available --timeout=180s deployment/kueue-controller-manager -n kueue-system
    echo "✓ $old_version ready"
    
    # Step 2: Create test resources
    echo "Creating test resources..."
    
    # Create custom namespace for test resources (idempotent)
    kubectl apply --kubeconfig="$1" -f - <<EOF_NS
apiVersion: v1
kind: Namespace
metadata:
  name: kueue-upgrade-test
EOF_NS
    
    # Apply test resources
    kubectl apply --kubeconfig="$1" -f - <<EOF
apiVersion: kueue.x-k8s.io/v1beta1
kind: ResourceFlavor
metadata:
  name: upgrade-test-flavor
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: upgrade-test-cq
spec:
  namespaceSelector: {}
  resourceGroups:
  - coveredResources: ["cpu", "memory"]
    flavors:
    - name: upgrade-test-flavor
      resources:
      - name: "cpu"
        nominalQuota: 10
      - name: "memory"
        nominalQuota: 10Gi
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: LocalQueue
metadata:
  name: upgrade-test-lq
  namespace: kueue-upgrade-test
spec:
  clusterQueue: upgrade-test-cq
EOF
    echo "✓ Resources created"
    
    # Step 3: Upgrade to current (rolling update)
    echo "Upgrading to current..."
    
    # Apply upgrade - rolling update will replace pods
    (
        set_managers_image
        trap restore_managers_image EXIT
        
        local build_output
        build_output=$($KUSTOMIZE build "${ROOT_DIR}/test/e2e/config/default")
        # shellcheck disable=SC2001 # bash parameter substitution does not work on macOS
        build_output=$(echo "$build_output" | sed "s/kueue-system/$KUEUE_NAMESPACE/g")
        echo "$build_output" | kubectl apply --kubeconfig="$1" --server-side --force-conflicts -f -
    )
    
    # Wait for the rolling update to complete.
    echo "Waiting for rolling update to complete..."
    kubectl wait --for=condition=available --timeout=300s deployment/kueue-controller-manager -n "${KUEUE_NAMESPACE}"
    echo "Upgrade complete (rolling update finished)"
    echo "========================================="
}
