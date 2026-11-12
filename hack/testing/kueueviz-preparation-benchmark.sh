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

# Temporary comparison harness; remove before submitting the final patch.
set -euo pipefail

base=9f223b614c283bed8944e0c308f0407aeb655752
benchmark_dir=$(mktemp -d)
artifact_root=${ARTIFACTS}/preparation-comparison
files=(hack/testing/e2e-common.sh hack/testing/e2e-kueueviz-backend.sh
       hack/testing/e2e-kueueviz-frontend.sh hack/testing/e2e-kueueviz-local.sh
       hack/testing/e2e-kueueviz-rbac-bypass.sh
       cmd/kueueviz/frontend/package-lock.json test/e2e/kueueviz/package-lock.json)
mkdir -p "${benchmark_dir}/baseline" "${benchmark_dir}/candidate" "${artifact_root}"
for file in "${files[@]}"; do
  mkdir -p "${benchmark_dir}/candidate/$(dirname "${file}")" "${benchmark_dir}/baseline/$(dirname "${file}")"
  git -C "${PROJECT_DIR}" show "HEAD:${file}" >"${benchmark_dir}/candidate/${file}"
  git -C "${PROJECT_DIR}" show "${base}:${file}" >"${benchmark_dir}/baseline/${file}"
done
cleanup() {
  status=$?
  for file in "${files[@]}"; do
    cat "${benchmark_dir}/candidate/${file}" >"${PROJECT_DIR}/${file}"
  done
  rm -r "${benchmark_dir}"
  exit "${status}"
}
trap cleanup EXIT

# Each run creates a new cluster and frontend container. Empty node_modules so
# both configurations reinstall dependencies; keep the manager image and Go
# cache fixed. The baseline warmup also populates the host's agnhost images.
for run in warmup:baseline 1:baseline 2:candidate 3:candidate 4:baseline; do
  variant=${run#*:}
  for file in "${files[@]}"; do
    cat "${benchmark_dir}/${variant}/${file}" >"${PROJECT_DIR}/${file}"
  done
  for directory in cmd/kueueviz/frontend/node_modules test/e2e/kueueviz/node_modules; do
    if [[ -d "${PROJECT_DIR}/${directory}" ]]; then
      rm -r "${PROJECT_DIR:?}/${directory}"
    fi
  done
  export ARTIFACTS="${artifact_root}/${run/:/-}"
  export CYPRESS_SCREENSHOTS_FOLDER="${ARTIFACTS}/cypress/screenshots"
  export CYPRESS_VIDEOS_FOLDER="${ARTIFACTS}/cypress/videos"
  mkdir -p "${ARTIFACTS}"
  started=$SECONDS
  echo "preparation-benchmark: start run=${run}"
  bash "${PROJECT_DIR}/hack/testing/e2e-kueueviz-backend.sh"
  echo "preparation-benchmark: complete run=${run} duration=$((SECONDS - started))s"
done
