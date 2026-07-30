#!/usr/bin/env bash

# Copyright The Kubernetes Authors.
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

# Centralized-TAS spike driver (non-production). Stands up one manager and two
# worker kind clusters, deploys Kueue with the spike enabled on the manager,
# wires MultiKueue, submits a TAS Job to the manager, and reports where the
# manager placed it.
#
# Prereqs: a running Docker (e.g. `colima start`), kind, kubectl, and a locally
# built+loaded image (default kueue-local/kueue:dev, see `make kind-image-build
# IMAGE_REGISTRY=kueue-local GIT_TAG=dev`).

set -o errexit
set -o nounset
set -o pipefail

SOURCE_DIR="$(cd "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT_DIR="$SOURCE_DIR/../.."

PREFIX=${PREFIX:-spike}
IMG=${IMG:-kueue-local/kueue:dev}
KUEUE_NS=${KUEUE_NS:-kueue-system}
OVERLAY="$ROOT_DIR/test/e2e/config/multikueue/centralizedtas"
SPIKE_DIR="$ROOT_DIR/cmd/experimental/centralizedtas"
KUSTOMIZE="$ROOT_DIR/bin/kustomize"

MANAGER="$PREFIX-manager"
WORKER1="$PREFIX-worker1"
WORKER2="$PREFIX-worker2"

MGR_CTX="kind-$MANAGER"
W1_CTX="kind-$WORKER1"
W2_CTX="kind-$WORKER2"

echo "== Ensuring kind clusters =="
existing="$(kind get clusters 2>/dev/null || true)"

ensure_cluster() {
  local name=$1
  if grep -qx "$name" <<<"$existing"; then
    echo "  cluster $name already exists"
    return
  fi
  echo "  creating cluster $name"
  # Single-node clusters: kind removes the control-plane taint so pods schedule
  # on the node. Multi-node clusters are flaky on some colima/cgroup kernels.
  kind create cluster --name "$name" --wait 5m
}

ensure_cluster "$MANAGER"
ensure_cluster "$WORKER1"
ensure_cluster "$WORKER2"

echo "== Loading image $IMG into clusters =="
for c in "$MANAGER" "$WORKER1" "$WORKER2"; do
  kind load docker-image "$IMG" --name "$c"
done

echo "== Deploying Kueue (spike overlay) =="
deploy_kueue() {
  local ctx=$1
  "$KUSTOMIZE" build "$OVERLAY" \
    | sed -e "s#us-central1-docker.pkg.dev/k8s-staging-images/kueue/kueue:main#$IMG#g" \
          -e "s#imagePullPolicy: Always#imagePullPolicy: IfNotPresent#g" \
    | kubectl --context "$ctx" apply --server-side --force-conflicts -f -
  # The manager config lives in a fixed-name ConfigMap (no hash), so a config
  # change does not roll the Deployment on its own; force it to pick up config.
  kubectl --context "$ctx" -n "$KUEUE_NS" rollout restart deploy/kueue-controller-manager
  echo "  waiting for controller rollout on $ctx"
  kubectl --context "$ctx" -n "$KUEUE_NS" rollout status deploy/kueue-controller-manager --timeout=300s
}
deploy_kueue "$MGR_CTX"
deploy_kueue "$W1_CTX"
deploy_kueue "$W2_CTX"

# Give the webhooks a moment to have serving endpoints before applying CRs.
sleep 10

echo "== Creating MultiKueue worker kubeconfig secrets on the manager =="
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT
for w in "$WORKER1" "$WORKER2"; do
  kind get kubeconfig --internal --name "$w" > "$tmpdir/$w.kubeconfig"
done
kubectl --context "$MGR_CTX" -n "$KUEUE_NS" create secret generic worker1-secret \
  --from-file=kubeconfig="$tmpdir/$WORKER1.kubeconfig" --dry-run=client -o yaml \
  | kubectl --context "$MGR_CTX" apply -f -
kubectl --context "$MGR_CTX" -n "$KUEUE_NS" create secret generic worker2-secret \
  --from-file=kubeconfig="$tmpdir/$WORKER2.kubeconfig" --dry-run=client -o yaml \
  | kubectl --context "$MGR_CTX" apply -f -

echo "== Applying Kueue objects =="
kubectl --context "$W1_CTX" apply -f "$SPIKE_DIR/worker.yaml"
kubectl --context "$W2_CTX" apply -f "$SPIKE_DIR/worker.yaml"
kubectl --context "$MGR_CTX" apply -f "$SPIKE_DIR/manager.yaml"

echo "== Waiting for MultiKueueClusters to become Active =="
for i in $(seq 1 30); do
  if kubectl --context "$MGR_CTX" get multikueuecluster worker1 worker2 \
      -o jsonpath='{.items[*].status.conditions[?(@.type=="Active")].status}' 2>/dev/null \
      | grep -q "True True"; then
    echo "  both clusters Active"
    break
  fi
  sleep 5
done
kubectl --context "$MGR_CTX" get multikueuecluster -o wide || true

echo "== Submitting the demo Job to the manager =="
kubectl --context "$MGR_CTX" apply -f "$SPIKE_DIR/job.yaml"

echo "== Waiting for the manager workload to be admitted =="
for i in $(seq 1 30); do
  adm="$(kubectl --context "$MGR_CTX" get workload -l kueue.x-k8s.io/job-uid \
    -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.admission.clusterQueue}{"\n"}{end}' 2>/dev/null || true)"
  if [[ -n "$adm" ]]; then break; fi
  sleep 5
done

echo
echo "############### RESULT ###############"
echo "--- Manager workload admission (TopologyAssignment) ---"
kubectl --context "$MGR_CTX" get workload -o json \
  | python3 -c '
import json,sys
d=json.load(sys.stdin)
for w in d["items"]:
    adm=w.get("status",{}).get("admission")
    print("workload:", w["metadata"]["name"], "cluster:", w.get("status",{}).get("clusterName"))
    if adm:
        for psa in adm.get("podSetAssignments",[]):
            ta=psa.get("topologyAssignment")
            print("  podSet", psa["name"], "levels:", ta.get("levels") if ta else None)
            if ta:
                print("   slices:", json.dumps(ta.get("slices")))
' || kubectl --context "$MGR_CTX" get workload -o yaml

for ctx in "$W1_CTX" "$W2_CTX"; do
  echo "--- $ctx : workloads ---"
  kubectl --context "$ctx" get workload -A 2>/dev/null || true
  echo "--- $ctx : demo pods and their nodes ---"
  kubectl --context "$ctx" get pods -o wide -l job-name=centralized-tas-demo 2>/dev/null || true
done
echo "######################################"
echo
echo "To tear down: kind delete clusters $MANAGER $WORKER1 $WORKER2"
