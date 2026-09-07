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

test_dir=$(mktemp -d)
trap 'rm -rf "${test_dir}"' EXIT

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
export ROOT_DIR
export SOURCE_DIR="${ROOT_DIR}/hack/testing"
export GINKGO_ARGS="--label-filter=feature:trainjob"
export E2E_KIND_VERSION="kindest/node:v1.36.1"
export PATH="${test_dir}:$PATH"

cat >"${test_dir}/kubectl" <<'EOF'
#!/usr/bin/env bash
set -o errexit
set -o nounset
set -o pipefail

state_file="${KUBECTL_FAKE_STATE:?}"
printf '%s\n' "$*" >>"${KUBECTL_FAKE_LOG:?}"

case "$*" in
  *" wait deploy/kubeflow-trainer-controller-manager "*)
    exit 0
    ;;
  *" wait deploy/kueue-controller-manager "*)
    exit 0
    ;;
  *" get deployment kubeflow-trainer-controller-manager "*)
    printf '2\n'
    exit 0
    ;;
  *" get deployment kueue-controller-manager "*)
    printf '1\n'
    exit 0
    ;;
  *" get endpointslices "*" -o wide"*)
    printf 'endpoint-slice status\n'
    exit 0
    ;;
  *" get endpointslices "*)
    attempts=$(cat "${state_file}")
    attempts=$((attempts + 1))
    printf '%s' "${attempts}" >"${state_file}"
    if [[ "${attempts}" -lt 2 ]]; then
      printf 'pod-1\n'
      printf 'pod-1\n'
    else
      printf 'pod-1\n'
      printf 'pod-2\n'
    fi
    exit 0
    ;;
  *" create --dry-run=server -f "*)
    exit 0
    ;;
  "wait --kubeconfig="*" --for=condition=Ready node --all"*)
    attempts=$(cat "${NODE_WAIT_FAKE_STATE:?}")
    attempts=$((attempts + 1))
    printf '%s' "${attempts}" >"${NODE_WAIT_FAKE_STATE}"
    if [[ "${attempts}" -lt "${NODE_WAIT_FAKE_READY_AFTER:-2}" ]]; then
      printf 'timed out waiting for the condition on nodes/kind-control-plane\n'
      exit 1
    fi
    printf 'node/kind-control-plane condition met\n'
    exit 0
    ;;
  "config "*)
    exit 0
    ;;
  "get nodes"*)
    printf 'fake node list\n'
    exit 0
    ;;
  "describe pods"*)
    exit 0
    ;;
esac

printf 'unexpected kubectl call: %s\n' "$*" >&2
exit 1
EOF
chmod +x "${test_dir}/kubectl"

cat >"${test_dir}/kind" <<'EOF'
#!/usr/bin/env bash
set -o errexit
set -o nounset
set -o pipefail

printf '%s\n' "$*" >>"${KIND_FAKE_LOG:?}"

case "$*" in
  "create cluster "*)
    if [[ -n "${KIND_FAKE_CREATE_ERROR:-}" ]]; then
      printf '%s\n' "${KIND_FAKE_CREATE_ERROR}"
      exit 1
    fi
    printf 'Creating cluster ...\n'
    exit 0
    ;;
  "delete cluster "*)
    exit 0
    ;;
esac

printf 'unexpected kind call: %s\n' "$*" >&2
exit 1
EOF
chmod +x "${test_dir}/kind"

cat >"${test_dir}/docker" <<'EOF'
#!/usr/bin/env bash
set -o errexit
set -o nounset
set -o pipefail

printf '%s\n' "$*" >>"${DOCKER_FAKE_LOG:?}"

