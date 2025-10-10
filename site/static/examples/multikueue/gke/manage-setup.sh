#!/bin/bash

set -o nounset
set -o pipefail
set -o errexit

if [[ $# -lt 2 ]]; then
  echo "Usage: $0 <feature-name> <apply|delete>"
  exit 1
fi

FEATURE="$1"
COMMAND="$2"

if [[ "$COMMAND" != "apply" && "$COMMAND" != "delete" ]]; then
  echo "Error: command must be 'apply' or 'delete'"
  exit 1
fi

MANAGER_SETUP="${FEATURE}-manager-setup.yaml"
WORKER_SETUP="${FEATURE}-worker-setup.yaml"
kubeconfigs=("manager-europe-west4-a" "worker-us-east4-a" "worker-asia-southeast1-a")

kubectl config use-context "${kubeconfigs[0]}"
kubectl "$COMMAND" -f "${MANAGER_SETUP}"

for i in "${!kubeconfigs[@]}"; do
  if [[ $i -ne 0 ]]; then
    kubectl config use-context "${kubeconfigs[$i]}"
    kubectl "$COMMAND" -f "${WORKER_SETUP}"
  fi
done

kubectl config use-context "${kubeconfigs[0]}"
