#!/bin/bash

set -o nounset
set -o pipefail
set -o xtrace

KUEUE_VERSION=v0.13.3
kubeconfigs=("manager-europe-west4-a" "worker-us-east4-a" "worker-asia-southeast1-a")
CUSTOM_INSTALL="${KUEUE_CUSTOM_INSTALL:-false}"

function uninstall_kueue() {
    if [[ "$CUSTOM_INSTALL" == "true" ]]; then
        pushd ../../.. > /dev/null
        make undeploy
        popd > /dev/null
    else
        kubectl delete --ignore-not-found -f "https://github.com/kubernetes-sigs/kueue/releases/download/${KUEUE_VERSION}/manifests.yaml"
    fi
}

for i in "${!kubeconfigs[@]}"; do
    config="${kubeconfigs[$i]}"
    kubectl config use-context "$config"
    uninstall_kueue
done

kubectl delete job --all
 