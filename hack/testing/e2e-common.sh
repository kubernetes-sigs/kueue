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

# When set (truthy), external operators are forcefully re-installed_version even in E2E_MODE=dev.
export E2E_ENFORCE_OPERATOR_UPDATE="${E2E_ENFORCE_OPERATOR_UPDATE:-false}"

export KIND_VERSION="${E2E_KIND_VERSION/"kindest/node:v"/}"

function e2e_is_truthy {
    case "${1:-}" in
        1|true|TRUE|True|yes|YES|Yes|y|Y|on|ON|On) return 1 ;;
        *) return 0 ;;
    esac
}

function e2e_deployment_exists {
    local kubeconfig=${1:-}
    local ns=$2
    local deployment_name=$3

    local -a kubectl_args=()
    if [[ -n "${kubeconfig}" ]]; then
        kubectl_args+=(--kubeconfig="${kubeconfig}")
    fi

    kubectl ${kubectl_args[@]+"${kubectl_args[@]}"} -n "${ns}" get deployment "${deployment_name}" >/dev/null 2>&1
}

function e2e_image_ref_get_tag {
    local img=${1:-}
    img="${img%%@*}"
    if [[ "${img}" == *":"* ]]; then
        echo "${img##*:}"
        return 0
    fi
    return 1
}

function e2e_versions_match {
    local a=${1:-}
    local b=${2:-}

    [[ -n "${a}" && -n "${b}" ]] || return 1
    [[ "${a}" == "${b}" ]] && return 0
    [[ "${a#v}" == "${b#v}" ]] && return 0
    return 1
}

function e2e_crd_exists {
    local kubeconfig=${1:-}
    local crd=$2
    local -a kubectl_args=()
    if [[ -n "${kubeconfig}" ]]; then
        kubectl_args+=(--kubeconfig="${kubeconfig}")
    fi
    kubectl ${kubectl_args[@]+"${kubectl_args[@]}"} get crd "${crd}" >/dev/null 2>&1
}

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

if [[ -n "${PROMETHEUS_OPERATOR_VERSION:-}" ]]; then
    export PROMETHEUS_OPERATOR_BUNDLE="https://github.com/prometheus-operator/prometheus-operator/releases/download/${PROMETHEUS_OPERATOR_VERSION}/bundle.yaml"
fi

if [[ -n "${DRA_EXAMPLE_DRIVER_VERSION:-}" ]]; then
    export DRA_EXAMPLE_DRIVER_REPO=https://github.com/kubernetes-sigs/dra-example-driver.git
fi

if [[ -n "${KUEUE_UPGRADE_FROM_VERSION:-}" ]]; then
    export KUEUE_OLD_VERSION_MANIFEST="https://github.com/kubernetes-sigs/kueue/releases/download/${KUEUE_UPGRADE_FROM_VERSION}/manifests.yaml"
    # Use the released image from registry.k8s.io (not the staging registry)
    # so upgrade tests don't break when staging images expire.
    export KUEUE_OLD_VERSION_IMAGE="registry.k8s.io/kueue/kueue:${KUEUE_UPGRADE_FROM_VERSION}"
fi

# agnhost image to use for testing.
E2E_TEST_AGNHOST_IMAGE_OLD_WITH_SHA=registry.k8s.io/e2e-test-images/agnhost:2.52@sha256:b173c7d0ffe3d805d49f4dfe48375169b7b8d2e1feb81783efd61eb9d08042e6
export E2E_TEST_AGNHOST_IMAGE_OLD=${E2E_TEST_AGNHOST_IMAGE_OLD_WITH_SHA%%@*}
E2E_TEST_AGNHOST_IMAGE_WITH_SHA=$(grep '^FROM' "${SOURCE_DIR}/agnhost/Dockerfile" | awk '{print $2}')
export E2E_TEST_AGNHOST_IMAGE=${E2E_TEST_AGNHOST_IMAGE_WITH_SHA%%@*}


# $1 cluster name
function cluster_cleanup {
    local cluster_name="$1"
    local max_retries=5
    local retry_delay=1

    for attempt in $(seq 1 "$max_retries"); do
        if $KIND delete cluster --name "$cluster_name"; then
            return 0
        fi

        if [ "$attempt" -eq "$max_retries" ]; then
            break
        fi

        echo "WARNING: kind delete cluster '$cluster_name' failed (attempt $attempt/$max_retries)."

        # Retry kind deletion to work around Docker race conditions where
        # delete can fail before receiving the container exit event.
        # Upstream bugs:
        # - https://github.com/moby/moby/issues/51028
        # - https://github.com/moby/moby/issues/51845
        # This workaround can be revisited after upstream fixes are available.

        echo "Retrying in ${retry_delay}s..."
        sleep "$retry_delay"
        retry_delay=$((retry_delay * 2))
    done

    echo "ERROR: Failed to delete kind cluster '$cluster_name' after $max_retries attempts."
    return 1
}

