#!/bin/bash
# Copyright The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#    http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# A/B test for the Kueue cold-restart instability.
#
# With ConcurrentWatchObjectDecode=false, the API server serialises its
# watch-cache rebuild. At large scale (e.g. 50,000 workloads), this serial
# conversion takes longer than the standard 5m etcd compaction interval.
# The cluster never stabilises: Kueue's informer gets trapped in an infinite relist loop.
#
# With ConcurrentWatchObjectDecode=true, the apiserver decodes objects in parallel.
# The parallel conversion finishes well within the compaction interval, 
# allowing Kueue to cleanly recover.
#
# Run A (gate=false): expect etcd compaction storm — Kueue stays broken.
# Run B (gate=true):  expect clean recovery within 60s.

set -euo pipefail

REPRO_DIR="$(cd "$(dirname -- "$0")" && pwd -P)"

# Defaults
KUEUE_VERSION="v0.15.0"
WORKLOAD_COUNT=50000
POPULATOR_MODE="local"
QPS=5
WORKERS=10
CONTAINERS=1
ENVS=0
GATE="both"
ETCD_INTERVAL="5m"
KUEUE_CPU=""
ENV_MODE="local"
CLEANUP="false"

while [[ $# -gt 0 ]]; do
  case $1 in
    --kueue-version) KUEUE_VERSION="$2"; shift 2 ;;
    --populator-mode) POPULATOR_MODE="$2"; shift 2 ;;
    --workloads) WORKLOAD_COUNT="$2"; shift 2 ;;
    --qps) QPS="$2"; shift 2 ;;
    --workers) WORKERS="$2"; shift 2 ;;
    --containers) CONTAINERS="$2"; shift 2 ;;
    --envs) ENVS="$2"; shift 2 ;;
    --gate) GATE="$2"; shift 2 ;;
    --etcd-interval) ETCD_INTERVAL="$2"; shift 2 ;;
    --kueue-cpu) KUEUE_CPU="$2"; shift 2 ;;
    --env) ENV_MODE="$2"; shift 2 ;;
    --cleanup) CLEANUP="$2"; shift 2 ;;
    -h|--help)
      echo "Usage: $0 [options]"
      echo "Options:"
      echo "  --env <local|cloud>        Execution environment (default: local)"
      echo "  --kueue-version <version>  (default: v0.15.0)"
      echo "  --populator-mode <mode>    local or cluster (default: local)"
      echo "  --workloads <count>        Number of workloads (default: 50000)"
      echo "  --qps <qps>                Populator QPS (default: 5)"
      echo "  --workers <count>          Populator workers (default: 10)"
      echo "  --containers <count>       Containers per workload (default: 1)"
      echo "  --envs <count>             Padded env vars per container (default: 0)"
      echo "  --gate <mode>              false, true, or both (default: both)"
      echo "  --etcd-interval <time>     Compaction interval (default: 5m)"
      echo "  --kueue-cpu <cpu>          Kueue CPU limit (e.g., 100m) (default: default)"
      echo "  --cleanup <true|false>     Delete the cluster after the run (default: false)"
      exit 0
      ;;
    *) echo "Unknown parameter passed: $1"; exit 1 ;;
  esac
done

if [[ "$GATE" != "true" && "$GATE" != "false" && "$GATE" != "both" ]]; then
  echo "Error: --gate must be 'true', 'false', or 'both'."
  exit 1
fi

echo "Configuration:"
echo "  WORKLOAD_COUNT: $WORKLOAD_COUNT"
echo "  KUEUE_VERSION: $KUEUE_VERSION"
echo "  POPULATOR_MODE: $POPULATOR_MODE"
echo "  QPS: $QPS"
echo "  WORKERS: $WORKERS"
echo "  CONTAINERS: $CONTAINERS"
echo "  ENVS: $ENVS"
echo "  GATE: $GATE"
echo "  ETCD_INTERVAL: $ETCD_INTERVAL"
echo "  KUEUE_CPU: ${KUEUE_CPU:-default}"
echo "  ENV_MODE: $ENV_MODE"
echo "  CLEANUP: $CLEANUP"

