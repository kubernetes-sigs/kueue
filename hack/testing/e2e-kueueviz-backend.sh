#!/bin/bash

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

set -e

SOURCE_DIR="$(cd "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT_DIR="$SOURCE_DIR/../.."
# shellcheck source=hack/testing/e2e-common.sh
source "${SOURCE_DIR}/e2e-common.sh"
echo ROOT_DIR="${ROOT_DIR}" SOURCE_DIR="${SOURCE_DIR}" KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME}"

# if CI is true and PROW_JOB_ID is set, set NO_COLOR to 1
if [ -n "${CI}" ] && [ -n "${PROW_JOB_ID}" ]; then
  export NO_COLOR=1
  echo "Running in prow: Disabling color to have readable logs: NO_COLOR=${NO_COLOR}"
fi

# Function to clean up background processes
cleanup() {
  echo "Cleaning up kueueviz processes"
  kill "${BACKEND_PID}"
  cluster_collect_artifacts "${KIND_CLUSTER_NAME}" ""
  cluster_cleanup "${KIND_CLUSTER_NAME}"
}

# Set trap to clean up on exit
trap cleanup EXIT

echo Creating kind cluster "${KIND_CLUSTER_NAME}"
cluster_create "${KIND_CLUSTER_NAME}" "$SOURCE_DIR/$KIND_CLUSTER_FILE" ""
echo Waiting for kind cluster "${KIND_CLUSTER_NAME}" to start...
prepare_docker_images
cluster_kind_load "${KIND_CLUSTER_NAME}"
kueue_deploy
kubectl wait deploy/kueue-controller-manager -n"$KUEUE_NAMESPACE" --for=condition=available --timeout=5m

# Deploy KueueViz resources
kubectl create -f "${ROOT_DIR}/cmd/kueueviz/examples/"

# Start KueueViz backend
cd "${ROOT_DIR}/cmd/kueueviz/backend"
go build -o bin/kueueviz
./bin/kueueviz & BACKEND_PID=$!
cd -

set -x  # Enable debug mode

# Debugging: Print the container ID
CONTAINER_ID=$(docker ps --format json | jq -r 'select(.Image | contains("kubekins-e2e")) | .ID')
echo "Container ID: $CONTAINER_ID"

# Check if the container is running
if [ -n "$CONTAINER_ID" ]; then
  echo "Running container found for image: $CONTAINER_ID"
  # Extract the workspace volume
  WORKSPACE_VOLUME=$(docker inspect "$CONTAINER_ID" | jq -r '.[] | .Mounts[] | select(.Destination=="/workspace") | .Source')
fi

# Check if the workspace volume is empty
if [ -z "$WORKSPACE_VOLUME" ]; then
  WORKSPACE_VOLUME="${ROOT_DIR}"
fi
echo "Workspace Volume: $WORKSPACE_VOLUME"

# if CYPRESS_IMAGE_NAME is not set, extract it from ./hack/testing/cypress/Dockerfile
if [ -z "$CYPRESS_IMAGE_NAME" ]; then
  CYPRESS_IMAGE_NAME=$(grep '^FROM' "${ROOT_DIR}/hack/testing/cypress/Dockerfile" | awk '{print $2}')
fi

# Start KueueViz frontend and cypress in a container
echo "Current container information: CONTAINER_ID=${CONTAINER_ID} WORKSPACE_VOLUME=${WORKSPACE_VOLUME}"
docker run -i --entrypoint /workspace/hack/testing/e2e-kueueviz-frontend.sh \
           -e CYPRESS_SCREENSHOTS_FOLDER="${CYPRESS_SCREENSHOTS_FOLDER}" \
           -e CYPRESS_VIDEOS_FOLDER="${CYPRESS_VIDEOS_FOLDER}" \
           -e PROJECT_DIR="/workspace" -w /workspace --network host \
           -v "${WORKSPACE_VOLUME}":/workspace:rw \
           -v "${CYPRESS_SCREENSHOTS_FOLDER}":"${CYPRESS_SCREENSHOTS_FOLDER}" \
           -v "${CYPRESS_VIDEOS_FOLDER}":"${CYPRESS_VIDEOS_FOLDER}" \
           -v /var/run/docker.sock:/var/run/docker.sock "${CYPRESS_IMAGE_NAME}"

set +x  # Disable debug mode
# The trap will handle cleanup 