# $1 cluster name
# $2 kubeconfig
function cluster_collect_artifacts {
    local name=$1
    local kubeconfig=${2:-}

    local -a kubectl_args=()
    if [[ -n "${kubeconfig}" ]]; then
        kubectl_args=(--kubeconfig="${kubeconfig}")
    fi

    kubectl config ${kubectl_args[@]+"${kubectl_args[@]}"} use-context "kind-${name}" || true
    $KIND export logs "$ARTIFACTS" --name "$name" || true
    kubectl describe pods ${kubectl_args[@]+"${kubectl_args[@]}"} -n kueue-system > "$ARTIFACTS/${name}-kueue-system-pods.log" || true
    kubectl describe pods ${kubectl_args[@]+"${kubectl_args[@]}"} > "$ARTIFACTS/${name}-default-pods.log" || true
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
        cluster_cleanup "$name"
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
    $YQ -i '(.nodes[] | select(.role == "control-plane")).kubeadmConfigPatches[0] = "kind: ClusterConfiguration
apiVersion: kubeadm.k8s.io/v1beta3
scheduler:
  extraArgs:
    v: \"3\"
controllerManager:
  extraArgs:
    v: \"3\"
apiServer:
  extraArgs:
    enable-aggregator-routing: \"true\"
    runtime-config: \"resource.k8s.io/v1=true\"
    v: \"3\"
"' "$patched_config"

    echo "$patched_config"
}

# Patches a kind config file with WAS (Workload Aware Scheduling) settings.
function patch_kind_config_for_was {
    local patched_config
    patched_config=$(mktemp)
    cp "$1" "$patched_config"

    $YQ -i '.featureGates.GenericWorkload = true' "$patched_config"
    $YQ -i '.featureGates.GangScheduling = true' "$patched_config"
    $YQ -i '(.nodes[] | select(.role == "control-plane")).kubeadmConfigPatches[0] = "kind: ClusterConfiguration
apiVersion: kubeadm.k8s.io/v1beta3
scheduler:
  extraArgs:
    v: \"3\"
controllerManager:
  extraArgs:
    v: \"3\"
apiServer:
  extraArgs:
    enable-aggregator-routing: \"true\"
    runtime-config: \"scheduling.k8s.io/v1alpha1=true\"
    v: \"3\"
"' "$patched_config"

    echo "$patched_config"
}

# $1 cluster name
# $2 cluster kind config
# $3 kubeconfig
function cluster_create {
    local cluster="$1"
    local kind_config="$2"
    local kubeconfig="$3"

    prepare_kubeconfig "$cluster" "$kubeconfig"

    if [[ -n ${DRA_EXAMPLE_DRIVER_VERSION:-} ]]; then
        kind_config=$(patch_kind_config_for_dra "$2")
        # shellcheck disable=SC2064 # Intentionally expand now to capture the temp file path
        trap "rm -f '$kind_config'" RETURN
        echo "Using patched kind config for DRA:"
        cat "$kind_config"
    fi

    if [[ -n ${WAS_ENABLED:-} ]]; then
        kind_config=$(patch_kind_config_for_was "$2")
        # shellcheck disable=SC2064 # Intentionally expand now to capture the temp file path
        trap "rm -f '$kind_config'" RETURN
        echo "Using patched kind config for WAS:"
        cat "$kind_config"
    fi

    $KIND create cluster --name "$cluster" --image "$E2E_KIND_VERSION" --config "$kind_config" --kubeconfig="$kubeconfig" --wait 1m -v 5  > "$ARTIFACTS/$cluster-create.log" 2>&1 \
    ||  { echo "unable to start the $cluster cluster "; cat "$ARTIFACTS/$cluster-create.log" ; }

    kubectl config --kubeconfig="$kubeconfig" use-context "kind-$cluster"
    # wait for nodes to become ready before loading images or deploying components
    kubectl wait --kubeconfig="$kubeconfig" --for=condition=Ready node --all --timeout=300s
    kubectl get nodes --kubeconfig="$kubeconfig" > "$ARTIFACTS/$cluster-nodes.log" || true
    kubectl describe pods --kubeconfig="$kubeconfig" -n kube-system > "$ARTIFACTS/$cluster-system-pods.log" || true
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
        docker pull "${KUEUE_OLD_VERSION_IMAGE}"
    fi
}

# $1 cluster
function cluster_kind_load {
    cluster_kind_load_image "$1" "${E2E_TEST_AGNHOST_IMAGE_OLD}"
    cluster_kind_load_image "$1" "${E2E_TEST_AGNHOST_IMAGE}"
    cluster_kind_load_image "$1" "$IMAGE_TAG"
    if [[ -n ${KUEUE_UPGRADE_FROM_VERSION:-} ]]; then
        cluster_kind_load_image "$1" "${KUEUE_OLD_VERSION_IMAGE}"
    fi
    if [[ -n "${CLUSTERPROFILE_VERSION:-}" ]]; then
        cluster_kind_load_image "$1" "${CLUSTERPROFILE_PLUGIN_IMAGE}"
    fi
}