cleanup_all() {
  if [ "$ENV_MODE" = "local" ] && [ "$CLEANUP" = "true" ]; then
    echo "=== Cleaning up test clusters ==="
    kind delete cluster --name kueue-ab-false 2>/dev/null || docker rm -f kueue-ab-false-control-plane 2>/dev/null || true
    kind delete cluster --name kueue-ab-true 2>/dev/null || docker rm -f kueue-ab-true-control-plane 2>/dev/null || true
  fi
}
trap cleanup_all EXIT

run_ab() {
  local gate_value="$1"
  local CLUSTER="kueue-ab-${gate_value}"
  local LOG_PREFIX=""
  if [ "$gate_value" = "false" ]; then
    LOG_PREFIX="[ConcurrentWatchObjectDecode disabled] "
  elif [ "$gate_value" = "true" ]; then
    LOG_PREFIX="[ConcurrentWatchObjectDecode enabled] "
  fi

  echo ""
  echo "============================================================"
  echo "  RUN: ConcurrentWatchObjectDecode=${gate_value}"
  echo "============================================================"

  if [ "$ENV_MODE" = "local" ]; then
    echo "=== ${LOG_PREFIX}Step 1: Create kind cluster ==="
    kind delete cluster --name "$CLUSTER" 2>/dev/null || \
      docker rm -f "${CLUSTER}-control-plane" 2>/dev/null || true
    sleep 5
    kind create cluster --name "$CLUSTER" \
      --config - --wait 5m <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  kubeadmConfigPatches:
  - |
    kind: ClusterConfiguration
    apiServer:
      extraArgs:
        enable-aggregator-routing: "true"
        feature-gates: "ConcurrentWatchObjectDecode=${gate_value}"
        etcd-compaction-interval: "${ETCD_INTERVAL}"
    etcd:
      local:
        extraArgs:
          quota-backend-bytes: "8589934592"
EOF
    kubectl config use-context "kind-$CLUSTER"
  else
    echo "=== ${LOG_PREFIX}Step 1: Verify cloud cluster ==="
    echo "Cloud mode active. Skipping kind cluster creation."
  fi

  echo "=== ${LOG_PREFIX}Step 2: Install Kueue $KUEUE_VERSION ==="
  kubectl apply --server-side -f "https://github.com/kubernetes-sigs/kueue/releases/download/${KUEUE_VERSION}/manifests.yaml" >/dev/null
  if [ -n "$KUEUE_CPU" ]; then
    kubectl set resources deployment kueue-controller-manager -n kueue-system -c manager --limits=cpu="${KUEUE_CPU}",memory=8Gi --requests=cpu="${KUEUE_CPU}",memory=8Gi
  else
    kubectl set resources deployment kueue-controller-manager -n kueue-system -c manager --limits=memory=8Gi --requests=memory=8Gi
  fi
  kubectl wait --for=condition=available --timeout=300s deployment/kueue-controller-manager -n kueue-system >/dev/null

  echo "=== ${LOG_PREFIX}Step 2b: Create ResourceFlavor / ClusterQueue / LocalQueue ==="
  kubectl apply -f - <<'EOF' >/dev/null
apiVersion: kueue.x-k8s.io/v1beta1
kind: ResourceFlavor
metadata:
  name: default-flavor
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: cluster-queue
spec:
  namespaceSelector: {}
  resourceGroups:
  - coveredResources: 
    - "cpu"
    - "memory"
    flavors:
    - name: default-flavor
      resources:
      - name: cpu
        nominalQuota: "10000"
      - name: memory
        nominalQuota: "10Ti"
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: LocalQueue
metadata:
  name: local-queue
  namespace: default
spec:
  clusterQueue: cluster-queue
EOF

  if [ "$POPULATOR_MODE" = "cluster" ]; then
    echo "=== ${LOG_PREFIX}Step 3: Build + load populator image ==="
    if ! docker image inspect populator:local >/dev/null 2>&1; then
      docker build -t populator:local -f "$REPRO_DIR/Dockerfile" "$REPRO_DIR" >/dev/null
    else
      echo "Image populator:local already exists, skipping build."
    fi
    kind load docker-image populator:local --name "$CLUSTER"

    echo "=== ${LOG_PREFIX}Step 4: Populate $WORKLOAD_COUNT v1beta1 workloads (CLUSTER) ==="
    kubectl create clusterrolebinding populator-admin --clusterrole=cluster-admin \
      --serviceaccount=default:default 2>/dev/null || true
    kubectl delete job kueue-populator --ignore-not-found >/dev/null
    kubectl apply -f - <<EOF >/dev/null
apiVersion: batch/v1
kind: Job
metadata:
  name: kueue-populator
spec:
  backoffLimit: 1
  template:
    spec:
      serviceAccountName: default
      restartPolicy: Never
      containers:
      - name: populator
        image: populator:local
        imagePullPolicy: IfNotPresent
        args: 
        - "--count"
        - "$WORKLOAD_COUNT"
        - "--qps"
        - "$QPS"
        - "--workers"
        - "$WORKERS"
        - "--containers"
        - "$CONTAINERS"
        - "--envs"
        - "$ENVS"
        resources:
          requests:
            memory: "512Mi"
          limits:
            memory: "2Gi"
EOF

    kubectl wait --for=condition=ready pod -l job-name=kueue-populator --timeout=60s 2>/dev/null || true

    COND=""
    while true; do
      UNREACHABLE=0
      for _ in 1 2 3; do
        if kubectl get nodes --request-timeout=20s &>/dev/null; then UNREACHABLE=0; break; fi
        UNREACHABLE=1; sleep 30
      done
      [ "$UNREACHABLE" = "1" ] && { echo "cluster unreachable during populator — aborting"; return 1; }
      COND=$(kubectl get job kueue-populator --request-timeout=20s -o \
        jsonpath='{range .status.conditions[?(@.status=="True")]}{.type} {end}' 2>/dev/null || echo "")
      S=$(kubectl get job kueue-populator --request-timeout=20s -o jsonpath='{.status.succeeded}' 2>/dev/null || echo "0")
      F=$(kubectl get job kueue-populator --request-timeout=20s -o jsonpath='{.status.failed}'    2>/dev/null || echo "0")
      A=$(kubectl get job kueue-populator --request-timeout=20s -o jsonpath='{.status.active}'    2>/dev/null || echo "0")
      echo "[$(date +%H:%M:%S)] populator: succeeded=${S:-0} failed=${F:-0} active=${A:-0} cond='${COND}'"
      case "$COND" in
        *Complete*) break ;;
        *Failed*)   echo "populator failed — aborting"; return 1 ;;
      esac
      sleep 30
    done

    echo "--- populator logs ---"
    kubectl logs job/kueue-populator --request-timeout=60s 2>/dev/null | tail -3
    echo "--- end logs ---"
  else
    echo "=== ${LOG_PREFIX}Step 3: Build local populator binary ==="
    if [ ! -f "$REPRO_DIR/populator" ]; then
      (cd "$REPRO_DIR" && go build -o populator main.go)
    fi

    echo "=== ${LOG_PREFIX}Step 4: Populate $WORKLOAD_COUNT v1beta1 workloads (LOCAL) ==="
    "$REPRO_DIR/populator" --count "$WORKLOAD_COUNT" --qps "$QPS" --workers "$WORKERS" --containers "$CONTAINERS" --envs "$ENVS"
  fi

  set +eo pipefail
  WL_COUNT=$(kubectl get workloads -n default --no-headers --request-timeout=300s 2>/dev/null | wc -l | tr -d ' ')
  set -eo pipefail
  echo "etcd holds ${WL_COUNT:-?} workloads (via v1beta1)"
  if [ "${WL_COUNT:-0}" -lt 100 ] 2>/dev/null; then
    echo "Too few workloads in etcd — aborting (expected ~$WORKLOAD_COUNT)"
    return 1
  fi

  echo "=== ${LOG_PREFIX}Step 5: Restart kube-apiserver (cold cache) ==="
  if [ "$ENV_MODE" = "local" ]; then
    docker exec "${CLUSTER}-control-plane" pkill -9 kube-apiserver
  else
    read -r -p "Please manually restart your cloud kube-apiserver now. Press Enter when done..."
  fi
  T_KILL=$(date +%s)
  T_KILL_ISO=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

  echo "=== ${LOG_PREFIX}Step 6: Measure recovery via etcd compaction logs ==="
  RESULT=0

  if [ "$ENV_MODE" = "cloud" ]; then
    echo "In cloud mode, API server logs are generally not accessible via 'kubectl logs'."
    echo "Please manually verify your cloud provider's logging service for compaction storms."
    echo "Skipping automated log measurement."
    RESULT=1
  else
    # Wait for apiserver to restart (up to 180s), then watch for compaction storm.
    # With gate=false the serial conversion takes longer than the compaction
    # interval, so etcd compacts the revision mid-relist → Kueue retries → compacted
    # again → infinite loop. With gate=true the parallel worker pool finishes all
    # conversions well within the window → clean sync.
    echo "Waiting for apiserver to restart..."
    CAME_BACK=0
    for _ in $(seq 1 36); do
      if kubectl get nodes --request-timeout=10s &>/dev/null; then CAME_BACK=1; break; fi
      sleep 5
    done

    if [ $CAME_BACK -eq 0 ]; then
      echo ""
      echo "APISERVER DID NOT RESTART within 180s — hard failure (gate=${gate_value})"
    else
      BACK_T=$(( $(date +%s) - T_KILL ))
      echo "Apiserver back at +${BACK_T}s. Monitoring logs for compaction storm..."

      # Poll for Compaction Storm (up to 10 min)
      for _ in $(seq 0 10 600); do
        if ! kubectl get nodes --request-timeout=10s &>/dev/null; then
          echo "[+$(( $(date +%s) - T_KILL ))s] cluster unreachable"
          sleep 10; continue
        fi

        COMPACT=$(kubectl logs -n kube-system \
          "kube-apiserver-${CLUSTER}-control-plane" \
          --since-time="${T_KILL_ISO}" --request-timeout=20s 2>/dev/null | \
          grep -c "required revision has been compacted" 2>/dev/null || echo "0")
        COMPACT=${COMPACT//[!0-9]/}; COMPACT=${COMPACT:-0}

        AGE=$(( $(date +%s) - T_KILL ))
        echo "[+${AGE}s] etcd-compact-events-since-kill=${COMPACT}"

        if [ "${COMPACT}" -eq 0 ] && [ "${AGE}" -gt 60 ]; then
          echo ""
          echo "NO COMPACTION STORM after 60s — assuming clean recovery (gate=${gate_value})"
          RESULT=1; break
        fi

        if [ "${COMPACT}" -gt 3 ]; then
          echo ""
          echo "ETCD COMPACTION STORM (${COMPACT} total events since kill) — relist loop confirmed (gate=${gate_value})"
          break
        fi

        sleep 10
      done

      if [ $RESULT -eq 0 ]; then
        AGE=$(( $(date +%s) - T_KILL ))
        echo "Final state at +${AGE}s"
        echo "KUEUE INFORMERS FAILED TO SYNC — instability confirmed (gate=${gate_value})"
      fi
    fi
  fi

  if [ "$ENV_MODE" != "cloud" ]; then
    [ $RESULT -eq 0 ] \
      && echo "==> gate=${gate_value}: INSTABILITY CONFIRMED" \
      || echo "==> gate=${gate_value}: clean recovery"
  fi

  return $RESULT
}

# ---- main ----
if [ "$ENV_MODE" = "cloud" ]; then
  echo "Running in cloud mode. The API Server feature gate is managed by the cloud provider."
  run_ab "cloud"
  exit 0
fi

RESULT_FALSE=0
RESULT_TRUE=0

if [ "$GATE" = "both" ] || [ "$GATE" = "false" ]; then
  run_ab "false" || RESULT_FALSE=$?
fi

if [ "$GATE" = "both" ] || [ "$GATE" = "true" ]; then
  run_ab "true"  || RESULT_TRUE=$?
fi

echo ""
echo "========== SUMMARY =========="
if [ "$GATE" = "both" ] || [ "$GATE" = "false" ]; then
  echo "gate=false (instability expected): $([ $RESULT_FALSE -eq 0 ] && echo 'FAILED ✓' || echo 'unexpectedly recovered')"
fi
if [ "$GATE" = "both" ] || [ "$GATE" = "true" ]; then
  echo "gate=true  (recovery expected):    $([ $RESULT_TRUE  -ne 0 ] && echo 'RECOVERED ✓' || echo 'also failed — unexpected')"
fi
