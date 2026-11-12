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
cleanup() {
  local status=$?
  if [[ -n ${builder} ]]; then
    docker buildx rm "${builder}" || status=1
  fi
  rm -r "${experiment_dir}"
  exit "${status}"
}
trap cleanup EXIT

mkdir -p "${ARTIFACTS}/compile-experiment"
go version
cat /sys/fs/cgroup/cpu.max || true

# Each builder owns an empty BuildKit cache, including the Go compilation mount.
# Keep the source, target platform and image metadata fixed across the sweep.
index=0
for config in auto 2:2 3:3 1:4 2:4 4:4 auto; do
  index=$((index + 1))
  builder=$(docker buildx create --driver docker-container)
  docker buildx inspect --bootstrap "${builder}"
  gomaxprocs=""
  parallelism=""
  if [[ ${config} != auto ]]; then
    gomaxprocs=${config%:*}
    parallelism=${config#*:}
  fi
  started=$SECONDS
  echo "compile-experiment: start phase=manager run=${index} config=${config}"
  BUILDX_BUILDER="${builder}" make -C "${PROJECT_DIR}" kind-image-build \
    GIT_TAG="${GIT_TAG}" GIT_COMMIT="${GIT_COMMIT}" \
    IMAGE_BUILD_GOMAXPROCS="${gomaxprocs}" IMAGE_BUILD_GO_PARALLELISM="${parallelism}" \
    2>&1 | tee "${ARTIFACTS}/compile-experiment/manager-${index}.log"
  echo "compile-experiment: complete phase=manager run=${index} config=${config} duration=$((SECONDS - started))s"
  docker buildx rm "${builder}"
  builder=""
done

cd "${PROJECT_DIR}/cmd/kueueviz/backend"
started=$SECONDS
"${PROJECT_DIR}/hack/testing/retry.sh" --stream -- env go mod download
echo "compile-experiment: complete phase=backend-download duration=$((SECONDS - started))s"

# Downloads are warm after the normal suite, but every run compiles race-enabled
# packages into a fresh cache and executes all backend tests without result reuse.
index=0
for config in auto 2:2 3:3 1:4 2:4 4:4 auto; do
  index=$((index + 1))
  started=$SECONDS
  echo "compile-experiment: start phase=backend run=${index} config=${config}"
  (
    export GOCACHE="${experiment_dir}/go-cache-${index}"
    if [[ ${config} != auto ]]; then
      export GOMAXPROCS=${config%:*}
      export GOFLAGS="${GOFLAGS:-} -p=${config#*:}"
    fi
    go test -race -count=1 ./...
  ) 2>&1 | tee "${ARTIFACTS}/compile-experiment/backend-${index}.log"
  echo "compile-experiment: complete phase=backend run=${index} config=${config} duration=$((SECONDS - started))s"
done