# $1 cluster
# $2 kubeconfig
function kind_load {
    local e2e_cluster_name=$1
    local e2e_kubeconfig=$2

    cluster_kind_load "${e2e_cluster_name}"

    if [[ -n ${APPWRAPPER_VERSION:-} && ("$GINKGO_ARGS" =~ feature:appwrapper || ! "$GINKGO_ARGS" =~ "--label-filter") ]]; then
        install_appwrapper "${e2e_cluster_name}" "${e2e_kubeconfig}"
    fi
    if [[ -n ${JOBSET_VERSION:-} && ("$GINKGO_ARGS" =~ feature:(jobset|tas|trainjob) || ! "$GINKGO_ARGS" =~ "--label-filter") ]]; then
        install_jobset "${e2e_cluster_name}" "${e2e_kubeconfig}"
    fi
    if [[ -n ${KUBEFLOW_VERSION:-} && ("$GINKGO_ARGS" =~ feature:(jaxjob|pytorchjob) || ! "$GINKGO_ARGS" =~ "--label-filter") ]]; then
        # In order for MPI-operator and Training-operator to work on the same cluster it is required that:
        # 1. 'kubeflow.org_mpijobs.yaml' is removed from base/crds/kustomization.yaml - https://github.com/kubeflow/training-operator/issues/1930
        # 2. Training-operator deployment is modified to enable all kubeflow jobs except for mpi -  https://github.com/kubeflow/training-operator/issues/1777
        install_kubeflow "${e2e_cluster_name}" "${e2e_kubeconfig}"
    fi

    if [[ -n ${KUBEFLOW_TRAINER_VERSION:-} && ("$GINKGO_ARGS" =~ feature:(tas|trainjob) || ! "$GINKGO_ARGS" =~ "--label-filter") ]]; then
        install_kubeflow_trainer "${e2e_cluster_name}" "${e2e_kubeconfig}"
    fi

    if [[ -n ${KUBEFLOW_MPI_VERSION:-} ]]; then
        install_mpi "${e2e_cluster_name}" "${e2e_kubeconfig}"
    fi
    if [[ -n ${LEADERWORKERSET_VERSION:-} && ("$GINKGO_ARGS" =~ feature:leaderworkerset || ! "$GINKGO_ARGS" =~ "--label-filter") ]]; then
        install_lws "${e2e_cluster_name}" "${e2e_kubeconfig}"
    fi
    if [[ -n ${KUBERAY_VERSION:-} && ("$GINKGO_ARGS" =~ feature:kuberay || ! "$GINKGO_ARGS" =~ "--label-filter") ]]; then
        install_kuberay "${e2e_cluster_name}" "${e2e_kubeconfig}"
    fi
    if [[ -n ${CERTMANAGER_VERSION:-} ]]; then
        install_cert_manager "${e2e_kubeconfig}"
    fi
    if [[ -n ${PROMETHEUS_OPERATOR_VERSION:-} && ("$GINKGO_ARGS" =~ feature:prometheus || ! "$GINKGO_ARGS" =~ "--label-filter") ]]; then
        install_prometheus_operator "${e2e_kubeconfig}"
    fi
    if [[ -n ${CLUSTERPROFILE_VERSION:-} ]]; then
        install_multicluster "${e2e_kubeconfig}"
    fi
    if [[ -n ${DRA_EXAMPLE_DRIVER_VERSION:-} ]]; then
        install_dra_example_driver "${e2e_cluster_name}" "${e2e_kubeconfig}"
    fi
}

# Save image to a temp file once, then load into all worker nodes in parallel.
# Using docker save + ctr import directly to avoid the --all-platforms
# issue with multi-arch images in DinD environments.
# See: https://github.com/kubernetes-sigs/kind/issues/3795
# $1 cluster
# $2 image
function cluster_kind_load_image {
    # filter out 'control-plane' node, use only worker nodes to load image
    worker_nodes=$($KIND get nodes --name "$1" | grep -v 'control-plane')
    if [[ -z "$worker_nodes" ]]; then
        echo "No worker nodes found for cluster '$1'"
        return 1
    fi

    local tmp_dir
    tmp_dir=$(mktemp -d)
    trap '[ -d "${tmp_dir:-}" ] && rm -rf "$tmp_dir"' RETURN
    local tmp_image="$tmp_dir/image.tar"

    echo "Saving image '$2'..."
    if ! docker save "$2" -o "$tmp_image"; then
        echo "Failed to save image '$2'"
        return 1
    fi

    echo "Loading image '$2' to cluster '$1' (parallel)"
    local pids=()
    local nodes=()
    while IFS= read -r node; do
        echo "  Loading image to node: $node"
        docker exec -i "$node" ctr --namespace=k8s.io images import \
            --digests --snapshotter=overlayfs - < "$tmp_image" &
        pids+=($!)
        nodes+=("$node")
    done <<< "$worker_nodes"

    local failed=0
    for i in "${!pids[@]}"; do
        if ! wait "${pids[$i]}"; then
            echo "Failed to load image '$2' to node '${nodes[$i]}'"
            failed=1
        fi
    done
    return "$failed"
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

    # Use --force-conflicts when Kueue is already deployed to handle SSA conflicts when reapplying.
    local force_conflicts=""
    if kubectl get deployment kueue-controller-manager -n "${KUEUE_NAMESPACE}" --kubeconfig="$1" >/dev/null 2>&1; then
        echo "Kueue controller already exists, using --force-conflicts"
        force_conflicts="--force-conflicts"
    fi

    echo "$build_output" | kubectl apply --kubeconfig="$1" --server-side $force_conflicts -f -
}

