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
  restore_managers_image
}

# Set trap to clean up on exit
trap cleanup EXIT

cluster_create "${KIND_CLUSTER_NAME}" "$SOURCE_DIR/$KIND_CLUSTER_FILE" ""
echo Waiting for kind cluster "${KIND_CLUSTER_NAME}" to start...
prepare_docker_images
cluster_kind_load "${KIND_CLUSTER_NAME}"
(cd config/components/manager && $KUSTOMIZE edit set image controller="$IMAGE_TAG")
cluster_kueue_deploy ""
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
# Run Cypress tests for kueueviz frontend
npm run cypress:run
cd -

# The trap will handle cleanup 
