#!/bin/bash

set -o nounset
set -o pipefail
set -o errexit


FEATURE="$1"
MANAGER_SETUP="${FEATURE}-manager-setup.yaml"
WORKER_SETUP="${FEATURE}-worker-setup.yaml"
kubeconfigs=("manager-europe-west4" "worker-asia-southeast1" "worker-us-east4")

kubectl config use-context "${kubeconfigs[0]}"
kubectl apply -f "${MANAGER_SETUP}"

for i in "${!kubeconfigs[@]}"; do
  if [[ $i -ne 0 ]]; then
    kubectl config use-context "${kubeconfigs[$i]}"
    kubectl apply -f "${WORKER_SETUP}"
  fi
done

kubectl config use-context "${kubeconfigs[0]}"
