#!/usr/bin/env bash

# Copyright 2025 The Kubernetes Authors.
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

# =============================================================================
# CONFIGURATION AND SETUP
# =============================================================================

SOURCE_DIR="$(cd "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT_DIR="$(cd "$SOURCE_DIR/../.." && pwd -P)"
HACK_DIR="$(cd "$SOURCE_DIR/.." && pwd -P)"

# Enable CRDs MultiKueue
export JOBSET_VERSION="${JOBSET_VERSION:-v0.8.0}"
export APPWRAPPER_VERSION="${APPWRAPPER_VERSION:-v0.27.0}"
export KUBEFLOW_VERSION="${KUBEFLOW_VERSION:-v1.8.1}"
export KUBEFLOW_MPI_VERSION="${KUBEFLOW_MPI_VERSION:-v0.5.0}"
export RAY_VERSION="${RAY_VERSION:-2.9.0}"
export RAYMINI_VERSION="${RAYMINI_VERSION:-2.9.0}"

# Cluster names and kubeconfig paths
export MANAGER_KIND_CLUSTER_NAME=${KIND_CLUSTER_NAME}-manager
export WORKER1_KIND_CLUSTER_NAME=${KIND_CLUSTER_NAME}-worker1
export WORKER2_KIND_CLUSTER_NAME=${KIND_CLUSTER_NAME}-worker2

export MANAGER_KUBECONFIG="${ARTIFACTS}/kubeconfig-$MANAGER_KIND_CLUSTER_NAME"
export WORKER1_KUBECONFIG="${ARTIFACTS}/kubeconfig-$WORKER1_KIND_CLUSTER_NAME"
export WORKER2_KUBECONFIG="${ARTIFACTS}/kubeconfig-$WORKER2_KIND_CLUSTER_NAME"

# Temporarily override SOURCE_DIR for e2e-common.sh to find agnhost Dockerfile
MULTIKUEUE_SOURCE_DIR="$SOURCE_DIR"
SOURCE_DIR="$HACK_DIR"

# shellcheck source=hack/e2e-common.sh
source "${ROOT_DIR}/hack/e2e-common.sh"

# Restore SOURCE_DIR for multikueue-specific files
SOURCE_DIR="$MULTIKUEUE_SOURCE_DIR"

# =============================================================================
# UTILITY FUNCTIONS
# =============================================================================

function ensure_artifacts_dir() {
    if [ ! -d "$ARTIFACTS" ]; then
        mkdir -p "$ARTIFACTS"
    fi
}

function print_environment_info() {
    echo "Using environment variables:"
    echo "  KIND_CLUSTER_NAME=$KIND_CLUSTER_NAME"
    echo "  ARTIFACTS=$ARTIFACTS"
    echo "  CREATE_KIND_CLUSTER=$CREATE_KIND_CLUSTER"
    echo "  E2E_KIND_VERSION=$E2E_KIND_VERSION"
    echo "  IMAGE_TAG=$IMAGE_TAG"
    echo "  JOBSET_VERSION=$JOBSET_VERSION"
    echo "  APPWRAPPER_VERSION=$APPWRAPPER_VERSION"
    echo "  KUBEFLOW_VERSION=$KUBEFLOW_VERSION"
    echo "  KUBEFLOW_MPI_VERSION=$KUBEFLOW_MPI_VERSION"
    echo "  Ray operator will be installed directly on worker clusters"
}

function cleanup_stale_files() {
    echo "Cleaning up any stale files from previous runs..."
    
    # Remove stale kubeconfig files
    rm -f "${ARTIFACTS}/kubeconfig-kind-"*
    rm -f "${ARTIFACTS}/"*"-multikueue-kubeconfig"
    
    # Remove any lock files that might be left behind
    find "${ARTIFACTS}" -name "*.lock" -delete 2>/dev/null || true
    
    echo "Stale file cleanup completed."
}

# =============================================================================
# REUSE EXISTING E2E FUNCTIONS
# =============================================================================

function cleanup() {
    if [ "$CREATE_KIND_CLUSTER" == 'true' ]; then
        ensure_artifacts_dir
        cluster_cleanup "$MANAGER_KIND_CLUSTER_NAME" "$MANAGER_KUBECONFIG" &
        cluster_cleanup "$WORKER1_KIND_CLUSTER_NAME" "$WORKER1_KUBECONFIG" &
        cluster_cleanup "$WORKER2_KIND_CLUSTER_NAME" "$WORKER2_KUBECONFIG" &
    fi
    wait
}

