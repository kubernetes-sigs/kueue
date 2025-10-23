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

# This script sets up MultiKueue + TAS (Topology Aware Scheduling) for e2e testing.
# It creates the same configuration that would be used in production but adapted for kind clusters.

set -o errexit
set -o nounset
set -o pipefail

SOURCE_DIR="$(cd "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT_DIR="$SOURCE_DIR/.."

# Use environment variables or defaults
KIND_CLUSTER_NAME=${KIND_CLUSTER_NAME:-kind}
MANAGER_KIND_CLUSTER_NAME=${KIND_CLUSTER_NAME}-manager
WORKER1_KIND_CLUSTER_NAME=${KIND_CLUSTER_NAME}-worker1
WORKER2_KIND_CLUSTER_NAME=${KIND_CLUSTER_NAME}-worker2
KUEUE_NAMESPACE=${KUEUE_NAMESPACE:-kueue-system}
ARTIFACTS=${ARTIFACTS:-/tmp/artifacts}

echo "Setting up MultiKueue + TAS configuration..."
echo "Manager cluster: $MANAGER_KIND_CLUSTER_NAME"
echo "Worker1 cluster: $WORKER1_KIND_CLUSTER_NAME"
echo "Worker2 cluster: $WORKER2_KIND_CLUSTER_NAME"
echo ""

mkdir -p "$ARTIFACTS"

# First, run the base multikueue secrets setup
echo "Setting up MultiKueue secrets first..."
bash "$SOURCE_DIR/setup-multikueue-secrets.sh"

echo ""
echo "========================================="
echo "Waiting for Kueue to be ready"
echo "========================================="
echo ""

# Function to wait for Kueue webhook to be ready
wait_for_kueue() {
    local context=$1
    local cluster_name=$2

    echo "Waiting for Kueue webhook to be ready on $cluster_name..."
    kubectl config use-context "$context"

    # Wait for webhook deployment to be available
    kubectl wait --for=condition=available --timeout=120s deployment/kueue-controller-manager -n "$KUEUE_NAMESPACE" || true

    # Give webhook a few more seconds to register
    sleep 5

    echo "✓ Kueue ready on $cluster_name"
}

# Wait for Kueue on all clusters
wait_for_kueue "kind-$MANAGER_KIND_CLUSTER_NAME" "manager"
wait_for_kueue "kind-$WORKER1_KIND_CLUSTER_NAME" "worker1"
wait_for_kueue "kind-$WORKER2_KIND_CLUSTER_NAME" "worker2"

echo ""
echo "========================================="
echo "Configuring TAS on Worker Clusters"
echo "========================================="
echo ""

# Function to setup TAS on a worker cluster
setup_worker_tas() {
    local worker_context=$1
    local worker_name=$2

    echo "Configuring TAS on $worker_name..."
    kubectl config use-context "kind-$worker_context"

    # Apply worker TAS configuration
    kubectl apply -f - <<EOF
apiVersion: kueue.x-k8s.io/v1beta1
kind: Topology
metadata:
  name: "default"
spec:
  levels:
  - nodeLabel: "kubernetes.io/hostname"
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: ResourceFlavor
metadata:
  name: "default-tas-flavor"
spec:
  nodeLabels:
    kubernetes.io/arch: arm64
  topologyName: "default"
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "cluster-queue"
spec:
  namespaceSelector: {}
  resourceGroups:
  - coveredResources: ["cpu", "memory"]
    flavors:
    - name: "default-tas-flavor"
      resources:
      - name: "cpu"
        nominalQuota: 9
      - name: "memory"
        nominalQuota: 36Gi
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: LocalQueue
metadata:
  namespace: "default"
  name: "user-queue"
spec:
  clusterQueue: "cluster-queue"
EOF

    echo "✓ TAS configured on $worker_name"
}

# Setup TAS on both worker clusters
setup_worker_tas "$WORKER1_KIND_CLUSTER_NAME" "worker1"
setup_worker_tas "$WORKER2_KIND_CLUSTER_NAME" "worker2"

echo ""
echo "========================================="
echo "Configuring Manager Cluster"
echo "========================================="
echo ""

# Switch to manager cluster
kubectl config use-context "kind-$MANAGER_KIND_CLUSTER_NAME"

echo "Applying MultiKueue + TAS configuration on manager..."

# Apply manager configuration with MultiKueue + TAS
kubectl apply -f - <<EOF
apiVersion: kueue.x-k8s.io/v1beta1
kind: Topology
metadata:
  name: "default"
spec:
  levels:
  - nodeLabel: "kubernetes.io/hostname"
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: ResourceFlavor
metadata:
  name: "default-flavor"
spec:
  nodeLabels:
    kubernetes.io/arch: arm64
  topologyName: "default"
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: AdmissionCheck
metadata:
  name: multikueue-ac
spec:
  controllerName: kueue.x-k8s.io/multikueue
  parameters:
    apiGroup: kueue.x-k8s.io
    kind: MultiKueueConfig
    name: multikueue-config
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: MultiKueueConfig
metadata:
  name: multikueue-config
spec:
  clusters:
  - multikueue-worker1
  - multikueue-worker2
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: MultiKueueCluster
metadata:
  name: multikueue-worker1
spec:
  kubeConfig:
    locationType: Secret
    location: multikueue1
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: MultiKueueCluster
metadata:
  name: multikueue-worker2
spec:
  kubeConfig:
    locationType: Secret
    location: multikueue2
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "cluster-queue"
spec:
  namespaceSelector: {}
  resourceGroups:
  - coveredResources: ["cpu", "memory"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "cpu"
        nominalQuota: 9
      - name: "memory"
        nominalQuota: 36Gi
  admissionChecks:
  - multikueue-ac
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: LocalQueue
metadata:
  namespace: "default"
  name: "user-queue"
spec:
  clusterQueue: "cluster-queue"
EOF

echo "✓ Manager cluster configured with MultiKueue + TAS"

echo ""
echo "========================================="
echo "MultiKueue + TAS Setup Complete!"
echo "========================================="
echo ""
