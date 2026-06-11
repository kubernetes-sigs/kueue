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
  *" get deployment kubeflow-trainer-controller-manager "*)
    printf '10.0.0.1\n'
    exit 0
    ;;
  *" get endpointslices "*)
    attempts=$(cat "${state_file}")
    attempts=$((attempts + 1))
    printf '%s' "${attempts}" >"${state_file}"
    if [[ "${attempts}" -lt 2 ]]; then
      printf '\n'
    else
      printf '10.0.0.1\n'
    fi
    exit 0
    ;;
esac

printf 'unexpected kubectl call: %s\n' "$*" >&2
exit 1
EOF
chmod +x "${test_dir}/kubectl"

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
  echo "expected EndpointSlice polling to continue until ready endpoint appears; got ${endpoint_attempts} attempt(s)" >&2
  exit 1
fi

echo "e2e-common tests passed"
