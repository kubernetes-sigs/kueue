#!/usr/bin/env bash
# e2e-test-ocp.sh

set -o errexit
set -o nounset
set -o pipefail

export OC=$(which oc) # OpenShift CLI
SOURCE_DIR="$(cd "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT_DIR="$SOURCE_DIR/.."

# This is required to reuse the exisiting code.
# Set this to empty value for OCP tests.
export E2E_KIND_VERSION=""
# shellcheck source=hack/e2e-common-ocp.sh
source "${SOURCE_DIR}/e2e-common.sh"

# To label worker nodes for e2e tests.
function label_worker_nodes() {
    echo "Labeling two worker nodes for e2e tests..."
    # Retrieve the names of nodes with the "worker" role.
    local nodes=($($OC get nodes -l node-role.kubernetes.io/worker -o jsonpath='{.items[*].metadata.name}'))
    
    if [ ${#nodes[@]} -lt 2 ]; then
        echo "Error: Found less than 2 worker nodes. Cannot assign labels."
        exit 1
    fi
    
    # Label the first node as "on-demand"
    $OC label node "${nodes[0]}" instance-type=on-demand --overwrite
    # Label the second node as "spot"
    $OC label node "${nodes[1]}" instance-type=spot --overwrite
    echo "Labeled ${nodes[0]} as on-demand and ${nodes[1]} as spot."
}

# Wait until the cert-manager CRDs are installed.
function wait_for_cert_manager_crds() {
    echo "Waiting for cert-manager CRDs to be installed..."
    local timeout=120
    local interval=5
    local elapsed=0

    until $OC get crd certificates.cert-manager.io >/dev/null 2>&1; do
        if [ $elapsed -ge $timeout ]; then
            echo "Timeout waiting for cert-manager CRDs"
            exit 1
        fi
        sleep $interval
        elapsed=$((elapsed + interval))
    done
    echo "cert-manager CRDs are installed."
}

# Wait until all cert-manager deployments are available.
function wait_for_cert_manager_ready() {
    echo "Waiting for cert-manager components to be ready..."
    local deployments=(cert-manager cert-manager-cainjector cert-manager-webhook)
    for dep in "${deployments[@]}"; do
        echo "Waiting for deployment '$dep'..."
        if ! $OC wait --for=condition=Available deployment/"$dep" -n cert-manager --timeout=300s; then
            echo "Timeout waiting for deployment '$dep' to become available."
            exit 1
        fi
    done
    echo "All cert-manager components are ready."
}

function wait_for_cert_manager_csv() {
    echo "Waiting for cert-manager Operator CSV to reach Succeeded status..."
    local timeout=300
    local interval=10
    local elapsed=0
    local csv_namespace="cert-manager-operator"
    while true; do
        local status
        status=$($OC get csv -n "$csv_namespace" -o jsonpath='{.items[0].status.phase}' 2>/dev/null || echo "NotFound")
        if [ "$status" = "Succeeded" ]; then
            echo "cert-manager Operator CSV is Succeeded."
            break
        fi
        if [ $elapsed -ge $timeout ]; then
            echo "Timeout waiting for cert-manager Operator CSV to succeed."
            exit 1
        fi
        sleep $interval
        elapsed=$((elapsed + interval))
    done
}

function collect_logs {
    if [ ! -d "$ARTIFACTS" ]; then
        mkdir -p "$ARTIFACTS"
    fi
    $OC describe pods -n kueue-system > "$ARTIFACTS/kueue-system-pods.log" || true
    $OC logs -n kueue-system -l app=kueue --tail=-1 > "$ARTIFACTS/kueue-system-logs.log" || true
    restore_ocp_manager_image
}

function deploy_kueue {
    # Update kueue image
    (cd config/components/manager-ocp && $KUSTOMIZE edit set image controller="$IMAGE_TAG")
    
    # Deploy kueue
    $OC apply --server-side -k config/default-ocp
}

function restore_ocp_manager_image {
    (cd config/components/manager-ocp && $KUSTOMIZE edit set image controller="$INITIAL_IMAGE")
}

function deploy_cert_manager {
    echo "Deploying cert-manager..."
    $OC apply -f config/default-ocp/cert_manager_rh.yaml
    wait_for_cert_manager_crds
    wait_for_cert_manager_csv
    wait_for_cert_manager_ready
}

trap collect_logs EXIT

deploy_cert_manager
deploy_kueue

# Label two worker nodes for e2e tests (similar to the Kind setup).
label_worker_nodes

# Skip e2e tests that either are dependent on the pod integration feature 
# like Deployment, StatefulSet, etc. or other integrations that are not
# supported in OCP. 
$GINKGO $GINKGO_ARGS \
  --skip="(AppWrapper|JobSet|LeaderWorkerSet|Pod|Deployment|StatefulSet)" \
  --junit-report=junit.xml \
  --json-report=e2e.json \
  --output-dir="$ARTIFACTS" \
  -v ./test/e2e/$E2E_TARGET_FOLDER/...
