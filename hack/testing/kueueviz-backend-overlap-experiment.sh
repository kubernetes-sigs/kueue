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

# Temporary remote comparison; every sample retains the complete test suite.
set -euo pipefail
[[ ${KUEUEVIZ_COMPILE_EXPERIMENT:-0} == 1 ]] || exit 0

experiment_dir=$(mktemp -d)
artifact_root=${ARTIFACTS}/backend-overlap-experiment
lockfiles=(cmd/kueueviz/frontend/package-lock.json test/e2e/kueueviz/package-lock.json)
for file in "${lockfiles[@]}"; do
  mkdir -p "${experiment_dir}/$(dirname "${file}")"
  cp "${PROJECT_DIR}/${file}" "${experiment_dir}/${file}"
done
cleanup() {
  local status=$?
  for file in "${lockfiles[@]}"; do
    cat "${experiment_dir}/${file}" >"${PROJECT_DIR}/${file}"
  done
  rm -r "${experiment_dir}"
  exit "${status}"
}
trap cleanup EXIT

original_go_cmd=${KUEUEVIZ_TEST_GO_CMD:-go}
original_test_flags="${KUEUEVIZ_TEST_FLAGS--race} -count=1"
# The ordinary suite has warmed image layers and module downloads. Compiler
# caches, clusters, frontend containers and npm installations are fresh per run.
for run in 1:serial 2:parallel 3:parallel-budget 4:parallel-budget 5:parallel 6:serial; do
  export ARTIFACTS="${artifact_root}/${run/:/-}"
  export CYPRESS_SCREENSHOTS_FOLDER="${ARTIFACTS}/cypress/screenshots"
  export CYPRESS_VIDEOS_FOLDER="${ARTIFACTS}/cypress/videos"
  export GOCACHE="${experiment_dir}/go-cache-${run/:/-}"
  export KUEUEVIZ_TEST_GO_CMD=${original_go_cmd}
  export KUEUEVIZ_TEST_FLAGS=${original_test_flags}
  export KUEUEVIZ_RUN_BACKEND_TESTS=true
  if [[ ${run#*:} == parallel-budget ]]; then
    export KUEUEVIZ_TEST_GO_CMD="env GOMAXPROCS=1 ${original_go_cmd}"
    export KUEUEVIZ_TEST_FLAGS="${original_test_flags} -p=4"
  fi
  for file in "${lockfiles[@]}"; do
    cat "${experiment_dir}/${file}" >"${PROJECT_DIR}/${file}"
  done
  for directory in cmd/kueueviz/frontend/node_modules test/e2e/kueueviz/node_modules; do
    if [[ -d "${PROJECT_DIR}/${directory}" ]]; then
      rm -r "${PROJECT_DIR:?}/${directory}"
    fi
  done
  mkdir -p "${ARTIFACTS}" "${GOCACHE}"
  started=$SECONDS
  echo "backend-overlap-experiment: start run=${run}"
  if [[ ${run#*:} == serial ]]; then
    export KUEUEVIZ_RUN_BACKEND_TESTS=false
    "${KUEUEVIZ_MAKE:-make}" -C "${PROJECT_DIR}" \
      "GOFLAGS=${KUEUEVIZ_TEST_GOFLAGS-${GOFLAGS:-}}" "GO_TEST_FLAGS=${KUEUEVIZ_TEST_FLAGS}" \
      "GO_CMD=${KUEUEVIZ_TEST_GO_CMD}" test-kueueviz-backend
  fi
  bash "${PROJECT_DIR}/hack/testing/e2e-kueueviz-backend.sh"
  echo "backend-overlap-experiment: complete run=${run} duration=$((SECONDS - started))s"
  rm -r "${GOCACHE}"
done
