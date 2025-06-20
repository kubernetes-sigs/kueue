#!/bin/bash

set -o nounset
set -o pipefail
set -o xtrace


KUEUE_VERSION=v0.12.0-rc.3
regions=("europe-west4" "us-east4")
kubeconfigs=("manager-europe-west4-a" "worker-us-east4-a")
PROJECT_ID=$(gcloud config get-value project)
PROJECT_NUMBER=$(gcloud projects describe $PROJECT_ID --format="value(projectNumber)")
IMAGE_REGISTRY=us-west1-docker.pkg.dev/${PROJECT_ID}/testing/kueue
CUSTOM_INSTALL="${KUEUE_CUSTOM_INSTALL:-false}"
BUILD_IMAGE="${KUEUE_BUILD_IMAGE:-false}"


function set_up_kueue_integrations() {
    cat > "custom-config.yaml" <<EOF
apiVersion: v1
data:
  controller_manager_config.yaml: |
    apiVersion: config.kueue.x-k8s.io/v1beta1
    kind: Configuration
    health:
      healthProbeBindAddress: :8081
    metrics:
      bindAddress: :8443
    webhook:
      port: 9443
    leaderElection:
      leaderElect: true
      resourceName: c1f6bfd2.kueue.x-k8s.io
    controller:
      groupKindConcurrency:
        Job.batch: 5
        Pod: 5
        Workload.kueue.x-k8s.io: 5
        LocalQueue.kueue.x-k8s.io: 1
        Cohort.kueue.x-k8s.io: 1
        ClusterQueue.kueue.x-k8s.io: 1
        ResourceFlavor.kueue.x-k8s.io: 1
    clientConnection:
      qps: 50
      burst: 100
    integrations:
      frameworks:
      - "batch/job"
kind: ConfigMap
metadata:
  labels:
    app.kubernetes.io/component: controller
    app.kubernetes.io/name: kueue
    control-plane: controller-manager
  name: kueue-manager-config
  namespace: kueue-system
EOF

    kubectl replace -f custom-config.yaml
    rm custom-config.yaml
}

function build_kueue_image() {
    pushd ../../.. > /dev/null
    IMAGE_REGISTRY=${IMAGE_REGISTRY} make image-local-push
    popd > /dev/null
}

function install_kueue() {
    if [[ "$CUSTOM_INSTALL" == "true" ]]; then
        pushd ../../.. > /dev/null
        IMAGE_REGISTRY=${IMAGE_REGISTRY} make deploy
        popd > /dev/null
    else
        kubectl apply --server-side -f "https://github.com/kubernetes-sigs/kueue/releases/download/${KUEUE_VERSION}/manifests.yaml"
    fi
}

function create_worker_kubeconfig() {
    local config=$1
    ./create-multikueue-kubeconfig.sh "${config}.kubeconfig"
}

function create_worker_secret() {
    local config=$1
    kubectl create secret generic "${config}-secret" -n kueue-system --from-file=kubeconfig="${config}.kubeconfig"
}

function patch_manager_deployment() {
    kubectl patch deployment kueue-controller-manager -n kueue-system --patch \
        '{"spec":{"template":{"spec":{"containers":[{"name":"manager","args":["--config=/controller_manager_config.yaml","--zap-log-level=8","--feature-gates=MultiKueueBatchJobWithManagedBy=true,TopologyAwareScheduling=true"]}]}}}}'
}

function patch_worker_deployment() {
    kubectl patch deployment kueue-controller-manager -n kueue-system --patch \
        '{"spec":{"template":{"spec":{"containers":[{"name":"manager","args":["--config=/controller_manager_config.yaml","--zap-log-level=8","--feature-gates=TopologyAwareScheduling=true"]}]}}}}'
}

function wait_for_deployment() {
    kubectl wait --for=condition=available --timeout=600s deployment/kueue-controller-manager -n kueue-system
}


if [[ "$BUILD_IMAGE" == "true" ]]; then
    build_kueue_image
fi

for i in "${!kubeconfigs[@]}"; do
    config="${kubeconfigs[$i]}"
    kubectl config use-context "$config"
    install_kueue
    set_up_kueue_integrations

    if [[ $i -ne 0 ]]; then
        create_worker_kubeconfig "$config"
    fi
done

kubectl config use-context "${kubeconfigs[0]}"
for i in "${!kubeconfigs[@]}"; do
    if [[ $i -ne 0 ]]; then
        create_worker_secret "${kubeconfigs[$i]}"
    fi
done

wait_for_deployment
patch_manager_deployment

for i in "${!kubeconfigs[@]}"; do
    if [[ $i -ne 0 ]]; then
        config="${kubeconfigs[$i]}"
        kubectl config use-context "$config"
        wait_for_deployment
        patch_worker_deployment
    fi
done

kubectl config use-context "${kubeconfigs[0]}"
kubectl rollout restart deployment -n kueue-system kueue-controller-manager
