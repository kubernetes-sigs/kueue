#!/usr/bin/env bash

# Copyright The Kubernetes Authors.
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

DEFAULT_KUEUE_VERSION=v0.16.1
KUEUE_VERSION="${KUEUE_VERSION:-${DEFAULT_KUEUE_VERSION}}"
USE_MAIN_BRANCH="${USE_MAIN_BRANCH:-false}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKER_CLUSTERS="worker1 worker2"

# Parse command line flags
while [[ $# -gt 0 ]]; do
    case $1 in
        --main)
            USE_MAIN_BRANCH=true
            shift
            ;;
        --version)
            KUEUE_VERSION="$2"
            shift 2
            ;;
        *)
            echo "Unknown option: $1"
            echo "Usage: $0 [--main] [--version VERSION]"
            echo "  --main           Use main branch instead of released version"
            echo "  --version        Specify Kueue version (default: ${DEFAULT_KUEUE_VERSION})"
            exit 1
            ;;
    esac
done

echo "========================================"
echo "MultiKueue + TAS Setup"
echo "========================================"

# Create Kind clusters
echo "[1/5] Creating Kind clusters..."
if kind get clusters 2>/dev/null | grep -q "^manager$"; then
    echo "Cluster manager already exists, skipping..."
else
    echo "Creating cluster: manager"
    kind create cluster --name manager --config="${SCRIPT_DIR}/manager-kind-config.yaml"
fi

for cluster in ${WORKER_CLUSTERS}; do
    if kind get clusters 2>/dev/null | grep -q "^${cluster}$"; then
        echo "Cluster ${cluster} already exists, skipping..."
    else
        echo "Creating cluster: ${cluster}"
        kind create cluster --name "${cluster}" --config="${SCRIPT_DIR}/worker-kind-config.yaml"
    fi
done

kubectl config use-context kind-manager

# Install Kueue
if [ "$USE_MAIN_BRANCH" = "true" ]; then
    echo "[2/5] Installing Kueue from main branch..."
    KUEUE_APPLY_FLAGS="-k github.com/kubernetes-sigs/kueue/config/default?ref=main"
else
    echo "[2/5] Installing Kueue ${KUEUE_VERSION}..."
    KUEUE_APPLY_FLAGS="-f https://github.com/kubernetes-sigs/kueue/releases/download/${KUEUE_VERSION}/manifests.yaml"
fi

for cluster in manager ${WORKER_CLUSTERS}; do
    # shellcheck disable=SC2086
    kubectl --context "kind-${cluster}" apply --server-side ${KUEUE_APPLY_FLAGS}
    kubectl --context "kind-${cluster}" wait --for=condition=available --timeout=300s deployment/kueue-controller-manager -n kueue-system
done

cat > /tmp/kueue-integrations-patch.yaml <<'EOF'
data:
  controller_manager_config.yaml: |
    apiVersion: config.kueue.x-k8s.io/v1beta2
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
EOF
kubectl --context kind-manager patch configmap kueue-manager-config -n kueue-system --patch-file=/tmp/kueue-integrations-patch.yaml
kubectl --context kind-manager rollout restart deployment/kueue-controller-manager -n kueue-system
kubectl --context kind-manager rollout status deployment/kueue-controller-manager -n kueue-system --timeout=180s

# Configure workers
echo "[3/5] Configuring worker clusters..."
for cluster in ${WORKER_CLUSTERS}; do
    kubectl --context "kind-${cluster}" apply -f "${SCRIPT_DIR}/../tas/worker-setup.yaml"
done

# Create worker kubeconfigs
echo "[4/5] Creating worker kubeconfigs..."
for cluster in ${WORKER_CLUSTERS}; do
    # Create ServiceAccount
    kubectl --context "kind-${cluster}" create sa multikueue-sa -n kueue-system 2>/dev/null || true

    # Create ClusterRole for Jobs
    kubectl --context "kind-${cluster}" create clusterrole multikueue-role \
      --verb=create,delete,get,list,watch \
      --resource=jobs.batch,workloads.kueue.x-k8s.io,pods 2>/dev/null || true

    kubectl --context "kind-${cluster}" create clusterrole multikueue-role-status \
      --verb=get,patch,update \
      --resource=jobs.batch/status,workloads.kueue.x-k8s.io/status,pods/status 2>/dev/null || true

    # Create ClusterRoleBindings
    kubectl --context "kind-${cluster}" create clusterrolebinding multikueue-crb \
      --clusterrole=multikueue-role \
      --serviceaccount=kueue-system:multikueue-sa 2>/dev/null || true

    kubectl --context "kind-${cluster}" create clusterrolebinding multikueue-crb-status \
      --clusterrole=multikueue-role-status \
      --serviceaccount=kueue-system:multikueue-sa 2>/dev/null || true

    # Create token
    TOKEN=$(kubectl --context "kind-${cluster}" create token multikueue-sa -n kueue-system --duration=24h)

    # Extract the Certificate Authority
    CA_DATA=$(kubectl --context "kind-${cluster}" config view --minify --raw -o jsonpath='{.clusters[0].cluster.certificate-authority-data}')

    # Create kubeconfig with insecure-skip-tls-verify for Kind clusters
    cat > "${SCRIPT_DIR}/${cluster}.kubeconfig" <<EOF
apiVersion: v1
kind: Config
clusters:
- cluster:
    certificate-authority-data: ${CA_DATA}
    server: https://${cluster}-control-plane:6443
  name: ${cluster}
contexts:
- context:
    cluster: ${cluster}
    user: multikueue-sa
  name: ${cluster}
current-context: ${cluster}
users:
- name: multikueue-sa
  user:
    token: ${TOKEN}
EOF
done

# Configure manager
echo "[5/5] Configuring manager cluster..."
for cluster in ${WORKER_CLUSTERS}; do
    kubectl --context kind-manager create secret generic "${cluster}-secret" -n kueue-system --from-file=kubeconfig="${SCRIPT_DIR}/${cluster}.kubeconfig" --dry-run=client -o yaml | kubectl --context kind-manager apply -f -
done
kubectl --context kind-manager apply -f "${SCRIPT_DIR}/../tas/manager-setup.yaml"

echo ""
echo "========================================"
echo "✓ Setup Complete"
echo "========================================"
echo ""
echo "Submit sample job:"
echo "  kubectl --context kind-manager apply -f ${SCRIPT_DIR}/sample-tas-job.yaml"
echo ""
echo "Monitor workloads:"
echo "  kubectl --context kind-manager get workloads.kueue.x-k8s.io -n default -w"
echo ""
echo "Cleanup:"
echo "  kind delete clusters manager ${WORKER_CLUSTERS}"
