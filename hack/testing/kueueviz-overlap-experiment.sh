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

# Temporary comparison, enabled only by the parent experiment entry point.
set -euo pipefail
[[ ${KUEUEVIZ_COMPILE_EXPERIMENT:-0} == 1 ]] || exit 0

base=024a94ddea13f0a9874eac6d39d52faa2f43ed8d
experiment_dir=$(mktemp -d)
artifact_root=${ARTIFACTS}/overlap-experiment
files=(hack/testing/e2e-kueueviz-backend.sh hack/testing/e2e-kueueviz-frontend.sh
       test/e2e/kueueviz/cypress/e2e/app.cy.js
       cmd/kueueviz/frontend/package-lock.json test/e2e/kueueviz/package-lock.json)
for file in "${files[@]}"; do
  for variant in original baseline candidate; do
    mkdir -p "${experiment_dir}/${variant}/$(dirname "${file}")"
  done
  cp "${PROJECT_DIR}/${file}" "${experiment_dir}/original/${file}"
  git -C "${PROJECT_DIR}" show "HEAD:${file}" >"${experiment_dir}/candidate/${file}"
  git -C "${PROJECT_DIR}" show "${base}:${file}" >"${experiment_dir}/baseline/${file}"
done
cleanup() {
  local status=$?
  for file in "${files[@]}"; do
    cat "${experiment_dir}/original/${file}" >"${PROJECT_DIR}/${file}"
  done
  rm -r "${experiment_dir}"
  exit "${status}"
}
trap cleanup EXIT

# Fresh clusters and frontend containers/dependencies in every run. The manager
# image and backend compilation cache remain fixed; warmup is not measured.
for run in warmup:baseline 1:baseline 2:parallel 3:parallel-nav 4:parallel-nav 5:parallel 6:baseline; do
  variant=candidate
  if [[ ${run#*:} == baseline ]]; then
    variant=baseline
  fi
  for file in "${files[@]}"; do
    cat "${experiment_dir}/${variant}/${file}" >"${PROJECT_DIR}/${file}"
  done
  if [[ ${run#*:} == parallel ]]; then
    cat "${experiment_dir}/baseline/test/e2e/kueueviz/cypress/e2e/app.cy.js" >"${PROJECT_DIR}/test/e2e/kueueviz/cypress/e2e/app.cy.js"
  fi
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
  echo "overlap-experiment: start run=${run}"
  bash "${PROJECT_DIR}/hack/testing/e2e-kueueviz-backend.sh"
  echo "overlap-experiment: complete run=${run} duration=$((SECONDS - started))s"
done
