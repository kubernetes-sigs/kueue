#!/usr/bin/env bash
# Copyright 2026 The Kubernetes Authors.
#
# SPDX-License-Identifier: Apache-2.0

# Patch an existing Kueue install so the controller exports OTLP traces to a Jaeger
# instance reachable from pods (e.g. hack/tracing/docker-compose.yaml on the host).
#
# Usage:
#   ./hack/tracing/patch-kueue-for-local-jaeger.sh
# Optional:
#   OTEL_EXPORTER_OTLP_ENDPOINT  default http://host.docker.internal:4317 (Docker Desktop + kind on macOS/Win)
#                                MUST include http:// — bare host:port breaks Go url.Parse for IPs; wrong scheme for DNS-only hosts.
#   KUEUE_NAMESPACE             default kueue-system

set -o errexit
set -o nounset
set -o pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
YQ="${ROOT_DIR}/bin/yq"
NS="${KUEUE_NAMESPACE:-kueue-system}"
DEPLOY="${KUEUE_DEPLOYMENT_NAME:-kueue-controller-manager}"
ENDPOINT="${OTEL_EXPORTER_OTLP_ENDPOINT:-http://host.docker.internal:4317}"

if [[ ! -x "${YQ}" ]]; then
  echo "missing ${YQ}; run: make yq" >&2
  exit 1
fi

tmp_cfg="$(mktemp)"
cleanup() { rm -f "${tmp_cfg}"; }
trap cleanup EXIT

kubectl get cm kueue-manager-config -n "${NS}" -o jsonpath='{.data.controller_manager_config\.yaml}' >"${tmp_cfg}"
"${YQ}" eval '.tracing = {"enable": true, "samplingRatio": 1.0}' -i "${tmp_cfg}"

kubectl create configmap kueue-manager-config -n "${NS}" \
  --from-file=controller_manager_config.yaml="${tmp_cfg}" \
  --dry-run=client -o yaml | kubectl apply -f -

kubectl set env "deployment/${DEPLOY}" -n "${NS}" \
  OTEL_EXPORTER_OTLP_ENDPOINT="${ENDPOINT}" \
  OTEL_EXPORTER_OTLP_INSECURE=true

kubectl rollout status "deployment/${DEPLOY}" -n "${NS}" --timeout=5m
echo "Kueue controller patched for OTLP -> ${ENDPOINT} (service name kueue in Jaeger UI)."
