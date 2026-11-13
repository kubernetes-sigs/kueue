#!/bin/bash

# Copyright 2025 The Kubernetes Authors.
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

set -e

SOURCE_DIR="$(cd "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT_DIR="$SOURCE_DIR/../.."
# shellcheck source=hack/testing/e2e-common.sh
source "${SOURCE_DIR}/e2e-common.sh"

mkdir -p "${ARTIFACTS}"

echo ROOT_DIR="${ROOT_DIR}" SOURCE_DIR="${SOURCE_DIR}" KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME}"

# if CI is true and PROW_JOB_ID is set, set NO_COLOR to 1
if [ -n "${CI}" ] && [ -n "${PROW_JOB_ID}" ]; then
  export NO_COLOR=1
  echo "Running in prow: Disabling color to have readable logs: NO_COLOR=${NO_COLOR}"
fi

# Function to clean up background processes
cleanup() {
  e2e_timed_log_unfinished "$?"
  echo "Cleaning up kueueviz processes"
  if [[ -n "${BACKEND_TEST_PID:-}" ]]; then
    # The worker has its own process group, including Make, Go and compilers.
    kill -- "-${BACKEND_TEST_PID}" 2>/dev/null || true
    wait "${BACKEND_TEST_PID}" 2>/dev/null || true
  fi
  # Stop the container itself: terminating the attached Docker client alone
  # would leave frontend preparation or Cypress running in the daemon.
  if [[ -n "${FRONTEND_CONTAINER_NAME:-}" ]]; then
    docker rm -f "${FRONTEND_CONTAINER_NAME}" >/dev/null 2>&1 || true
  fi
  if [[ -n "${FRONTEND_PID:-}" ]]; then
    wait "${FRONTEND_PID}" 2>/dev/null || true
  fi
  # BACKEND_PID may be unset if we exit before the backend starts (e.g. while
  # waiting for the Kueue webhook), so guard against an empty kill argument.
  if [[ -n "${BACKEND_PID:-}" ]]; then
    kill "${BACKEND_PID}" 2>/dev/null || true
    wait "${BACKEND_PID}" 2>/dev/null || true
  fi
  if [[ "${CLUSTER_CREATE_STARTED:-false}" == true ]]; then
    cluster_collect_artifacts "${KIND_CLUSTER_NAME}" ""
    cluster_cleanup "${KIND_CLUSTER_NAME}"
  fi
}

# Set trap to clean up on exit
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if [[ "${KUEUEVIZ_RUN_BACKEND_TESTS:-false}" == true ]]; then
  # Job control gives the worker a separate process group on both Linux and
  # macOS. Disable it inside the worker so all descendants stay in that group.
  set -m
  (
    set +m
    trap 'e2e_timed_log_unfinished "$?"' EXIT
    # Defer the worker's exit until its foreground Make process has reaped the
    # compilers receiving the same group signal.
    trap 'exit 143' TERM
    # Leave CPU for preparation without reducing the race tests' runtime
    # concurrency. Only compiler tools get the default GOMAXPROCS budget;
    # explicit environment settings and later user Go flags take precedence.
    test_go_flags="-p=4 ${KUEUEVIZ_TEST_GOFLAGS-${GOFLAGS:-}}"
    if [[ -z ${GOMAXPROCS+x} ]]; then
      test_go_flags="\"-toolexec=env GOMAXPROCS=1\" ${test_go_flags}"
    fi
    e2e_timed "kueueviz/backend_tests" "${KUEUEVIZ_MAKE:-make}" -C "${ROOT_DIR}" \
      "GOFLAGS=${test_go_flags}" \
      "GO_TEST_FLAGS=${KUEUEVIZ_TEST_FLAGS--race}" \
      "GO_CMD=${KUEUEVIZ_TEST_GO_CMD:-go}" test-kueueviz-backend
  ) & BACKEND_TEST_PID=$!
  set +m
fi

set -x  # Enable debug mode

# Debugging: Print the container ID
CONTAINER_ID=$(docker ps --format json | jq -r 'select(.Image | contains("kubekins-e2e")) | .ID')
echo "Container ID: $CONTAINER_ID"

# Check if the container is running
if [ -n "$CONTAINER_ID" ]; then
  echo "Running container found for image: $CONTAINER_ID"
  # Extract the workspace volume
  WORKSPACE_VOLUME=$(docker inspect "$CONTAINER_ID" | jq -r '.[] | .Mounts[] | select(.Destination=="/workspace") | .Source')
fi

# Check if the workspace volume is empty
if [ -z "$WORKSPACE_VOLUME" ]; then
  WORKSPACE_VOLUME="${ROOT_DIR}"
fi
echo "Workspace Volume: $WORKSPACE_VOLUME"

# if CYPRESS_IMAGE_NAME is not set, extract it from ./hack/testing/cypress/Dockerfile
if [ -z "$CYPRESS_IMAGE_NAME" ]; then
  CYPRESS_IMAGE_NAME=$(grep '^FROM' "${ROOT_DIR}/hack/testing/cypress/Dockerfile" | awk '{print $2}')
fi