case "$*" in
  "pull "*)
    attempts=$(cat "${DOCKER_FAKE_STATE:?}")
    attempts=$((attempts + 1))
    printf '%s' "${attempts}" >"${DOCKER_FAKE_STATE}"
    if [[ "${attempts}" -lt "${DOCKER_FAKE_PULL_OK_AFTER:-1}" ]]; then
      printf '%s\n' "${DOCKER_FAKE_PULL_ERROR:?}" >&2
      exit 1
    fi
    printf 'Status: Downloaded newer image\n'
    exit 0
    ;;
  "image inspect "*)
    exit "${DOCKER_FAKE_IMAGE_PRESENT:-1}"
    ;;
  "manifest inspect "*)
    attempts=$(cat "${DOCKER_FAKE_MANIFEST_STATE:?}")
    attempts=$((attempts + 1))
    printf '%s' "${attempts}" >"${DOCKER_FAKE_MANIFEST_STATE}"
    if [[ "${attempts}" -lt "${DOCKER_FAKE_MANIFEST_OK_AFTER:-1}" ]]; then
      printf '%s\n' "${DOCKER_FAKE_MANIFEST_ERROR:?}" >&2
      exit 1
    fi
    printf '{"fake":"manifest"}\n'
    exit 0
    ;;
esac

printf 'unexpected docker call: %s\n' "$*" >&2
exit 1
EOF
chmod +x "${test_dir}/docker"

# shellcheck source=hack/testing/e2e-common.sh
source "${ROOT_DIR}/hack/testing/e2e-common.sh"

KUBECTL_FAKE_STATE="${test_dir}/kubectl-state"
KUBECTL_FAKE_LOG="${test_dir}/kubectl.log"
export KUBECTL_FAKE_STATE KUBECTL_FAKE_LOG
printf '0' >"${KUBECTL_FAKE_STATE}"
: >"${KUBECTL_FAKE_LOG}"

e2e_wait_for_deployment_webhook_endpoints "test.kubeconfig" "kubeflow-system" \
  "kubeflow-trainer-controller-manager" "kubeflow-trainer-controller-manager"

endpoint_attempts=$(cat "${KUBECTL_FAKE_STATE}")
if [[ "${endpoint_attempts}" != "2" ]]; then
  echo "expected EndpointSlice polling to continue until ready endpoints match deployment replicas; got ${endpoint_attempts} attempt(s)" >&2
  exit 1
fi

status_dump_count=$(grep -c -- "-o wide" "${KUBECTL_FAKE_LOG}" || true)
if [[ "${status_dump_count}" != "1" ]]; then
  echo "expected EndpointSlice status to be dumped after an unsatisfied readiness check; got ${status_dump_count} dump(s)" >&2
  exit 1
fi

printf '0' >"${KUBECTL_FAKE_STATE}"
: >"${KUBECTL_FAKE_LOG}"

wait_for_kueue_controller_operator "test.kubeconfig"

endpoint_line=$(grep -n "get endpointslices" "${KUBECTL_FAKE_LOG}" | head -n1 | cut -d: -f1 || true)
probe_line=$(grep -n "create --dry-run=server" "${KUBECTL_FAKE_LOG}" | head -n1 | cut -d: -f1 || true)
if [[ -z "${endpoint_line}" || -z "${probe_line}" ]]; then
  echo "expected Kueue readiness to check EndpointSlices before the dry-run webhook probe" >&2
  exit 1
fi
if [[ "${endpoint_line}" -ge "${probe_line}" ]]; then
  echo "expected Kueue EndpointSlice check to run before the dry-run webhook probe" >&2
  exit 1
fi

# cluster_create retries the bring-up when the node readiness wait times out.
export KIND="${test_dir}/kind"
KIND_FAKE_LOG="${test_dir}/kind.log"
NODE_WAIT_FAKE_STATE="${test_dir}/node-wait-state"
export KIND_FAKE_LOG NODE_WAIT_FAKE_STATE
: >"${KIND_FAKE_LOG}"
printf '0' >"${NODE_WAIT_FAKE_STATE}"
export ARTIFACTS="${test_dir}/artifacts"
mkdir -p "${ARTIFACTS}"

cluster_create "flake-test" "unused-kind-config.yaml" ""

create_attempts=$(grep -c "^create cluster" "${KIND_FAKE_LOG}" || true)
if [[ "${create_attempts}" != "2" ]]; then
  echo "expected cluster creation to be retried after a node readiness timeout; got ${create_attempts} attempt(s)" >&2
  exit 1
fi

node_wait_attempts=$(cat "${NODE_WAIT_FAKE_STATE}")
if [[ "${node_wait_attempts}" != "2" ]]; then
  echo "expected the node readiness wait to run once per bring-up attempt; got ${node_wait_attempts} run(s)" >&2
  exit 1
fi

