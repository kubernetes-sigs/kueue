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

# This file is sourced by KWOK tests after they define their configuration and paths.

function cluster_exists {
    "${KWOKCTL}" get clusters | grep -Fxq "${KWOK_CLUSTER_NAME}"
}

function cluster_kubectl {
    "${KWOKCTL}" --name="${KWOK_CLUSTER_NAME}" kubectl "$@"
}

function delete_cluster {
    local delete_pid
    local elapsed

    "${KWOKCTL}" delete cluster --name="${KWOK_CLUSTER_NAME}" \
        --kubeconfig="${KWOK_KUBECONFIG}" &
    delete_pid=$!
    for ((elapsed = 0; elapsed < KWOK_DELETE_TIMEOUT_SECONDS; elapsed++)); do
        if ! kill -0 "${delete_pid}" 2>/dev/null; then
            wait "${delete_pid}"
            return
        fi
        sleep 1
    done

    echo "Timed out deleting KWOK cluster '${KWOK_CLUSTER_NAME}'" >&2
    kill "${delete_pid}" 2>/dev/null || true
    sleep 1
    kill -KILL "${delete_pid}" 2>/dev/null || true
    wait "${delete_pid}" 2>/dev/null || true
    return 1
}

function stop_kueue_manager {
    [[ -n "${KUEUE_MANAGER_PID}" ]] || return

    kill "${KUEUE_MANAGER_PID}" 2>/dev/null || true
    for _ in {1..10}; do
        kill -0 "${KUEUE_MANAGER_PID}" 2>/dev/null || break
        sleep 0.5
    done
    kill -KILL "${KUEUE_MANAGER_PID}" 2>/dev/null || true
    wait "${KUEUE_MANAGER_PID}" 2>/dev/null || true
}

function collect_diagnostics {
    cluster_kubectl get nodes,pods -A -o wide \
        > "${ARTIFACTS}/kwok-resources.log" 2>&1 || true
}

function cleanup_cluster {
    if [[ "${E2E_MODE}" == "ci" ]]; then
        if cluster_exists; then
            delete_cluster || true
        fi
        rm -f "${KWOK_KUBECONFIG}"
    elif cluster_exists; then
        echo "Keeping KWOK cluster '${KWOK_CLUSTER_NAME}' (E2E_MODE=${E2E_MODE})."
        echo "Kubeconfig: ${KWOK_KUBECONFIG}"
        echo "To delete it:"
        echo "  ${KWOKCTL} delete cluster --name=${KWOK_CLUSTER_NAME} --kubeconfig=${KWOK_KUBECONFIG}"
    fi
}

function remove_runtime_files {
    rm -f "${KUEUE_CERT_DIR}/tls.crt" "${KUEUE_CERT_DIR}/tls.key" \
        "${KUEUE_MANAGER_CONFIG}"
    rmdir "${KUEUE_CERT_DIR}" "${KUEUE_RUNTIME_DIR}" 2>/dev/null || true
}

function cleanup {
    local exit_code=$?

    # Cleanup is best-effort and must not replace the test's original exit status.
    trap - EXIT
    set +o errexit

    stop_kueue_manager
    collect_diagnostics
    cleanup_cluster
    remove_runtime_files

    exit "${exit_code}"
}

function validate_configuration {
    if [[ "${E2E_MODE}" != "ci" && "${E2E_MODE}" != "dev" ]]; then
        echo "Invalid E2E_MODE='${E2E_MODE}'. Supported values: ci|dev" >&2
        exit 2
    fi
}

function create_cluster {
    "${KWOKCTL}" create cluster --name="${KWOK_CLUSTER_NAME}" \
        --runtime="${KWOK_RUNTIME}" --kubeconfig="${KWOK_KUBECONFIG}" \
        --wait=2m --timeout="${KWOK_CLUSTER_TIMEOUT}"
}

function prepare_cluster {
    if cluster_exists; then
        if [[ "${E2E_MODE}" == "ci" ]]; then
            delete_cluster
            create_cluster
        else
            "${KWOKCTL}" start cluster --name="${KWOK_CLUSTER_NAME}" \
                --wait=2m --timeout="${KWOK_CLUSTER_TIMEOUT}"
            "${KWOKCTL}" get kubeconfig --name="${KWOK_CLUSTER_NAME}" > "${KWOK_KUBECONFIG}"
        fi
    else
        create_cluster
    fi

    "${KWOKCTL}" scale node --name="${KWOK_CLUSTER_NAME}" --replicas="${KWOK_NODE_COUNT}"
    cluster_kubectl wait node --all --for=condition=Ready --timeout=60s
}

function install_kueue_resources {
    local deadline=$((SECONDS + 60))

    cluster_kubectl apply --server-side \
        -f "${ROOT_DIR}/config/components/crd/_output/crds-with-webhooks.yaml"
    until [[ "$(cluster_kubectl get \
        crd/workloads.kueue.x-k8s.io \
        -o jsonpath='{.status.conditions[?(@.type=="Established")].status}' 2>/dev/null || true)" == "True" ]]; do
        if (( SECONDS >= deadline )); then
            echo "Timed out waiting for the Workload CRD to become established" >&2
            exit 1
        fi
        sleep 1
    done

    # The local manager needs the namespace and certificate objects, but no Deployment or Service.
    cluster_kubectl apply -f "${KUEUE_BOOTSTRAP_RESOURCES}"
}

function start_kueue_manager {
    mkdir -p "${KUEUE_CERT_DIR}"
    sed "s|@CERT_DIR@|${KUEUE_CERT_DIR}|" "${KUEUE_MANAGER_CONFIG_TEMPLATE}" \
        > "${KUEUE_MANAGER_CONFIG}"
    KUBECONFIG="${KWOK_KUBECONFIG}" "${KUEUE_MANAGER}" \
        --config="${KUEUE_MANAGER_CONFIG}" --zap-log-level=2 \
        > "${ARTIFACTS}/kueue-manager.log" 2>&1 &
    KUEUE_MANAGER_PID=$!
}

function decode_base64 {
    # GNU and BSD base64 use different flags for decoding.
    if base64 --decode </dev/null >/dev/null 2>&1; then
        base64 --decode
    else
        base64 -D
    fi
}

function install_kueue_serving_certificate {
    local deadline=$((SECONDS + 60))
    local encoded_cert=""
    local encoded_key=""

    while [[ -z "${encoded_cert}" || -z "${encoded_key}" ]]; do
        encoded_cert=$(cluster_kubectl get secret \
            --namespace=kueue-system kueue-webhook-server-cert \
            -o jsonpath='{.data.tls\.crt}' 2>/dev/null || true)
        encoded_key=$(cluster_kubectl get secret \
            --namespace=kueue-system kueue-webhook-server-cert \
            -o jsonpath='{.data.tls\.key}' 2>/dev/null || true)
        if (( SECONDS >= deadline )); then
            echo "Timed out waiting for the Kueue serving certificate" >&2
            exit 1
        fi
        [[ -n "${encoded_cert}" && -n "${encoded_key}" ]] || sleep 1
    done

    printf '%s' "${encoded_key}" | decode_base64 > "${KUEUE_CERT_DIR}/tls.key"
    printf '%s' "${encoded_cert}" | decode_base64 > "${KUEUE_CERT_DIR}/tls.crt"
    chmod 600 "${KUEUE_CERT_DIR}/tls.key" "${KUEUE_CERT_DIR}/tls.crt"
}
