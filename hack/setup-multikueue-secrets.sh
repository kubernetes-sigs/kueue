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

# This script sets up MultiKueue secrets for e2e testing when using E2E_RUN_ONLY_ENV=true.
# It creates the same secrets that would normally be created by the test suite's BeforeSuite hook.

set -o errexit
set -o nounset
set -o pipefail

SOURCE_DIR="$(cd "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT_DIR="$SOURCE_DIR/.."

# Use environment variables or defaults
MANAGER_KIND_CLUSTER_NAME=${MANAGER_KIND_CLUSTER_NAME:-kind-manager}
WORKER1_KIND_CLUSTER_NAME=${WORKER1_KIND_CLUSTER_NAME:-kind-worker1}
WORKER2_KIND_CLUSTER_NAME=${WORKER2_KIND_CLUSTER_NAME:-kind-worker2}
KUEUE_NAMESPACE=${KUEUE_NAMESPACE:-kueue-system}
ARTIFACTS=${ARTIFACTS:-/tmp/artifacts}

echo "Setting up MultiKueue secrets for e2e environment..."
echo "Manager cluster: $MANAGER_KIND_CLUSTER_NAME"
echo "Worker1 cluster: $WORKER1_KIND_CLUSTER_NAME"
echo "Worker2 cluster: $WORKER2_KIND_CLUSTER_NAME"
echo "Kueue namespace: $KUEUE_NAMESPACE"

mkdir -p "$ARTIFACTS"

# Function to create kubeconfig for a worker cluster
# $1: worker cluster context name
# $2: output kubeconfig file
# $3: cluster control plane hostname (e.g., kind-worker1-control-plane)
create_worker_kubeconfig() {
    local worker_context=$1
    local output_file=$2
    local control_plane_host=$3

    echo "Creating kubeconfig for $worker_context..."

    # Switch to worker cluster context
    kubectl config use-context "$worker_context"

    # Create the service account, role, and binding using the existing script
    bash "$ROOT_DIR/examples/multikueue/create-multikueue-kubeconfig.sh" "$output_file"

    # Add missing permissions for e2e tests (trainer.kubeflow.org and jaxjobs)
    echo "Adding additional permissions for e2e tests..."
    kubectl apply -f - <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: multikueue-sa-e2e-extras
rules:
- apiGroups:
  - trainer.kubeflow.org
  resources:
  - trainjobs
  verbs:
  - create
  - delete
  - get
  - list
  - watch
- apiGroups:
  - trainer.kubeflow.org
  resources:
  - trainjobs/status
  verbs:
  - get
- apiGroups:
  - kubeflow.org
  resources:
  - jaxjobs
  verbs:
  - create
  - delete
  - get
  - list
  - watch
- apiGroups:
  - kubeflow.org
  resources:
  - jaxjobs/status
  verbs:
  - get
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: multikueue-sa-e2e-extras-binding
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: multikueue-sa-e2e-extras
subjects:
- kind: ServiceAccount
  name: multikueue-sa
  namespace: ${KUEUE_NAMESPACE}
EOF

    # Get the current cluster server address
    CURRENT_CLUSTER=$(kubectl config view -o jsonpath="{.contexts[?(@.name == \"${worker_context}\")].context.cluster}")
    CA_CERT=$(kubectl config view --raw -o jsonpath="{.clusters[?(@.name == \"${CURRENT_CLUSTER}\")].cluster.certificate-authority-data}")

    # Get the token from the secret
    SA_TOKEN=$(kubectl get secret -n "$KUEUE_NAMESPACE" multikueue-sa -o jsonpath='{.data.token}' | base64 -d)

    # Recreate the kubeconfig with the control plane hostname (for inter-cluster communication in kind)
    cat > "$output_file" <<EOF
apiVersion: v1
kind: Config
clusters:
- cluster:
    certificate-authority-data: ${CA_CERT}
    server: https://${control_plane_host}:6443
  name: default-cluster
contexts:
- context:
    cluster: default-cluster
    user: default-user
  name: default-context
current-context: default-context
users:
- name: default-user
  user:
    token: ${SA_TOKEN}
EOF

    echo "✓ Created kubeconfig for $worker_context at $output_file"
}

# Create kubeconfigs for both worker clusters
create_worker_kubeconfig "kind-$WORKER1_KIND_CLUSTER_NAME" "$ARTIFACTS/worker1-kubeconfig" "$WORKER1_KIND_CLUSTER_NAME-control-plane"
create_worker_kubeconfig "kind-$WORKER2_KIND_CLUSTER_NAME" "$ARTIFACTS/worker2-kubeconfig" "$WORKER2_KIND_CLUSTER_NAME-control-plane"

# Switch to manager cluster and create secrets
echo "Creating secrets in manager cluster..."
kubectl config use-context "kind-$MANAGER_KIND_CLUSTER_NAME"

# Create secret for worker1
kubectl create secret generic multikueue1 \
    -n "$KUEUE_NAMESPACE" \
    --from-file=kubeconfig="$ARTIFACTS/worker1-kubeconfig" \
    --dry-run=client -o yaml | kubectl apply -f -

echo "✓ Created secret 'multikueue1' in namespace '$KUEUE_NAMESPACE'"

# Create secret for worker2
kubectl create secret generic multikueue2 \
    -n "$KUEUE_NAMESPACE" \
    --from-file=kubeconfig="$ARTIFACTS/worker2-kubeconfig" \
    --dry-run=client -o yaml | kubectl apply -f -

echo "✓ Created secret 'multikueue2' in namespace '$KUEUE_NAMESPACE'"

echo ""
echo "✅ MultiKueue secrets setup complete!"

# Wait for Kueue webhook to be ready on manager cluster before applying configuration
echo ""
echo "Waiting for Kueue webhook to be ready on manager cluster..."
kubectl config use-context "kind-$MANAGER_KIND_CLUSTER_NAME"
kubectl wait --for=condition=available --timeout=120s deployment/kueue-controller-manager -n "$KUEUE_NAMESPACE" || true
sleep 5
echo "✓ Kueue webhook ready"

# Deploy MultiKueue configuration
echo ""
echo "Deploying MultiKueue configuration..."

# Create a modified version of the multikueue-setup.yaml for e2e testing
cat > "$ARTIFACTS/multikueue-e2e-setup.yaml" <<EOF
apiVersion: kueue.x-k8s.io/v1beta1
kind: ResourceFlavor
metadata:
  name: "default-flavor"
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "cluster-queue"
spec:
  namespaceSelector: {} # match all.
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
EOF

kubectl apply -f "$ARTIFACTS/multikueue-e2e-setup.yaml"

echo "✓ Applied MultiKueue configuration"
echo ""
echo "Complete MultiKueue setup finished!"
echo ""
