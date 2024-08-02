#!/bin/bash

if [[ ! -v CL2_HOME_DIR ]]; then
    echo "please set CL2_HOME_DIR"
    exit 1
fi

PROVIDER=${PROVIDER:="gke"}
KUBECONFIG=${KUBECONFIG:="$HOME/.kube/config"}
export KUBECONFIG

kubectl apply -f templates/resource-flavor.yaml

"$CL2_HOME_DIR/clusterloader" \
    --testconfig=test-config.yaml \
    --provider="$PROVIDER" \
    --v=2

# as clusterqueues and resource flavors are non-namespaced, we clean up here.
kubectl delete clusterqueue -l "group=pod-performance-cluster-queue"
kubectl delete -f templates/resource-flavor.yaml
