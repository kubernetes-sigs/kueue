#!/bin/bash

set -e

SOURCE_DIR="$(cd "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT_DIR="$SOURCE_DIR/.."
# shellcheck source=hack/e2e-common.sh
source "${SOURCE_DIR}/e2e-common.sh"

# Set CYPRESS_IMAGE_NAME to the value of the env var or default to cypress/base
CYPRESS_IMAGE_NAME="${CYPRESS_IMAGE_NAME:-cypress/base}"

# Function to clean up background processes
cleanup() {
  echo "Cleaning up kueue-viz processes"
  kill $BACKEND_PID $FRONTEND_PID
  cluster_cleanup "$KIND_CLUSTER_NAME"
}

# Set trap to clean up on exit
trap cleanup EXIT

cluster_create "${KIND_CLUSTER_NAME}"
echo Waiting for kind cluster "${KIND_CLUSTER_NAME}" to start...
cluster_kueue_deploy "${KIND_CLUSTER_NAME}"
kubectl wait deploy/kueue-controller-manager -nkueue-system --for=condition=available --timeout=5m

# Deploy kueue-viz resources
kubectl create -f "${ROOT_DIR}/cmd/experimental/kueue-viz/examples/"

# Start kueue-viz backend
cd "${ROOT_DIR}/cmd/experimental/kueue-viz/backend"
go build -o bin/kueue-viz
./bin/kueue-viz & BACKEND_PID=$!
cd -

# Start kueue-viz frontend
cd cmd/experimental/kueue-viz/frontend
npm start & FRONTEND_PID=$!

# Run Cypress tests for kueue-viz frontend
npm run cypress:run --headless

# The trap will handle cleanup 
