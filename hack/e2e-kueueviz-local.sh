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
ROOT_DIR="$SOURCE_DIR/.."
# shellcheck source=hack/e2e-common.sh
source "${SOURCE_DIR}/e2e-common.sh"

# Function to clean up background processes
cleanup() {
  echo "Cleaning up kueueviz processes"
  kill $BACKEND_PID $FRONTEND_PID
  cluster_cleanup "$KIND_CLUSTER_NAME" ""
}

# Set trap to clean up on exit
trap cleanup EXIT

cluster_create "${KIND_CLUSTER_NAME}" "$SOURCE_DIR/$KIND_CLUSTER_FILE" ""
echo Waiting for kind cluster "${KIND_CLUSTER_NAME}" to start...
prepare_docker_images
cluster_kind_load "${KIND_CLUSTER_NAME}"
kueue_deploy
kubectl wait deploy/kueue-controller-manager -n"$KUEUE_NAMESPACE" --for=condition=available --timeout=5m

# Deploy kueueviz resources
kubectl create -f "${ROOT_DIR}/cmd/kueueviz/examples/"

# Start kueueviz backend
cd "${ROOT_DIR}/cmd/kueueviz/backend"
go build -o bin/kueueviz
./bin/kueueviz & BACKEND_PID=$!
cd -

# Start kueueviz frontend
cd "${ROOT_DIR}/cmd/kueueviz/frontend"
npm install
npm run dev & FRONTEND_PID=$!
cd -

cd "${ROOT_DIR}/test/e2e/kueueviz/"
npm install

if [ "$E2E_RUN_ONLY_ENV" = "true" ]; then
  read -rp "Do you want to cleanup? [Y/n] " reply
  if [[ "$reply" =~ ^[nN]$ ]]; then
    trap - EXIT
    echo "Skipping cleanup for backend, frontend, and kind cluster."
    echo -e "\nBackend cleanup:\n  kill $BACKEND_PID"
    echo -e "\nFrontend cleanup:\n  kill $FRONTEND_PID"
    echo -e "\nKind cluster cleanup:\n  kind delete cluster --name $KIND_CLUSTER_NAME"
  fi
  exit 0
fi

# Run Cypress tests for kueueviz frontend
npm run cypress:run
cd -

# The trap will handle cleanup 
