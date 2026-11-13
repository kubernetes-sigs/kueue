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

# Temporary, explicitly enabled CI experiment; remove before the final patch.
set -euo pipefail
[[ ${KUEUEVIZ_COMPILE_EXPERIMENT:-0} == 1 ]] || exit 0

experiment_dir=$(mktemp -d)
cleanup() {
  local status=$?
  rm -r "${experiment_dir}"
  exit "${status}"
}
trap cleanup EXIT

mkdir -p "${ARTIFACTS}/compiler-budget-experiment"
go version
cat /sys/fs/cgroup/cpu.max || true

docker buildx inspect default

# Keep the ordinary Docker driver and its Prow cgroup throughout the sweep.
# The normal suite warmed image layers; every sample disables layer reuse and
# gets a fresh compiler-cache mount. Compare the two fastest screening budgets
# in symmetric order before adopting either one.
for run in 1:auto 2:1-4 3:2-4 4:2-4 5:1-4 6:auto; do
  config=${run#*:}
  sample=${run/:/-}
  dockerfile="${experiment_dir}/Dockerfile-${sample}"
  python3 - "${PROJECT_DIR}/Dockerfile" "${dockerfile}" "${sample}-$$" <<'PY'
import pathlib
import sys

source, destination, cache_id = sys.argv[1:]
text = pathlib.Path(source).read_text()
mount = 'RUN --mount=type=cache,target=/root/.cache/go-build \\\n'
assert text.count(mount) == 1
prefix = 'SHELL ["/bin/bash", "-c"]\n'
replacement = (prefix + f'RUN --mount=type=cache,id=kueueviz-budget-{cache_id},target=/root/.cache/go-build \\\n'
               '    export TIMEFORMAT="compiler-cpu: real=%R user=%U sys=%S"; \\\n')
text = text.replace(mount, replacement)
assert text.count('    make build GIT_TAG=') == 1
text = text.replace('    make build GIT_TAG=', '    time make build GIT_TAG=')
pathlib.Path(destination).write_text(text)
PY
  compiler_procs=""
  package_parallelism=""
  if [[ ${config} != auto ]]; then
    compiler_procs=${config%-*}
    package_parallelism=${config#*-}
  fi
  started=$SECONDS
  echo "compiler-budget-experiment: start run=${run}"
  BUILDX_BUILDER=default make -C "${PROJECT_DIR}" kind-image-build \
    GIT_TAG="${GIT_TAG}" GIT_COMMIT="${GIT_COMMIT}" \
    IMAGE_BUILD_GOMAXPROCS="${compiler_procs}" IMAGE_BUILD_GO_PARALLELISM="${package_parallelism}" \
    IMAGE_BUILD_EXTRA_OPTS="--no-cache -f ${dockerfile}" \
    2>&1 | tee "${ARTIFACTS}/compiler-budget-experiment/${sample}.log"
  echo "compiler-budget-experiment: complete run=${run} duration=$((SECONDS - started))s"
done
