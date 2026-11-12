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
builder=""
bounded_builder=""
cleanup() {
  local status=$?
  if [[ -n ${builder} ]]; then
    docker buildx rm "${builder}" || status=1
  fi
  if [[ -n ${bounded_builder} ]]; then
    docker buildx rm "${bounded_builder}" || status=1
  fi
  rm -r "${experiment_dir}"
  exit "${status}"
}
trap cleanup EXIT

mkdir -p "${ARTIFACTS}/driver-experiment"
go version
cat /sys/fs/cgroup/cpu.max || true

docker buildx inspect default
builder=$(docker buildx create --driver docker-container)
docker buildx inspect --bootstrap "${builder}"
bounded_builder=$(docker buildx create --driver docker-container --driver-opt cpu-quota=370000,cpu-period=100000,memory=10g)
docker buildx inspect --bootstrap "${bounded_builder}"
for node in "${builder}" "${bounded_builder}"; do
  docker inspect --format 'driver-host: Name={{.Name}} NanoCpus={{.HostConfig.NanoCpus}} CpuQuota={{.HostConfig.CpuQuota}} CpuPeriod={{.HostConfig.CpuPeriod}} Cpuset={{.HostConfig.CpusetCpus}} CgroupParent={{.HostConfig.CgroupParent}} Memory={{.HostConfig.Memory}}' "buildx_buildkit_${node}0"
  docker exec "buildx_buildkit_${node}0" sh -c 'cat /proc/self/cgroup; cat /sys/fs/cgroup/cpu.max' || true
done

# Diagnostic warmups populate image layers. Measured runs disable layer reuse
# and use distinct compiler-cache IDs, but retain the warmed image stores.
for run in diagnostic:docker diagnostic:container diagnostic:bounded 1:docker 2:container 3:bounded 4:bounded 5:container 6:docker; do
  config=${run#*:}
  sample=${run/:/-}
  dockerfile="${experiment_dir}/Dockerfile-${sample}"
  python3 - "${PROJECT_DIR}/Dockerfile" "${dockerfile}" "${sample}-$$" "${run%%:*}" <<'PY'
import pathlib
import shlex
import sys

source, destination, cache_id, phase = sys.argv[1:]
text = pathlib.Path(source).read_text()
mount = 'RUN --mount=type=cache,target=/root/.cache/go-build \\\n'
assert text.count(mount) == 1
prefix = 'SHELL ["/bin/bash", "-c"]\n'
if phase == 'diagnostic':
    probe = ('package main; import ("fmt"; "runtime"); '
             'func main() { fmt.Printf("driver-runtime: version=%s cpus=%d gomaxprocs=%d\\n", '
             'runtime.Version(), runtime.NumCPU(), runtime.GOMAXPROCS(0)) }')
    # The diagnostic program must not warm the manager's compilation cache.
    prefix += ("RUN printf '%s\\n' " + shlex.quote(probe) +
               ' > /tmp/driver-probe.go && GOCACHE=/tmp/driver-probe-cache go run /tmp/driver-probe.go && '
               'cat /proc/self/cgroup && (cat /sys/fs/cgroup/cpu.max || true) && '
               '(cat /sys/fs/cgroup/cpu.stat || true)\n')
replacement = (prefix + f'RUN --mount=type=cache,id=kueueviz-driver-{cache_id},target=/root/.cache/go-build \\\n'
               '    export TIMEFORMAT="driver-cpu: real=%R user=%U sys=%S"; \\\n')
text = text.replace(mount, replacement)
assert text.count('    make build GIT_TAG=') == 1
text = text.replace('    make build GIT_TAG=', '    time make build GIT_TAG=')
pathlib.Path(destination).write_text(text)
PY
  selected_builder=default
  if [[ ${config} == container ]]; then
    selected_builder=${builder}
  elif [[ ${config} == bounded ]]; then
    selected_builder=${bounded_builder}
  fi
  started=$SECONDS
  echo "driver-experiment: start run=${run}"
  BUILDX_BUILDER="${selected_builder}" make -C "${PROJECT_DIR}" kind-image-build \
    GIT_TAG="${GIT_TAG}" GIT_COMMIT="${GIT_COMMIT}" \
    IMAGE_BUILD_GOMAXPROCS= IMAGE_BUILD_GO_PARALLELISM= \
    IMAGE_BUILD_EXTRA_OPTS="--no-cache -f ${dockerfile}" \
    2>&1 | tee "${ARTIFACTS}/driver-experiment/${sample}.log"
  echo "driver-experiment: complete run=${run} duration=$((SECONDS - started))s"
done
