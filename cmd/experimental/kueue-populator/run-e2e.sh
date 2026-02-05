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

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(realpath "$SCRIPT_DIR/../../..")
KIND_CLUSTER_NAME="kueue-populator-e2e"
KUEUE_VERSION="${KUEUE_VERSION:-v0.16.0}"
GIT_TAG=$(git describe --tags --dirty --always)

# Use tools from the root project
KUSTOMIZE="$REPO_ROOT/bin/kustomize"
GINKGO="$REPO_ROOT/bin/ginkgo"
KIND="$REPO_ROOT/bin/kind"

# Ensure tools are built
cd "$REPO_ROOT"
make kustomize ginkgo kind
cd "$SCRIPT_DIR"

function cleanup {
  "$KIND" delete cluster --name "$KIND_CLUSTER_NAME"
}
trap cleanup EXIT

echo "Creating Kind cluster..."
"$KIND" create cluster --name "$KIND_CLUSTER_NAME"

echo "Installing Kueue..."
kubectl apply --server-side -f "https://github.com/kubernetes-sigs/kueue/releases/download/${KUEUE_VERSION}/manifests.yaml"
kubectl wait deployment/kueue-controller-manager -n kueue-system --for=condition=available --timeout=5m
cd "$SCRIPT_DIR"

echo "Building and loading kueue-populator image..."
make kind-image-build
IMAGE_TAG="us-central1-docker.pkg.dev/k8s-staging-images/kueue/kueue-populator:$GIT_TAG"
"$KIND" load docker-image "$IMAGE_TAG" --name "$KIND_CLUSTER_NAME"

echo "Deploying kueue-populator..."
cd config
# Use the same tag as the Makefile
IMAGE_TAG="us-central1-docker.pkg.dev/k8s-staging-images/kueue/kueue-populator:$GIT_TAG"
"$KUSTOMIZE" edit set image us-central1-docker.pkg.dev/k8s-staging-images/kueue/kueue-populator="$IMAGE_TAG"
"$KUSTOMIZE" build . | kubectl apply --server-side -f -
cd ..

echo "Waiting for deployment to be ready..."
kubectl wait deployment/kueue-populator -n kueue-system --for=condition=available --timeout=2m

echo "Running E2E tests..."
"$GINKGO" -v test/e2e/...