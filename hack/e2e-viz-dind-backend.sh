#!/bin/bash

set -e

SOURCE_DIR="$(cd "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT_DIR="$SOURCE_DIR/.."
source "${SOURCE_DIR}/e2e-common.sh"
echo ROOT_DIR=$ROOT_DIR SOURCE_DIR=$SOURCE_DIR KIND_CLUSTER_NAME=$KIND_CLUSTER_NAME


# Function to clean up background processes
cleanup() {
  echo "Cleaning up kueue-viz processes"
  kill $BACKEND_PID $FRONTEND_PID
  cluster_cleanup "$KIND_CLUSTER_NAME"
}

# Set trap to clean up on exit
trap cleanup EXIT

echo Creating kind cluster $KIND_CLUSTER_NAME
cluster_create "$KIND_CLUSTER_NAME"
echo Waiting for kind cluster $KIND_CLUSTER_NAME to start...
cluster_kueue_deploy "$KIND_CLUSTER_NAME"
kubectl wait deploy/kueue-controller-manager -nkueue-system --for=condition=available --timeout=5m

# Deploy kueue-viz resources
kubectl create -f $i "$ROOT_DIR/cmd/experimental/kueue-viz/examples/"

# Start kueue-viz backend
cd cmd/experimental/kueue-viz/backend
go build -o bin/kueue-viz
./bin/kueue-viz & BACKEND_PID=$!
cd -

# Start kueue-viz frontend and cypress in a container
CONTAINER_ID=$(docker ps --format json | jq -r 'select(.Image | contains("kubekins-e2e")) | .ID')
WORKSPACE_VOLUME=$(docker inspect $CONTAINER_ID | jq -r '.[] | .Mounts[] | select(.Destination=="/workspace") | .Source')
docker run -ti --entrypoint /workspace/hack/e2e-viz-dind-frontend.sh \
           -w /workspace --network host \
           -v $WORKSPACE_VOLUME:/workspace:rw \
           -v /var/run/docker.sock:/var/run/docker.sock cypress/base


# The trap will handle cleanup 
