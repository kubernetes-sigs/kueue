#!/bin/bash

set -e
set -x

dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)

# Create cluster
kind create cluster --config $dir/kind-config.yaml

# Install Kueue
kubectl apply -f https://github.com/kubernetes-sigs/kueue/releases/download/v0.4.1/manifests.yaml
kubectl rollout status deployment -n kueue-system kueue-controller-manager

# Configure Kueue
kubectl apply -f $dir/queue.yaml

kubectl apply -f $dir/priorities.yaml

# Taint Nodes
# pool: a
kubectl taint nodes kind-worker2 tier=spot:NoSchedule
kubectl taint nodes kind-worker2 company.com/kueue-admission:NoSchedule

# pool: b
kubectl taint nodes kind-worker3 tier=regular:NoSchedule
kubectl taint nodes kind-worker3 company.com/kueue-admission:NoSchedule