function startup() {
    if [ "$CREATE_KIND_CLUSTER" != 'true' ]; then
        return 0
    fi
    
    ensure_artifacts_dir
    cp "${SOURCE_DIR}/manager-cluster.kind.yaml" "$ARTIFACTS"

    # Enable the JobManagedBy feature gate for Kubernetes 1.31.
    # In newer versions, this feature is in Beta and enabled by default.
    IFS=. read -r -a varr <<< "$KIND_VERSION"
    minor=$(( varr[1] ))        
    if [ "$minor" -eq 31 ]; then
        echo "Enable JobManagedBy feature in manager's kind config"
        $YQ e -i '.featureGates.JobManagedBy = true' "${ARTIFACTS}/manager-cluster.kind.yaml"
    fi

    echo "Creating KIND clusters..."
    cluster_create "$MANAGER_KIND_CLUSTER_NAME" "${ARTIFACTS}/manager-cluster.kind.yaml" "$MANAGER_KUBECONFIG" &
    cluster_create "$WORKER1_KIND_CLUSTER_NAME" "$SOURCE_DIR/worker-cluster.kind.yaml" "$WORKER1_KUBECONFIG" &
    cluster_create "$WORKER2_KIND_CLUSTER_NAME" "$SOURCE_DIR/worker-cluster.kind.yaml" "$WORKER2_KUBECONFIG" &
}

function wait_for_kueue_ready() {
    local kubeconfig=$1
    local cluster_name=$2
    
    echo "Waiting for Kueue to be ready in $cluster_name cluster..."
    
    # Wait for Kueue controller pods to be ready
    echo "  Waiting for Kueue controller pods in $cluster_name..."
    kubectl --kubeconfig="$kubeconfig" wait --for=condition=ready pod \
        -l control-plane=controller-manager \
        -n kueue-system \
        --timeout=300s || {
        echo "  Warning: Kueue controller pods not ready in $cluster_name, checking status..."
        kubectl --kubeconfig="$kubeconfig" get pods -n kueue-system || true
    }
    
    # Check if webhook service exists
    echo "  Checking webhook service availability..."
    if kubectl --kubeconfig="$kubeconfig" get service kueue-webhook-service -n kueue-system >/dev/null 2>&1; then
        echo "  ✓ Webhook service found in $cluster_name"
    else
        echo "  Warning: Webhook service not found in $cluster_name"
        kubectl --kubeconfig="$kubeconfig" get services -n kueue-system || true
    fi
    
    # Check if Kueue CRDs are registered
    echo "  Verifying Kueue CRDs are registered..."
    if kubectl --kubeconfig="$kubeconfig" get crd clusterqueues.kueue.x-k8s.io >/dev/null 2>&1; then
        echo "  ✓ Kueue CRDs are registered in $cluster_name"
    else
        echo "  Warning: Kueue CRDs not found in $cluster_name"
        kubectl --kubeconfig="$kubeconfig" get crd | grep -i kueue || echo "  No Kueue CRDs found"
    fi
    
    echo "  Kueue readiness check completed for $cluster_name cluster."
}

function deploy_kueue_to_clusters() {
    # We use a subshell to avoid overwriting the global cleanup trap, which also uses the EXIT signal.
    (
        set_managers_image
        trap restore_managers_image EXIT
        
        # Deploy to all clusters in parallel first
        echo "Deploying Kueue to all clusters in parallel..."
        echo "  Starting deployment to manager cluster..."
        cluster_kueue_deploy "$MANAGER_KUBECONFIG" &
        echo "  Starting deployment to worker1 cluster..."
        cluster_kueue_deploy "$WORKER1_KUBECONFIG" &
        echo "  Starting deployment to worker2 cluster..."
        cluster_kueue_deploy "$WORKER2_KUBECONFIG" &
        
        # Wait for all deployments to complete
        echo "Waiting for all Kueue deployments to complete..."
        wait
        
        # Now check readiness for all clusters
        echo "Checking Kueue readiness on all clusters..."
        wait_for_kueue_ready "$MANAGER_KUBECONFIG" "manager"
        wait_for_kueue_ready "$WORKER1_KUBECONFIG" "worker1"
        wait_for_kueue_ready "$WORKER2_KUBECONFIG" "worker2"
        
        echo "Kueue deployment completed on all clusters."
    )
}

# =============================================================================
# MULTIKUEUE SETUP FUNCTIONS
# =============================================================================

function validate_kubeconfig_script() {
    local script_path="${SOURCE_DIR}/create-multikueue-kubeconfig.sh"
    
    if [ ! -f "$script_path" ]; then
        echo "Error: create-multikueue-kubeconfig.sh not found at $script_path"
        exit 1
    fi
    
    if [ ! -x "$script_path" ]; then
        echo "Making create-multikueue-kubeconfig.sh executable..."
        chmod +x "$script_path"
    fi
}

function generate_worker_kubeconfig() {
    local cluster_name=$1
    local kubeconfig_path=$2
    local output_name=$3
    
    echo "Generating kubeconfig for cluster: $cluster_name"
    
    # Switch to the worker cluster context
    export KUBECONFIG="$kubeconfig_path"
    
    # Check if kueue-system namespace exists
    echo "Checking for kueue-system namespace in $cluster_name..."
    kubectl get namespace kueue-system >/dev/null 2>&1 || {
        echo "Warning: kueue-system namespace not found in $cluster_name, continuing anyway..."
    }
    
    # Generate the kubeconfig using the create-multikueue-kubeconfig.sh script
    "${SOURCE_DIR}/create-multikueue-kubeconfig.sh" "${ARTIFACTS}/${output_name}" || {
        echo "Error: Failed to generate kubeconfig for $cluster_name"
        return 1
    }
    
    echo "Successfully generated kubeconfig: ${ARTIFACTS}/${output_name}"
}

