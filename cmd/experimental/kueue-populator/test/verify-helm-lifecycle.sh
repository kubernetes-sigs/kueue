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

# E2E_KIND_VERSION is the kind node image (e.g. kindest/node:v1.35.0) and selects
# the Kubernetes version of the cluster. Like every other cluster-creating path in
# the repo, it is provided by the build system (the Makefile derives it from the
# K8s version). A fallback keeps direct execution working. See #13296 / #13291.
export E2E_KIND_VERSION="${E2E_KIND_VERSION:-kindest/node:v1.35.0}"
# ARTIFACTS is where the shared helpers write cluster logs.
export ARTIFACTS="${ARTIFACTS:-$ROOT_DIR/artifacts}"
mkdir -p "$ARTIFACTS"

# Build the tools the shared helpers need before sourcing them: e2e-common.sh
# reads config/components/manager/kustomization.yaml with yq at source time, so
# it must run from ROOT_DIR with yq and kustomize already present. Tool versions
# are resolved via the root Makefile (go list), consistent with the rest of the repo.
echo "Ensuring tools are available..."
cd "$ROOT_DIR"
make kind helm yq kustomize

# Reuse the common cluster lifecycle helpers so tooling versions and cluster
# creation stay consistent with the rest of the build system. e2e-common.sh
# resolves some paths relative to SOURCE_DIR (its own directory) and the current
# working directory (ROOT_DIR).
SOURCE_DIR="$ROOT_DIR/hack/testing"
# shellcheck source=/dev/null
source "$SOURCE_DIR/e2e-common.sh"

KIND_CONFIG="$SOURCE_DIR/kind-cluster.yaml"

cd "$SCRIPT_DIR/.."

function cleanup {
  echo "Cleaning up Kind cluster..."
  cluster_cleanup "$KIND_CLUSTER_NAME"
}
trap cleanup EXIT

echo "Creating Kind cluster (node image: $E2E_KIND_VERSION)..."
ensure_kind_cluster "$KIND_CLUSTER_NAME" "$KIND_CONFIG" ""

echo "Building and loading kueue-populator image..."
make kind-image-build
IMAGE_TAG="us-central1-docker.pkg.dev/k8s-staging-images/kueue/kueue-populator:$GIT_TAG"
"$KIND" load docker-image "$IMAGE_TAG" --name "$KIND_CLUSTER_NAME"

echo "Building Helm dependencies..."
"$HELM" dependency build charts/kueue-populator

echo "Installing kueue-populator with Topology, ResourceFlavor and ClusterQueue..."
"$HELM" upgrade --install kueue-populator charts/kueue-populator \
  --namespace kueue-system \
  --create-namespace \
  --set kueue.enabled=true  \
  --set image.tag="$GIT_TAG" \
  --set image.pullPolicy=IfNotPresent \
  --set kueuePopulator.config.topology.levels[0].nodeLabel="cloud.google.com/gke-nodepool" \
  --set kueuePopulator.config.resourceFlavor.nodeLabels."cloud\.google\.com/gke-nodepool"="default-pool" \
  --set kueuePopulator.config.clusterQueue.name="cluster-queue" \
  --set kueuePopulator.config.clusterQueue.resources[0].name="cpu" \
  --set kueuePopulator.config.clusterQueue.resources[0].nominalQuota=10 \
  --wait

echo "Running Helm tests..."
"$HELM" test kueue-populator --namespace kueue-system

# The post-install Job creates the cluster-scoped Kueue resources from the chart's
# ConfigMap (see charts/kueue-populator/templates/setup-hook.yaml).
echo "Verifying the Kueue resources were created..."
kubectl get topology default
kubectl get resourceflavor tas-gpu-default
kubectl get clusterqueue cluster-queue

echo "Uninstalling kueue-populator (triggers the pre-delete cleanup hook)..."
"$HELM" uninstall kueue-populator --namespace kueue-system

# The pre-delete Job must delete the resources created above so they do not leak
# on `helm uninstall`. helm waits for the hook Job to complete, so the objects
# should already be gone once uninstall returns.
echo "Verifying the Kueue resources were cleaned up by the pre-delete hook..."
if kubectl get topology default >/dev/null 2>&1; then
  echo "ERROR: Topology \"default\" still exists after helm uninstall" >&2
  exit 1
fi
if kubectl get resourceflavor tas-gpu-default >/dev/null 2>&1; then
  echo "ERROR: ResourceFlavor \"tas-gpu-default\" still exists after helm uninstall" >&2
  exit 1
fi
if kubectl get clusterqueue cluster-queue >/dev/null 2>&1; then
  echo "ERROR: ClusterQueue \"cluster-queue\" still exists after helm uninstall" >&2
  exit 1
fi

echo "Verification passed!"