# $1 cluster name
# $2 kubeconfig option
function install_appwrapper {
    local name=$1
    local kubeconfig=${2:-}
    local ns="${APPWRAPPER_DEPLOYMENT_NAMESPACE:-appwrapper-system}"
    local deployment_name="${APPWRAPPER_CONTROLLER_MANAGER_DEPLOYMENT:-appwrapper-controller-manager}"
    local expected_version="${APPWRAPPER_VERSION:-}"

    if [[ "${E2E_MODE}" == "dev" ]] && e2e_is_truthy "${E2E_ENFORCE_OPERATOR_UPDATE}"; then
        if e2e_deployment_exists "${kubeconfig}" "${ns}" "${deployment_name}"; then
            local installed_version=""
            local img=""
            img=$(kubectl --kubeconfig="${kubeconfig}" -n "${ns}" get deployment "${deployment_name}" \
                -o jsonpath='{.spec.template.spec.containers[?(@.name=="manager")].image}' 2>/dev/null || true)
            installed_version=$(e2e_image_ref_get_tag "${img}" || true)
            if [[ -n "${installed_version}" ]] && { [[ -z "${expected_version}" ]] || e2e_versions_match "${installed_version}" "${expected_version}"; }; then
                echo "AppWrapper already installed (${installed_version}); skipping install (E2E_MODE=dev)."
                return 0
            fi
            if [[ -n "${installed_version}" && -n "${expected_version}" ]]; then
                echo "AppWrapper installed version (${installed_version}) does not match requested (${expected_version}); upgrading."
            else
                echo "AppWrapper already present; upgrading."
            fi
        else
            echo "AppWrapper controller deployment not found; installing."
        fi
    fi

    cluster_kind_load_image "${name}" "${APPWRAPPER_IMAGE}"
    kubectl apply --kubeconfig="${kubeconfig}" --server-side -k "${APPWRAPPER_MANIFEST}"
    kubectl wait --kubeconfig="${kubeconfig}" deploy/"${deployment_name}" -n "${ns}" --for=condition=available --timeout=5m
}

# $1 cluster name
# $2 kubeconfig option
function install_jobset {
    local name=$1
    local kubeconfig=${2:-}
    local ns="${JOBSET_DEPLOYMENT_NAMESPACE:-jobset-system}"
    local deployment_name="${JOBSET_CONTROLLER_MANAGER_DEPLOYMENT:-jobset-controller-manager}"
    local expected_version="${JOBSET_VERSION:-}"

    if [[ "${E2E_MODE}" == "dev" ]] && e2e_is_truthy "${E2E_ENFORCE_OPERATOR_UPDATE}"; then
        if e2e_deployment_exists "${kubeconfig}" "${ns}" "${deployment_name}"; then
            local installed_version=""
            local images=""
            images=$(kubectl --kubeconfig="${kubeconfig}" -n "${ns}" get deployment "${deployment_name}" \
                -o jsonpath='{.spec.template.spec.containers[*].image}' 2>/dev/null || true)
            local img=""
            for img in ${images}; do
                if [[ "${img}" == *"jobset/jobset"* ]]; then
                    installed_version=$(e2e_image_ref_get_tag "${img}" || true)
                    break
                fi
            done
            if [[ -n "${installed_version}" ]] && { [[ -z "${expected_version}" ]] || e2e_versions_match "${installed_version}" "${expected_version}"; }; then
                echo "JobSet already installed (${installed_version}); skipping install (E2E_MODE=dev)."
                return 0
            fi
            if [[ -n "${installed_version}" && -n "${expected_version}" ]]; then
                echo "JobSet installed version (${installed_version}) does not match requested (${expected_version}); upgrading."
            else
                echo "JobSet already present; upgrading."
            fi
        else
            echo "JobSet controller deployment not found; installing."
        fi
    fi

    cluster_kind_load_image "${name}" "${JOBSET_IMAGE}"
    kubectl apply --kubeconfig="${kubeconfig}" --server-side -f "${JOBSET_MANIFEST}"
    kubectl wait --kubeconfig="${kubeconfig}" deploy/"${deployment_name}" -n "${ns}" --for=condition=available --timeout=5m
}

# $1 cluster name
# $2 kubeconfig option
function install_kubeflow {
    local name=$1
    local kubeconfig=${2:-}
    local ns="${KUBEFLOW_TRAINING_OPERATOR_NAMESPACE:-kubeflow}"
    local deployment_name="${KUBEFLOW_TRAINING_OPERATOR_DEPLOYMENT:-training-operator}"
    local expected_version="${KUBEFLOW_IMAGE_VERSION:-}"

    if [[ "${E2E_MODE}" == "dev" ]] && e2e_is_truthy "${E2E_ENFORCE_OPERATOR_UPDATE}"; then
        if e2e_deployment_exists "${kubeconfig}" "${ns}" "${deployment_name}"; then
            local installed_version=""
            local img=""
            img=$(kubectl --kubeconfig="${kubeconfig}" -n "${ns}" get deployment "${deployment_name}" \
                -o jsonpath='{.spec.template.spec.containers[?(@.name=="training-operator")].image}' 2>/dev/null || true)
            installed_version=$(e2e_image_ref_get_tag "${img}" || true)
            if [[ -n "${installed_version}" ]] && { [[ -z "${expected_version}" ]] || e2e_versions_match "${installed_version}" "${expected_version}"; }; then
                echo "Kubeflow Training Operator already installed (${installed_version}); skipping install (E2E_MODE=dev)."
                return 0
            fi
            if [[ -n "${installed_version}" && -n "${expected_version}" ]]; then
                echo "Kubeflow Training Operator installed version (${installed_version}) does not match requested (${expected_version}); upgrading."
            else
                echo "Kubeflow Training Operator already present; upgrading."
            fi
        else
            echo "Kubeflow Training Operator controller deployment not found; installing."
        fi
    fi

    cluster_kind_load_image "${name}" "${KUBEFLOW_IMAGE}"
    kubectl apply --kubeconfig="${kubeconfig}" --server-side -k "${KUBEFLOW_MANIFEST_PATCHED}"
    kubectl wait --kubeconfig="${kubeconfig}" deploy/"${deployment_name}" -n "${ns}" --for=condition=available --timeout=5m
}

