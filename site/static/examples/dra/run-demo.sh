#!/usr/bin/env bash
# Demonstration script for Kueue DRA GPU quota example.
# 1. Apply setup resources (flavor, DRA config, quota, queues, claim template).
# 2. Create a Job that fits within the quota and should be admitted.
# 3. Wait until the user deletes that Job.
# 4. Create a Job that exceeds quota and should remain suspended.
# 5. Wait until the user deletes that Job.
# 6. Exit.

set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

function wait_until_deleted() {
  local name=$1
  echo "Waiting for Job $name to be deleted (Ctrl-C to abort)…"
  while kubectl get job "$name" -n default &>/dev/null; do
    sleep 2
  done
}

echo "Applying GPU quota setup…"
kubectl apply -f "$DIR/gpu-quota-setup.yaml"

echo "Creating Job that fits into quota (gpu-job-admit)…"
kubectl apply -f "$DIR/gpu-job-admitted.yaml"

wait_until_deleted "gpu-job-admit"

echo "Creating Job that exceeds quota and should stay suspended (gpu-job)…"
kubectl apply -f "$DIR/gpu-job-suspended.yaml"

wait_until_deleted "gpu-job"

echo "Demo completed." 