if ! ls "${ARTIFACTS}"/flake-test-create.log.failed-* >/dev/null 2>&1; then
  echo "expected the failed bring-up log to be archived before the retry" >&2
  exit 1
fi

# A bring-up failure matching no retriable error aborts after a single attempt.
: >"${KIND_FAKE_LOG}"
export KIND_FAKE_CREATE_ERROR="node(s) already exist for a cluster with the name"
if cluster_create "failfast-test" "unused-kind-config.yaml" ""; then
  echo "expected cluster_create to fail on a non-retriable error" >&2
  exit 1
fi
unset KIND_FAKE_CREATE_ERROR

create_attempts=$(grep -c "^create cluster" "${KIND_FAKE_LOG}" || true)
if [[ "${create_attempts}" != "1" ]]; then
  echo "expected a non-retriable failure to fail fast without retrying; got ${create_attempts} attempt(s)" >&2
  exit 1
fi

# A persistent node readiness timeout exhausts all bring-up attempts and fails.
: >"${KIND_FAKE_LOG}"
printf '0' >"${NODE_WAIT_FAKE_STATE}"
export NODE_WAIT_FAKE_READY_AFTER=99
if cluster_create "persistent-test" "unused-kind-config.yaml" ""; then
  echo "expected cluster_create to fail when nodes never become ready" >&2
  exit 1
fi
unset NODE_WAIT_FAKE_READY_AFTER

create_attempts=$(grep -c "^create cluster" "${KIND_FAKE_LOG}" || true)
if [[ "${create_attempts}" != "3" ]]; then
  echo "expected a persistent readiness timeout to exhaust all attempts; got ${create_attempts} attempt(s)" >&2
  exit 1
fi

node_wait_attempts=$(cat "${NODE_WAIT_FAKE_STATE}")
if [[ "${node_wait_attempts}" != "3" ]]; then
  echo "expected the node readiness wait to run once per bring-up attempt; got ${node_wait_attempts} run(s)" >&2
  exit 1
fi

archived_count=$(find "${ARTIFACTS}" -name "persistent-test-create.log.failed-*" | wc -l | tr -d ' ')
if [[ "${archived_count}" != "2" ]]; then
  echo "expected the first two attempts' logs to be archived; got ${archived_count}" >&2
  exit 1
fi

if [[ ! -f "${ARTIFACTS}/persistent-test-create.log" ]]; then
  echo "expected the final attempt's log to remain for failure reporting" >&2
  exit 1
fi

# A transient registry error is retried until the pull succeeds.
DOCKER_FAKE_LOG="${test_dir}/docker.log"
DOCKER_FAKE_STATE="${test_dir}/docker-state"
export DOCKER_FAKE_LOG DOCKER_FAKE_STATE
: >"${DOCKER_FAKE_LOG}"
printf '0' >"${DOCKER_FAKE_STATE}"
export DOCKER_FAKE_PULL_OK_AFTER=2
export DOCKER_FAKE_PULL_ERROR='Error response from daemon: Get "https://registry.example.com/v2/kueue/manifests/sha256:0badc0de": net/http: TLS handshake timeout'

e2e_docker_pull_if_needed "registry.example.com/kueue:test"

pull_attempts=$(grep -c "^pull " "${DOCKER_FAKE_LOG}" || true)
if [[ "${pull_attempts}" != "2" ]]; then
  echo "expected a TLS handshake timeout to be retried; got ${pull_attempts} attempt(s)" >&2
  exit 1
fi

# A missing image is not a transient failure, so it aborts after a single attempt.
: >"${DOCKER_FAKE_LOG}"
printf '0' >"${DOCKER_FAKE_STATE}"
export DOCKER_FAKE_PULL_OK_AFTER=99
export DOCKER_FAKE_PULL_ERROR='Error response from daemon: manifest for registry.example.com/kueue:missing not found'

if e2e_docker_pull_if_needed "registry.example.com/kueue:missing"; then
  echo "expected a missing manifest to fail the pull" >&2
  exit 1
fi
unset DOCKER_FAKE_PULL_ERROR

pull_attempts=$(grep -c "^pull " "${DOCKER_FAKE_LOG}" || true)
if [[ "${pull_attempts}" != "1" ]]; then
  echo "expected a missing manifest to fail fast without retrying; got ${pull_attempts} attempt(s)" >&2
  exit 1