# $1 cluster name
# $2 kubeconfig option
function install_kubeflow_trainer {
    local name=$1
    local kubeconfig=${2:-}
    local ns="${KUBEFLOW_TRAINER_NAMESPACE:-kubeflow-system}"
    local deployment_name="${KUBEFLOW_TRAINER_DEPLOYMENT:-kubeflow-trainer-controller-manager}"
    local expected_version="${KF_TRAINER_IMAGE_VERSION:-}"

    if [[ "${E2E_MODE}" == "dev" ]] && e2e_is_truthy "${E2E_ENFORCE_OPERATOR_UPDATE}"; then
        if e2e_deployment_exists "${kubeconfig}" "${ns}" "${deployment_name}"; then
            local installed_version=""
            local img=""
            img=$(kubectl --kubeconfig="${kubeconfig}" -n "${ns}" get deployment "${deployment_name}" \
                -o jsonpath='{.spec.template.spec.containers[?(@.name=="manager")].image}' 2>/dev/null || true)
            installed_version=$(e2e_image_ref_get_tag "${img}" || true)
            if [[ -n "${installed_version}" ]] && { [[ -z "${expected_version}" ]] || e2e_versions_match "${installed_version}" "${expected_version}"; }; then
                echo "Kubeflow Trainer already installed (${installed_version}); skipping install (E2E_MODE=dev)."
                return 0
            fi
            if [[ -n "${installed_version}" && -n "${expected_version}" ]]; then
                echo "Kubeflow Trainer installed version (${installed_version}) does not match requested (${expected_version}); upgrading."
            else
                echo "Kubeflow Trainer already present; upgrading."
            fi
        else
            echo "Kubeflow Trainer controller deployment not found; installing."
        fi
    fi

    cluster_kind_load_image "${name}" "${KF_TRAINER_IMAGE}"
    (
        # Kustomize patches don't work on the Kustomization file itself, they work on the Kubernetes resources that the Kustomization generates.
        # So in case the jobset controller was already installed, we need to remove the resource from the kustomization file to avoid controller duplications
        # We do it always since is cheap and more readable than dealing with conditionals
        manifests_temp_dir=$(mktemp -d) && trap 'rm -rf "$manifests_temp_dir"' EXIT
        cp -r "${KUBEFLOW_TRAINER_MANIFEST}"/* "$manifests_temp_dir/" && chmod -R 777 "$manifests_temp_dir"
        if [[ -n ${JOBSET_VERSION:-} ]]; then
            $YQ eval 'del(.resources[] | select(. == "../../third-party/jobset"))' -i "$manifests_temp_dir/overlays/manager/kustomization.yaml"
        fi
        kubectl apply --kubeconfig="${kubeconfig}" --server-side -k "$manifests_temp_dir/overlays/manager"
    )
    # In order to install the training runtimes we need to wait for the ClusterTrainingRuntime webhook to be ready
    kubectl wait --kubeconfig="${kubeconfig}" deploy/"${deployment_name}" -n "${ns}" --for=condition=available --timeout=5m
    kubectl apply --kubeconfig="${kubeconfig}" --server-side -k "${KUBEFLOW_TRAINER_MANIFEST}/overlays/runtimes"
}

# $1 cluster name
# $2 kubeconfig option
function install_mpi {
    local name=$1
    local kubeconfig=${2:-}
    local ns="${KUBEFLOW_MPI_OPERATOR_NAMESPACE:-mpi-operator}"
    local deployment_name="${KUBEFLOW_MPI_OPERATOR_DEPLOYMENT:-mpi-operator}"
    local expected_version="${KUBEFLOW_MPI_VERSION:-}"

    if [[ "${E2E_MODE}" == "dev" ]] && e2e_is_truthy "${E2E_ENFORCE_OPERATOR_UPDATE}"; then
        if e2e_deployment_exists "${kubeconfig}" "${ns}" "${deployment_name}"; then
            local installed_version=""
            local images=""
            images=$(kubectl --kubeconfig="${kubeconfig}" -n "${ns}" get deployment "${deployment_name}" \
                -o jsonpath='{.spec.template.spec.containers[*].image}' 2>/dev/null || true)
            local img=""
            for img in ${images}; do
                if [[ "${img}" == *"mpi-operator"* ]]; then
                    installed_version=$(e2e_image_ref_get_tag "${img}" || true)
                    break
                fi
            done
            if [[ -n "${installed_version}" ]] && { [[ -z "${expected_version}" ]] || e2e_versions_match "${installed_version}" "${expected_version}"; }; then
                echo "Kubeflow MPI operator already installed (${installed_version}); skipping install (E2E_MODE=dev)."
                return 0
            fi
            if [[ -n "${installed_version}" && -n "${expected_version}" ]]; then
                echo "Kubeflow MPI operator installed version (${installed_version}) does not match requested (${expected_version}); upgrading."
            else
                echo "Kubeflow MPI operator already present; upgrading."
            fi
        else
            echo "Kubeflow MPI operator controller deployment not found; installing."
        fi
    fi

    cluster_kind_load_image "${name}" "${KUBEFLOW_MPI_IMAGE/#v}"
    # NOTE: When reusing an existing cluster (E2E_MODE=dev), aggregated ClusterRoles may already have
    # their `.rules` field managed by the `clusterrole-aggregation-controller`. The upstream MPI
    # operator manifest can include `rules: []` for such ClusterRoles, which causes SSA conflicts.
    #
    # To keep installs idempotent without `--force-conflicts`, drop empty `rules: []` only for
    # ClusterRoles that define an `aggregationRule`.
    curl -sSL "${KUBEFLOW_MPI_MANIFEST}" \
        | $YQ eval '(. | select(.kind == "ClusterRole" and has("aggregationRule"))) |= del(.rules | select(length == 0))' - \
        | kubectl apply --kubeconfig="${kubeconfig}" --server-side -f -
    kubectl wait --kubeconfig="${kubeconfig}" deploy/"${deployment_name}" -n "${ns}" --for=condition=available --timeout=5m || true
}

# $1 cluster name
# $2 kubeconfig option
function install_kuberay {
    local name=$1
    local kubeconfig=${2:-}
    local ns="${KUBERAY_NAMESPACE:-default}"
    local deployment_name="${KUBERAY_DEPLOYMENT:-kuberay-operator}"
    local expected_version="${KUBERAY_VERSION:-}"
    local force_reinstall=false
    local -a kubectl_args=()
    if [[ -n "${kubeconfig}" ]]; then
        kubectl_args+=(--kubeconfig="${kubeconfig}")
    fi

    if [[ "${E2E_MODE}" == "dev" ]] && e2e_is_truthy "${E2E_ENFORCE_OPERATOR_UPDATE}"; then
        if e2e_deployment_exists "${kubeconfig}" "${ns}" "${deployment_name}"; then
            local installed_version=""
            local img=""
            img=$(kubectl --kubeconfig="${kubeconfig}" -n "${ns}" get deployment "${deployment_name}" \
                -o jsonpath='{.spec.template.spec.containers[?(@.name=="kuberay-operator")].image}' 2>/dev/null || true)
            installed_version=$(e2e_image_ref_get_tag "${img}" || true)
            if [[ -n "${installed_version}" ]] && { [[ -z "${expected_version}" ]] || e2e_versions_match "${installed_version}" "${expected_version}"; }; then
                echo "KubeRay operator already installed (${installed_version}); skipping install (E2E_MODE=dev)."
                return 0
            fi
            if [[ -n "${installed_version}" && -n "${expected_version}" ]]; then
                echo "KubeRay operator installed version (${installed_version}) does not match requested (${expected_version}); reinstalling."
            else
                echo "KubeRay operator already present; reinstalling."
            fi
            force_reinstall=true
        else
            echo "KubeRay operator deployment not found; installing."
        fi
    fi

    cluster_kind_load_image "$name" "${KUBERAY_RAY_IMAGE}"
    cluster_kind_load_image "$name" "${KUBERAY_IMAGE}"
    # In E2E_MODE=dev we keep and reuse the kind cluster between runs.
    #
    # "kubectl create -k" is used instead of apply (https://github.com/ray-project/kuberay/issues/504),
    # but it is not idempotent: it exits non-zero if any objects already exist.

    if e2e_is_truthy "${E2E_ENFORCE_OPERATOR_UPDATE}" || e2e_is_truthy "${force_reinstall}"; then
        kubectl ${kubectl_args[@]+"${kubectl_args[@]}"} delete -k "${KUBERAY_MANIFEST}" --ignore-not-found=true
    fi
    kubectl ${kubectl_args[@]+"${kubectl_args[@]}"} create -k "${KUBERAY_MANIFEST}"
    kubectl ${kubectl_args[@]+"${kubectl_args[@]}"} wait deploy/"${deployment_name}" -n "${ns}" --for=condition=available --timeout=5m || true
}

# $1 cluster name
# $2 kubeconfig option
function install_lws {
    local name=$1
    local kubeconfig=${2:-}
    local ns="${LEADERWORKERSET_NAMESPACE:-lws-system}"
    local deployment_name="${LEADERWORKERSET_DEPLOYMENT:-lws-controller-manager}"
    local expected_version="${LEADERWORKERSET_VERSION:-}"

    if [[ "${E2E_MODE}" == "dev" ]] && e2e_is_truthy "${E2E_ENFORCE_OPERATOR_UPDATE}"; then
        if e2e_deployment_exists "${kubeconfig}" "${ns}" "${deployment_name}"; then
            local installed_version=""
            local images=""
            images=$(kubectl --kubeconfig="${kubeconfig}" -n "${ns}" get deployment "${deployment_name}" \
                -o jsonpath='{.spec.template.spec.containers[*].image}' 2>/dev/null || true)
            local img=""
            for img in ${images}; do
                if [[ "${img}" == *"/lws:"* || "${img}" == *"/lws@"* || "${img}" == *"/lws"* ]]; then
                    installed_version=$(e2e_image_ref_get_tag "${img}" || true)
                    break
                fi
            done
            if [[ -n "${installed_version}" ]] && { [[ -z "${expected_version}" ]] || e2e_versions_match "${installed_version}" "${expected_version}"; }; then
                echo "LeaderWorkerSet already installed (${installed_version}); skipping install (E2E_MODE=dev)."
                return 0
            fi
            if [[ -n "${installed_version}" && -n "${expected_version}" ]]; then
                echo "LeaderWorkerSet installed version (${installed_version}) does not match requested (${expected_version}); upgrading."
            else
                echo "LeaderWorkerSet already present; upgrading."
            fi
        else
            echo "LeaderWorkerSet controller deployment not found; installing."
        fi
    fi

    cluster_kind_load_image "${name}" "${LEADERWORKERSET_IMAGE/#v}"
    kubectl apply --kubeconfig="${kubeconfig}" --server-side -f "${LEADERWORKERSET_MANIFEST}"
    kubectl wait --kubeconfig="${kubeconfig}" deploy/"${deployment_name}" -n "${ns}" --for=condition=available --timeout=5m || true
}

# $1 kubeconfig option
function install_cert_manager {
    local kubeconfig=${1:-}
    local ns="${CERTMANAGER_NAMESPACE:-cert-manager}"
    local deployment_name="${CERTMANAGER_DEPLOYMENT:-cert-manager}"
    local expected_version="${CERTMANAGER_VERSION:-}"

    if [[ "${E2E_MODE}" == "dev" ]] && e2e_is_truthy "${E2E_ENFORCE_OPERATOR_UPDATE}"; then
        if e2e_deployment_exists "${kubeconfig}" "${ns}" "${deployment_name}"; then
            local installed_version=""
            local img=""
            img=$(kubectl --kubeconfig="${kubeconfig}" -n "${ns}" get deployment "${deployment_name}" \
                -o jsonpath='{.spec.template.spec.containers[?(@.name=="cert-manager")].image}' 2>/dev/null || true)
            if [[ -z "${img}" ]]; then
                # Fallback: some manifests use container name "cert-manager-controller".
                img=$(kubectl --kubeconfig="${kubeconfig}" -n "${ns}" get deployment "${deployment_name}" \
                    -o jsonpath='{.spec.template.spec.containers[?(@.name=="cert-manager-controller")].image}' 2>/dev/null || true)
            fi
            installed_version=$(e2e_image_ref_get_tag "${img}" || true)
            if [[ -n "${installed_version}" ]] && { [[ -z "${expected_version}" ]] || e2e_versions_match "${installed_version}" "${expected_version}"; }; then
                echo "cert-manager already installed (${installed_version}); skipping install (E2E_MODE=dev)."
                return 0
            fi
            if [[ -n "${installed_version}" && -n "${expected_version}" ]]; then
                echo "cert-manager installed version (${installed_version}) does not match requested (${expected_version}); upgrading."
            else
                echo "cert-manager already present; upgrading."
            fi
        else
            echo "cert-manager deployment not found; installing."
        fi
    fi

    kubectl apply --kubeconfig="${kubeconfig}" --server-side -f "${CERTMANAGER_MANIFEST}"
    kubectl wait --kubeconfig="${kubeconfig}" deploy/cert-manager -n "${ns}" --for=condition=available --timeout=5m
    kubectl wait --kubeconfig="${kubeconfig}" deploy/cert-manager-webhook -n "${ns}" --for=condition=available --timeout=5m || true
    kubectl wait --kubeconfig="${kubeconfig}" deploy/cert-manager-cainjector -n "${ns}" --for=condition=available --timeout=5m || true
}

# $1 kubeconfig option
function install_prometheus_operator {
    local kubeconfig=${1:-}
    local ns="default"
    local deployment_name="prometheus-operator"
    local expected_version="${PROMETHEUS_OPERATOR_VERSION:-}"

    if [[ "${E2E_MODE}" == "dev" ]] && e2e_is_truthy "${E2E_ENFORCE_OPERATOR_UPDATE}"; then
        if e2e_deployment_exists "${kubeconfig}" "${ns}" "${deployment_name}"; then
            local installed_version=""
            local img=""
            img=$(kubectl --kubeconfig="${kubeconfig}" -n "${ns}" get deployment "${deployment_name}" \
                -o jsonpath='{.spec.template.spec.containers[?(@.name=="prometheus-operator")].image}' 2>/dev/null || true)
            installed_version=$(e2e_image_ref_get_tag "${img}" || true)
            if [[ -n "${installed_version}" ]] && { [[ -z "${expected_version}" ]] || e2e_versions_match "${installed_version}" "${expected_version}"; }; then
                echo "Prometheus operator already installed (${installed_version}); skipping install (E2E_MODE=dev)."
                return 0
            fi
            if [[ -n "${installed_version}" && -n "${expected_version}" ]]; then
                echo "Prometheus operator installed version (${installed_version}) does not match requested (${expected_version}); reinstalling."
            else
                echo "Prometheus operator already present; reinstalling."
            fi
        else
            echo "Prometheus operator deployment not found; installing."
        fi
    fi

    kubectl apply --kubeconfig="${kubeconfig}" --server-side -f "${PROMETHEUS_OPERATOR_BUNDLE}"
    kubectl wait deploy/"${deployment_name}" -n "${ns}" \
        --for=condition=available --timeout=5m --kubeconfig="${kubeconfig}"
    kubectl apply --kubeconfig="${kubeconfig}" --server-side \
        -f "${ROOT_DIR}/test/e2e/config/prometheus/prometheus-setup.yaml"
}

# $1 kubeconfig option
function deploy_kueue_prometheus_config {
    local kubeconfig=${1:-}
    $KUSTOMIZE build "${ROOT_DIR}/config/prometheus" | \
        kubectl apply --kubeconfig="${kubeconfig}" --server-side -f -
}

# $1 kubeconfig option
function install_multicluster {
    local kubeconfig=${1:-}
    if [[ "${E2E_MODE}" == "dev" ]] && e2e_is_truthy "${E2E_ENFORCE_OPERATOR_UPDATE}"; then
        if e2e_crd_exists "${kubeconfig}" "clusterprofiles.multicluster.x-k8s.io"; then
            echo "ClusterProfile CRD already installed; skipping install (E2E_MODE=dev)."
            return 0
        fi
        echo "ClusterProfile CRD not found; installing."
    fi
    kubectl apply --kubeconfig="${kubeconfig}" --server-side -f "${CLUSTERPROFILE_CRD}"
}

# $1 cluster name
# $2 kubeconfig option
function install_dra_example_driver {
    local name=$1
    local kubeconfig=${2:-}
    local expected_version="${DRA_EXAMPLE_DRIVER_VERSION:-}"

    if [[ "${E2E_MODE}" == "dev" ]] && e2e_is_truthy "${E2E_ENFORCE_OPERATOR_UPDATE}"; then
        local installed_version=""
        local images=""
        images=$(kubectl --kubeconfig="${kubeconfig}" -n dra-example-driver get deployments \
            -o jsonpath='{range .items[*]}{range .spec.template.spec.containers[*]}{.image}{"\n"}{end}{end}' 2>/dev/null || true)
        local img=""
        while IFS= read -r img; do
            [[ -n "${img}" ]] || continue
            if [[ "${img}" == *"dra-example-driver"* ]]; then
                installed_version=$(e2e_image_ref_get_tag "${img}" || true)
                [[ -n "${installed_version}" ]] && break
            fi
        done <<< "${images}"
        if [[ -n "${installed_version}" ]] && { [[ -z "${expected_version}" ]] || e2e_versions_match "${installed_version}" "${expected_version}"; }; then
            echo "DRA example driver already installed (${installed_version}); skipping install (E2E_MODE=dev)."
            return 0
        fi
        if [[ -n "${installed_version}" && -n "${expected_version}" ]]; then
            echo "DRA example driver installed version (${installed_version}) does not match requested (${expected_version}); upgrading."
        fi
    fi

    local dra_driver_temp_dir
    dra_driver_temp_dir=$(mktemp -d)
    # shellcheck disable=SC2064 # Intentionally expand now to capture the temp dir path
    trap "rm -rf '$dra_driver_temp_dir'" RETURN
    git clone --depth 1 --branch "${DRA_EXAMPLE_DRIVER_VERSION}" "${DRA_EXAMPLE_DRIVER_REPO}" "$dra_driver_temp_dir"

    local dra_image_repo="dra-example-driver"
    local dra_image_tag="${expected_version#v}"
    dra_image_tag="${dra_image_tag//\//-}"
    if [[ -z "${dra_image_tag}" ]]; then
        dra_image_tag="local"
    fi

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

    cluster_kind_load_image "${name}" "${dra_image_repo}:${dra_image_tag}"

    $HELM upgrade -i \
      --create-namespace \
      --namespace dra-example-driver \
      --kubeconfig="${kubeconfig}" \
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

    echo "Upgrade Test: $old_version -> current"
    echo "Old image: $KUEUE_OLD_VERSION_IMAGE"
    echo "New image: $IMAGE_TAG"
    
    # Step 1: Install old version using the released image from registry.k8s.io
    echo "Installing $old_version..."
    echo "  Manifest URL: ${KUEUE_OLD_VERSION_MANIFEST}"
    echo "  Downloading and modifying manifests..."
    
    # Download manifests, rewrite the image reference to match the pre-loaded
    # image, and set imagePullPolicy to IfNotPresent so kind uses it directly.
    curl -sL "${KUEUE_OLD_VERSION_MANIFEST}" | \
      sed "s|registry.k8s.io/kueue/kueue:${old_version}|${KUEUE_OLD_VERSION_IMAGE}|g" | \
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
