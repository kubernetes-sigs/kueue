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
artifact_root=${ARTIFACTS}/frontend-serving-experiment
build_dir=${PROJECT_DIR}/cmd/kueueviz/frontend/build
build_owned=false
files=(cmd/kueueviz/frontend/package-lock.json test/e2e/kueueviz/package-lock.json
       hack/testing/e2e-kueueviz-frontend.sh)
for file in "${files[@]}"; do
  mkdir -p "${experiment_dir}/$(dirname "${file}")"
  cp "${PROJECT_DIR}/${file}" "${experiment_dir}/${file}"
done
cleanup() {
  local status=$?
  for file in "${files[@]}"; do
    cat "${experiment_dir}/${file}" >"${PROJECT_DIR}/${file}"
  done
  if [[ ${build_owned} == true ]]; then
    if [[ -e ${build_dir} ]]; then
      rm -r "${build_dir}"
    fi
    if [[ -e ${experiment_dir}/original-build ]]; then
      mv "${experiment_dir}/original-build" "${build_dir}"
    fi
  fi
  rm -r "${experiment_dir}"
  exit "${status}"
}
trap cleanup EXIT
if [[ -e ${build_dir} ]]; then
  mv "${build_dir}" "${experiment_dir}/original-build"
fi
build_owned=true

original_go_cmd=${KUEUEVIZ_TEST_GO_CMD:-go}
original_test_flags="${KUEUEVIZ_TEST_FLAGS--race} -count=1"
# The ordinary suite has warmed image layers and module downloads. Compiler
# caches, clusters, frontend containers and npm installations are fresh per run.
for run in 1:dev 2:production 3:production 4:dev; do
  export ARTIFACTS="${artifact_root}/${run/:/-}"
  export CYPRESS_SCREENSHOTS_FOLDER="${ARTIFACTS}/cypress/screenshots"
  export CYPRESS_VIDEOS_FOLDER="${ARTIFACTS}/cypress/videos"
  export GOCACHE="${experiment_dir}/go-cache-${run/:/-}"
  export KUEUEVIZ_TEST_GO_CMD="env GOMAXPROCS=1 ${original_go_cmd}"
  export KUEUEVIZ_TEST_FLAGS="${original_test_flags} -p=4"
  export KUEUEVIZ_RUN_BACKEND_TESTS=true
  for file in "${files[@]}"; do
    cat "${experiment_dir}/${file}" >"${PROJECT_DIR}/${file}"
  done
  if [[ ${run#*:} == production ]]; then
    python3 - "${PROJECT_DIR}/hack/testing/e2e-kueueviz-frontend.sh" <<'PY'
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
text = path.read_text()
command = 'npm run dev & FRONTEND_PID=$!'
assert text.count(command) == 1
path.write_text(text.replace(command, 'npm run build\n'
    'npm run serve -- --listen tcp://127.0.0.1:3000 --no-clipboard & FRONTEND_PID=$!'))
PY
  fi
  for directory in cmd/kueueviz/frontend/node_modules test/e2e/kueueviz/node_modules cmd/kueueviz/frontend/build; do
    if [[ -d "${PROJECT_DIR}/${directory}" ]]; then
      rm -r "${PROJECT_DIR:?}/${directory}"
    fi
  done
  mkdir -p "${ARTIFACTS}" "${GOCACHE}"
  started=$SECONDS
  echo "frontend-serving-experiment: start run=${run}"
  bash "${PROJECT_DIR}/hack/testing/e2e-kueueviz-backend.sh"
  echo "frontend-serving-experiment: complete run=${run} duration=$((SECONDS - started))s"
  rm -r "${GOCACHE}"
done
