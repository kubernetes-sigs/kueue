#!/bin/bash

# Copyright 2026 The Kubernetes Authors.
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

# This script performs an end-to-end integration test to verify that the KueueViz
# backend correctly enforces Kubernetes RBAC policies via SubjectAccessReviews.

set -e

SOURCE_DIR="$(cd "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT_DIR="$(cd "${SOURCE_DIR}/../.." && pwd -P)"

ARTIFACTS="${ARTIFACTS:-${ROOT_DIR}/_artifacts}"
KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-kind}"
KIND_CLUSTER_FILE="${KIND_CLUSTER_FILE:-kind-cluster.yaml}"

# shellcheck source=hack/testing/e2e-common.sh
source "${SOURCE_DIR}/e2e-common.sh"

cleanup() {
  echo "Cleaning up kueueviz processes"
  [ -n "${BACKEND_PID:-}" ] && kill "${BACKEND_PID}"
  cluster_collect_artifacts "${KIND_CLUSTER_NAME}" ""
  cluster_cleanup "${KIND_CLUSTER_NAME}"
}

trap cleanup EXIT

echo Creating kind cluster "${KIND_CLUSTER_NAME}"
cluster_create "${KIND_CLUSTER_NAME}" "$SOURCE_DIR/$KIND_CLUSTER_FILE" ""
echo Waiting for kind cluster "${KIND_CLUSTER_NAME}" to start...
prepare_docker_images
cluster_kind_load "${KIND_CLUSTER_NAME}"
kueue_deploy

VICTIM_NS="kueueviz-rbac-victim"
ATTACKER_NS="kueueviz-rbac-attacker"
ATTACKER_SA="low-priv"
ATTACKER_USER="system:serviceaccount:${ATTACKER_NS}:${ATTACKER_SA}"
VICTIM_POD="victim-secret-pod"
SECRET_VALUE="super-secret-cross-tenant-value"
KUEUEVIZ_RBAC_PORT="${KUEUEVIZ_RBAC_PORT:-8081}"
KUEUEVIZ_URL="http://localhost:${KUEUEVIZ_RBAC_PORT}"

BACKEND_PID=""

fail_setup() {
  echo "SETUP ERROR: $*" >&2
  exit 2
}

echo "== KueueViz RBAC-bypass reproduction =="

cd "${ROOT_DIR}/cmd/kueueviz/backend"
go build -o bin/kueueviz .
KUEUEVIZ_PORT="${KUEUEVIZ_RBAC_PORT}" KUEUEVIZ_AUTH_MODE="TokenReview" ./bin/kueueviz & BACKEND_PID=$!
cd -

echo "Waiting for backend /healthz on ${KUEUEVIZ_URL}..."
for _ in $(seq 1 60); do
  if curl -sS -o /dev/null "${KUEUEVIZ_URL}/healthz" 2>/dev/null; then
    break
  fi
  kill -0 "${BACKEND_PID}" 2>/dev/null || fail_setup "backend process exited before becoming ready"
  sleep 1
done
curl -sS -o /dev/null "${KUEUEVIZ_URL}/healthz" || fail_setup "backend did not become ready in time"

# Step 1: Create a "victim" namespace with a Pod containing a secret environment variable.
# We will use this to confirm whether an unauthorized user can read other tenants' data.
echo "Creating victim namespace ${VICTIM_NS} with a Pod carrying a secret env var..."
kubectl create namespace "${VICTIM_NS}"
kubectl apply -n "${VICTIM_NS}" -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: ${VICTIM_POD}
spec:
  containers:
  - name: app
    image: registry.k8s.io/pause:3.9
    env:
    - name: TENANT_SECRET
      value: ${SECRET_VALUE}
EOF

# Step 2: Create an "attacker" namespace with an unprivileged ServiceAccount.
# This ServiceAccount will not have any RoleBindings granting it access to the victim namespace.
echo "Creating attacker namespace ${ATTACKER_NS} with an unprivileged ServiceAccount (no RoleBindings)..."
kubectl create namespace "${ATTACKER_NS}"
kubectl create serviceaccount "${ATTACKER_SA}" -n "${ATTACKER_NS}"

echo
# Step 3: Verify the attacker ServiceAccount is denied direct API access to the victim Pod.
# This establishes the baseline: Kubernetes RBAC is working, and the attacker shouldn't
# be able to read the Pod. We test this both with 'auth can-i' and by making a direct request.
echo "== Confirming the API server itself denies ${ATTACKER_USER} =="
CAN_I="$(kubectl auth can-i get pods -n "${VICTIM_NS}" --as="${ATTACKER_USER}" 2>/dev/null || true)"
echo "kubectl auth can-i get pods -n ${VICTIM_NS} --as=${ATTACKER_USER} -> ${CAN_I}"
if [ "${CAN_I}" != "no" ]; then
  fail_setup "expected the API server to deny the caller (want 'no', got '${CAN_I}'); RBAC is not restrictive enough for a meaningful test"
fi

echo "Minting a token for ${ATTACKER_USER}..."
TOKEN="$(kubectl create token "${ATTACKER_SA}" -n "${ATTACKER_NS}")"
[ -n "${TOKEN}" ] || fail_setup "failed to mint a ServiceAccount token"

echo "Direct API read with the caller's own token should be Forbidden..."
APISERVER=$(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}')
if kubectl get pod "${VICTIM_POD}" -n "${VICTIM_NS}" --kubeconfig=/dev/null --server="${APISERVER}" --insecure-skip-tls-verify --token="${TOKEN}" >/dev/null 2>&1; then
  fail_setup "the API server unexpectedly allowed the low-priv token to read the pod directly"
fi
echo "Confirmed: the API server rejects the caller's direct read."

echo
# Step 4: Ask the KueueViz backend for the victim Pod using the attacker's token.
# Step 5: Assert the backend responds with HTTP 401/403 (Forbidden) and not HTTP 200.
# If the backend acts as a confused deputy, it would fetch the Pod using its own privileges
# and leak it to the attacker. The expected behavior is that it performs a SubjectAccessReview
# and returns an error.
echo "== Asking the KueueViz backend for the same resource, as the same caller =="
URL="${KUEUEVIZ_URL}/api/pod/${VICTIM_POD}?namespace=${VICTIM_NS}&output=yaml"
echo "GET ${URL}"
BODY_FILE="$(mktemp)"
HTTP_CODE="$(curl -sS -o "${BODY_FILE}" -w '%{http_code}' -H "Authorization: Bearer ${TOKEN}" "${URL}")"
BODY="$(cat "${BODY_FILE}")"
rm -f "${BODY_FILE}"
echo "Backend responded with HTTP ${HTTP_CODE}."

if [ "${HTTP_CODE}" = "200" ] && printf '%s' "${BODY}" | grep -q "${SECRET_VALUE}"; then
  echo
  echo "A caller the API server denies (${ATTACKER_USER}) read the Pod"
  echo "${VICTIM_NS}/${VICTIM_POD} — including its secret env value —"
  echo "through the KueueViz backend, which never authorized the caller."
  exit 1
fi

if [ "${HTTP_CODE}" != "401" ] && [ "${HTTP_CODE}" != "403" ]; then
  echo "UNEXPECTED: HTTP ${HTTP_CODE}, body:" >&2
  printf '%s\n' "${BODY}" >&2
  exit 1
fi

echo "SECURE: the backend denied the unauthorized caller (HTTP ${HTTP_CODE})."