# Create the container before backgrounding it so cleanup always owns a known
# container, even if cluster creation fails before the Docker client attaches.
echo "Current container information: CONTAINER_ID=${CONTAINER_ID} WORKSPACE_VOLUME=${WORKSPACE_VOLUME}"
FRONTEND_CONTAINER_NAME="kueueviz-frontend-$$"
BACKEND_START_FILE=/tmp/kueueviz-backend-started
backend_address="${KUEUEVIZ_LISTEN:-localhost:${KUEUEVIZ_PORT:-8080}}"
# Match ServerConfig's bare-port and host-only listen forms. A host-only Listen
# uses the default port even when KUEUEVIZ_PORT is also set.
if [[ "${backend_address}" != *:* ]]; then
  if [[ "${backend_address}" =~ ^[0-9]+$ ]]; then
    backend_address="localhost:${backend_address}"
  else
    backend_address="${backend_address}:8080"
  fi
fi
case "${backend_address}" in
  :*) backend_address="localhost${backend_address}" ;;
  0.0.0.0:*) backend_address="127.0.0.1:${backend_address##*:}" ;;
  '[::]':*) backend_address="[::1]:${backend_address##*:}" ;;
esac
e2e_timed "kueueviz/frontend_create" docker create --name "${FRONTEND_CONTAINER_NAME}" \
           --entrypoint /workspace/hack/testing/e2e-kueueviz-frontend.sh \
           -e CYPRESS_SCREENSHOTS_FOLDER="${CYPRESS_SCREENSHOTS_FOLDER}" \
           -e CYPRESS_VIDEOS_FOLDER="${CYPRESS_VIDEOS_FOLDER}" \
           -e KUEUEVIZ_BACKEND_START_FILE="${BACKEND_START_FILE}" \
           -e KUEUEVIZ_HEALTH_URL="${KUEUEVIZ_HEALTH_URL:-http://${backend_address}/healthz}" \
           -e PROJECT_DIR="/workspace" -w /workspace --network host \
           -v "${WORKSPACE_VOLUME}":/workspace:rw \
           -v "${CYPRESS_SCREENSHOTS_FOLDER}":"${CYPRESS_SCREENSHOTS_FOLDER}" \
           -v "${CYPRESS_VIDEOS_FOLDER}":"${CYPRESS_VIDEOS_FOLDER}" \
           -v /var/run/docker.sock:/var/run/docker.sock "${CYPRESS_IMAGE_NAME}"

set +x  # Disable debug mode

# Dependency installation and frontend unit tests do not need the cluster.
# The frontend script waits for backend health before executing Cypress.
(
  trap 'e2e_timed_log_unfinished "$?"' EXIT
  e2e_timed "kueueviz/frontend" docker start --attach "${FRONTEND_CONTAINER_NAME}"
) & FRONTEND_PID=$!

echo Creating kind cluster "${KIND_CLUSTER_NAME}"
CLUSTER_CREATE_STARTED=true
e2e_timed "kueueviz/cluster_create" cluster_create "${KIND_CLUSTER_NAME}" "$SOURCE_DIR/$KIND_CLUSTER_FILE" ""
echo Waiting for kind cluster "${KIND_CLUSTER_NAME}" to start...
# KueueViz fixtures do not use agnhost or external job operators.
e2e_timed "kueueviz/prepare_images" prepare_kueue_image
e2e_timed "kueueviz/load_images" cluster_kind_load_kueue_image "${KIND_CLUSTER_NAME}"
e2e_timed "kueueviz/deploy" kueue_deploy
kubectl create -f "${ROOT_DIR}/cmd/kueueviz/examples/"

if [[ -n "${BACKEND_TEST_PID:-}" ]]; then
  # Reuse the race-test compilation cache only after every test has passed.
  e2e_timed "kueueviz/backend_tests_wait" wait "${BACKEND_TEST_PID}"
  BACKEND_TEST_PID=""
fi

cd "${ROOT_DIR}/cmd/kueueviz/backend"
# Match the preceding backend tests to reuse their compiled dependencies.
GOFLAGS="${KUEUEVIZ_BACKEND_GOFLAGS:-${GOFLAGS:-}}" e2e_timed "kueueviz/build_backend" go build -o bin/kueueviz
./bin/kueueviz & BACKEND_PID=$!
cd -

# Start the frontend health deadline only after all cluster preparation succeeds.
# A slow but recoverable kind startup must not consume that deadline.
if ! docker exec "${FRONTEND_CONTAINER_NAME}" touch "${BACKEND_START_FILE}"; then
  # If preparation already failed, report its exit status. Do not wait here:
  # a still-running container could be blocked on the marker we failed to send.
  frontend_status=$(docker inspect --format '{{.State.ExitCode}}' "${FRONTEND_CONTAINER_NAME}") || frontend_status=1
  [[ "${frontend_status}" =~ ^[1-9][0-9]*$ ]] || frontend_status=1
  exit "${frontend_status}"
fi
e2e_timed "kueueviz/frontend_wait" wait "${FRONTEND_PID}"
FRONTEND_PID=""

# Stop the unauthenticated backend before starting the TokenReview backend, which
# may use the same port. Keep the cluster alive; this script owns its cleanup.
kill "${BACKEND_PID}" 2>/dev/null || true
wait "${BACKEND_PID}" || {
  backend_status=$?
  [[ $backend_status -eq 143 ]] || exit "$backend_status"
}
BACKEND_PID=""
e2e_timed "kueueviz/rbac" "${SOURCE_DIR}/e2e-kueueviz-rbac-bypass.sh" --use-existing-cluster
# The trap will handle cleanup 