fi

# In dev mode a cached dependency image is reused instead of pulled again.
: >"${DOCKER_FAKE_LOG}"
printf '0' >"${DOCKER_FAKE_STATE}"
export DOCKER_FAKE_PULL_OK_AFTER=1
export DOCKER_FAKE_IMAGE_PRESENT=0

E2E_MODE=dev E2E_SKIP_IMAGE_RELOAD=true e2e_docker_pull_if_needed "registry.example.com/dependency:test"

pull_attempts=$(grep -c "^pull " "${DOCKER_FAKE_LOG}" || true)
if [[ "${pull_attempts}" != "0" ]]; then
  echo "expected a cached dependency image to be reused in dev mode; got ${pull_attempts} pull(s)" >&2
  exit 1
fi

# Opting out of E2E_SKIP_IMAGE_RELOAD pulls even when the image is cached locally.
: >"${DOCKER_FAKE_LOG}"
printf '0' >"${DOCKER_FAKE_STATE}"
export DOCKER_FAKE_PULL_OK_AFTER=1
export DOCKER_FAKE_IMAGE_PRESENT=0

E2E_MODE=dev E2E_SKIP_IMAGE_RELOAD=false e2e_docker_pull_if_needed "registry.example.com/kueue:test"
unset DOCKER_FAKE_PULL_OK_AFTER
unset DOCKER_FAKE_IMAGE_PRESENT

pull_attempts=$(grep -c "^pull " "${DOCKER_FAKE_LOG}" || true)
if [[ "${pull_attempts}" != "1" ]]; then
  echo "expected a cached image to be pulled when E2E_SKIP_IMAGE_RELOAD is off; got ${pull_attempts} pull(s)" >&2
  exit 1
fi

# e2e_docker_manifest_available retries a transient manifest-inspect failure until it succeeds.
DOCKER_FAKE_LOG="${test_dir}/docker.log"
DOCKER_FAKE_MANIFEST_STATE="${test_dir}/docker-manifest-state"
export DOCKER_FAKE_LOG DOCKER_FAKE_MANIFEST_STATE
: >"${DOCKER_FAKE_LOG}"
printf '0' >"${DOCKER_FAKE_MANIFEST_STATE}"
export DOCKER_FAKE_MANIFEST_OK_AFTER=2
export DOCKER_FAKE_MANIFEST_ERROR="net/http: TLS handshake timeout"

if ! e2e_docker_manifest_available "registry.example.com/kueue:transient"; then
  echo "expected a transient manifest-inspect failure to be retried until success" >&2
  exit 1
fi

manifest_attempts=$(grep -c "^manifest inspect " "${DOCKER_FAKE_LOG}" || true)
if [[ "${manifest_attempts}" != "2" ]]; then
  echo "expected manifest inspect to be retried once after a transient failure; got ${manifest_attempts} attempt(s)" >&2
  exit 1
fi

unset DOCKER_FAKE_MANIFEST_OK_AFTER DOCKER_FAKE_MANIFEST_ERROR

# e2e_docker_manifest_available fails fast without retrying when the manifest is genuinely missing.
: >"${DOCKER_FAKE_LOG}"
printf '0' >"${DOCKER_FAKE_MANIFEST_STATE}"
export DOCKER_FAKE_MANIFEST_OK_AFTER=99
export DOCKER_FAKE_MANIFEST_ERROR="no such manifest: registry.example.com/kueue:missing"

if e2e_docker_manifest_available "registry.example.com/kueue:missing"; then
  echo "expected e2e_docker_manifest_available to fail for a genuinely missing manifest" >&2
  exit 1
fi

manifest_attempts=$(grep -c "^manifest inspect " "${DOCKER_FAKE_LOG}" || true)
if [[ "${manifest_attempts}" != "1" ]]; then
  echo "expected a non-retriable manifest-inspect failure to fail fast without retrying; got ${manifest_attempts} attempt(s)" >&2
  exit 1
fi

unset DOCKER_FAKE_MANIFEST_OK_AFTER DOCKER_FAKE_MANIFEST_ERROR

echo "e2e-common tests passed"
