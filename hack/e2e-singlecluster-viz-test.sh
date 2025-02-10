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

SOURCE_DIR="$(cd "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT_DIR="$SOURCE_DIR/.."
E2E_CONFIG_PATH=${ROOT_DIR}/test/e2e/config/viz

# shellcheck source=hack/e2e-singlecluster-common.sh
source "${SOURCE_DIR}/e2e-singlecluster-common.sh"

TAG=${IMAGE_TAG##*:}

INITIAL_VIZ_FRONTEND_IMAGE=$($YQ '.images[] | select(.name == "controller") | [.newName, .newTag] | join(":")' config/components/manager/kustomization.yaml)

#$1 - cluster name
function install_ingress {
    kubectl apply -f https://kind.sigs.k8s.io/examples/ingress/deploy-ingress-nginx.yaml
}

function viz_cleanup() {
    cleanup
#    (cd config/components/manager && $KUSTOMIZE edit set image controller="$INITIAL_IMAGE")
}

function viz_kueue_deploy() {
#    (cd config/components/viz && $KUSTOMIZE edit set image controller="$IMAGE_TAG")
    kueue_deploy
}

trap viz_cleanup EXIT
startup

install_ingress

echo "Waiting for Ingress to be ready...."
kubectl wait --namespace ingress-nginx \
  --for=condition=ready pod \
  --selector=app.kubernetes.io/component=controller \
  --timeout=90s

kind_load
viz_kueue_deploy

echo "Waiting for Kueue to be ready..."
kubectl wait deploy/kueue-controller-manager -nkueue-system --for=condition=available --timeout=5m
echo "Waiting for Kueue Viz backend to be ready..."
kubectl wait deploy/kueue-viz-backend -nkueue-system --for=condition=available --timeout=90s
echo "Waiting for Kueue Viz frontend to be ready..."
kubectl wait deploy/kueue-viz-frontend -nkueue-system --for=condition=available --timeout=90s

npm run e2e-cy --prefix "${ROOT_DIR}/cmd/experimental/kueue-viz/frontend"