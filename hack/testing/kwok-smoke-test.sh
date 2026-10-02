#!/usr/bin/env bash

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

set -o errexit
set -o nounset
set -o pipefail
umask 077

# shellcheck source=hack/testing/kwok/config.sh
source "$(cd "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/kwok/config.sh"

# Smoke-specific configuration.
SMOKE_NAMESPACE="kueue-kwok-smoke"
SMOKE_POD_NAME="kueue-smoke"
KUEUE_SMOKE_RESOURCES="${KWOK_TEST_DIR}/smoke/resources.yaml"

# shellcheck source=hack/testing/kwok/common.sh
source "${KWOK_TEST_DIR}/common.sh"

# Smoke-specific test helpers.
function wait_for_workload {
    local deadline=$((SECONDS + 60))
    local workload_name=""

    while [[ -z "${workload_name}" ]]; do
        if ! kill -0 "${KUEUE_MANAGER_PID}" 2>/dev/null; then
            echo "Kueue manager exited before creating the Workload" >&2
            exit 1
        fi
        workload_name=$(cluster_kubectl get workloads \
            --namespace="${SMOKE_NAMESPACE}" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
        if (( SECONDS >= deadline )); then
            echo "Timed out waiting for Kueue to create the Workload" >&2
            exit 1
        fi
        [[ -n "${workload_name}" ]] || sleep 1
    done

    printf '%s' "${workload_name}"
}

function wait_for_pod_ungated {
    local deadline=$((SECONDS + 60))

    while cluster_kubectl get pod "${SMOKE_POD_NAME}" \
        --namespace="${SMOKE_NAMESPACE}" \
        -o jsonpath='{range .spec.schedulingGates[*]}{.name}{"\n"}{end}' | \
        grep -Fxq 'kueue.x-k8s.io/admission'; do
        if (( SECONDS >= deadline )); then
            echo "Timed out waiting for Kueue to remove the Pod scheduling gate" >&2
            exit 1
        fi
        sleep 1
    done
}

function run_smoke_test {
    local workload_name

    cluster_kubectl delete namespace "${SMOKE_NAMESPACE}" \
        --ignore-not-found --wait=true --timeout=60s
    cluster_kubectl apply -f "${KUEUE_SMOKE_RESOURCES}"

    workload_name="$(wait_for_workload)"
    cluster_kubectl wait "workload/${workload_name}" \
        --namespace="${SMOKE_NAMESPACE}" --for=condition=Admitted --timeout=60s
    wait_for_pod_ungated

    # KWOK reports Running through its simulated node; no container image is pulled or executed.
    cluster_kubectl wait pod "${SMOKE_POD_NAME}" \
        --namespace="${SMOKE_NAMESPACE}" --for=jsonpath='{.status.phase}'=Running --timeout=60s
    cluster_kubectl get "workload/${workload_name}" \
        --namespace="${SMOKE_NAMESPACE}" -o wide
    cluster_kubectl get pod "${SMOKE_POD_NAME}" \
        --namespace="${SMOKE_NAMESPACE}" -o wide
}

validate_configuration

KUEUE_RUNTIME_DIR="$(mktemp -d "${TMPDIR:-/tmp}/kueue-kwok-smoke.XXXXXX")"
KUEUE_CERT_DIR="${KUEUE_RUNTIME_DIR}/certs"
KUEUE_MANAGER_CONFIG="${KUEUE_RUNTIME_DIR}/manager-config.yaml"
trap cleanup EXIT

mkdir -p "${ARTIFACTS}"
prepare_cluster
install_kueue_resources
start_kueue_manager
install_kueue_serving_certificate
run_smoke_test