function generate_all_worker_kubeconfigs() {
    echo "Generating MultiKueue kubeconfigs for worker clusters..."
    
    validate_kubeconfig_script
    
    # Generate kubeconfigs for both worker clusters in parallel
    generate_worker_kubeconfig "$WORKER1_KIND_CLUSTER_NAME" "$WORKER1_KUBECONFIG" "worker1-multikueue-kubeconfig" &
    generate_worker_kubeconfig "$WORKER2_KIND_CLUSTER_NAME" "$WORKER2_KUBECONFIG" "worker2-multikueue-kubeconfig" &
    
    # Wait for both kubeconfig generations to complete
    wait
}

function create_worker_secret() {
    local worker_name=$1
    local kubeconfig_file="${ARTIFACTS}/${worker_name}-multikueue-kubeconfig"
    local secret_name="${worker_name}-secret"
    
    if [ ! -f "$kubeconfig_file" ]; then
        echo "Error: $kubeconfig_file not found"
        return 1
    fi
    
    echo "Creating secret for ${worker_name} cluster..."
    kubectl create secret generic "$secret_name" \
        -n kueue-system \
        --from-file=kubeconfig="$kubeconfig_file" \
        --dry-run=client -o yaml | kubectl apply -f - || {
        echo "Error: Failed to create $secret_name"
        return 1
    }
    echo "Successfully created $secret_name in kueue-system namespace"
}

function create_all_worker_secrets() {
    echo "Creating secrets in manager cluster for worker clusters..."
    
    # Switch to manager cluster context
    export KUBECONFIG="$MANAGER_KUBECONFIG"
    
    # Ensure kueue-system namespace exists in manager cluster
    kubectl create namespace kueue-system --dry-run=client -o yaml | kubectl apply -f - || {
        echo "Warning: Could not create/verify kueue-system namespace in manager cluster"
    }
    
    # Create secrets for each worker cluster
    create_worker_secret "worker1" || return 1
    create_worker_secret "worker2" || return 1
    
    echo "All worker secrets created successfully!"
}

function apply_multikueue_config() {
    echo "Applying MultiKueue configuration to manager cluster..."
    
    # Switch to manager cluster context
    export KUBECONFIG="$MANAGER_KUBECONFIG"
    
    # Apply the MultiKueue configuration
    kubectl apply -f "${SOURCE_DIR}/multikueue-config.yaml" || {
        echo "Error: Failed to apply MultiKueue configuration"
        return 1
    }
    
    echo "MultiKueue configuration applied successfully!"
}

function setup_multikueue() {
    generate_all_worker_kubeconfigs
    create_all_worker_secrets
    apply_multikueue_config
}

function print_completion_summary() {
    echo "MultiKueue setup complete!"
    echo "Generated kubeconfigs:"
    echo "  - Worker1: ${ARTIFACTS}/worker1-multikueue-kubeconfig"
    echo "  - Worker2: ${ARTIFACTS}/worker2-multikueue-kubeconfig"
    echo ""
    echo "Created secrets in manager cluster (kueue-system namespace):"
    echo "  - worker1-secret (contains worker1 kubeconfig)"
    echo "  - worker2-secret (contains worker2 kubeconfig)"
    echo ""
    echo "Applied MultiKueue configuration to manager cluster:"
    echo "  - AdmissionCheck: sample-multikueue"
    echo "  - MultiKueueConfig: multikueue-test (with worker1 and worker2)"
    echo "  - MultiKueueCluster: multikueue-test-worker1"
    echo "  - MultiKueueCluster: multikueue-test-worker2"
    echo ""
    echo "The MultiKueue setup is now ready for testing workload delegation across clusters."
    
    read -rp "Press Enter to cleanup."
}

# =============================================================================
# MAIN EXECUTION
# =============================================================================

print_environment_info

# Set up cleanup trap
trap cleanup EXIT
cleanup_stale_files

# Ensure external CRDs are available
echo "Setting up external CRDs..."

# Set up clusters and infrastructure
startup
prepare_docker_images
wait # for clusters creation

# Load images to clusters in parallel
kind_load "$MANAGER_KIND_CLUSTER_NAME" "$MANAGER_KUBECONFIG" &
kind_load "$WORKER1_KIND_CLUSTER_NAME" "$WORKER1_KUBECONFIG" &
kind_load "$WORKER2_KIND_CLUSTER_NAME" "$WORKER2_KUBECONFIG" &
wait # for libraries installation

# Deploy Kueue to all clusters
deploy_kueue_to_clusters

# Set up MultiKueue configuration
setup_multikueue

# Restore the merged kubeconfig for tests
export KUBECONFIG="$MANAGER_KUBECONFIG:$WORKER1_KUBECONFIG:$WORKER2_KUBECONFIG"

print_completion_summary