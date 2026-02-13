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

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT_DIR=$(realpath "$SCRIPT_DIR/../../../..")
KIND_CLUSTER_NAME="kueue-populator-verify-helm"
GIT_TAG=$(git describe --tags --dirty --always)

# Use tools from the root project
KIND="$ROOT_DIR/bin/kind"
HELM="$ROOT_DIR/bin/helm"

# Ensure tools are built
echo "Ensuring tools are available..."
cd "$ROOT_DIR"
# Pass explicit versions to bypass broken 'go list' in hack/tools
make kind helm KIND_VERSION=v0.24.0 HELM_VERSION=v3.16.2
cd "$SCRIPT_DIR/.."

function cleanup {
  echo "Cleaning up Kind cluster..."
  "$KIND" delete cluster --name "$KIND_CLUSTER_NAME" 2>/dev/null || true
}
trap cleanup EXIT

echo "Creating Kind cluster..."
# Delete existing cluster if any
"$KIND" delete cluster --name "$KIND_CLUSTER_NAME" 2>/dev/null || true
"$KIND" create cluster --name "$KIND_CLUSTER_NAME"

echo "Building and loading kueue-populator image..."
make kind-image-build
IMAGE_TAG="us-central1-docker.pkg.dev/k8s-staging-images/kueue/kueue-populator:$GIT_TAG"
"$KIND" load docker-image "$IMAGE_TAG" --name "$KIND_CLUSTER_NAME"

echo "Building Helm dependencies..."
"$HELM" dependency build charts/kueue-populator

echo "Installing kueue-populator with Topology and ResourceFlavor..."
"$HELM" upgrade --install kueue-populator charts/kueue-populator \
  --namespace kueue-system \
  --create-namespace \
  --set kueue.enabled=true  \
  --set image.tag="$GIT_TAG" \
  --set image.pullPolicy=IfNotPresent \
  --set kueuePopulator.config.topology.levels[0].nodeLabel="cloud.google.com/gke-nodepool" \
  --set kueuePopulator.config.resourceFlavor.nodeLabels."cloud\.google\.com/gke-nodepool"="default-pool" \
  --wait

echo "Running Helm tests..."
"$HELM" test kueue-populator --namespace kueue-system

echo "Verification passed!"
