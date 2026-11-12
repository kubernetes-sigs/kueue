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

# Temporary upstream experiment; remove before final submission.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$root"
baseline=f4e4bb1381ff60637f04db758164d0dce9759596
c1=67c967e6f
git_tag=${KUEUEVIZ_BENCHMARK_GIT_TAG:?}
echo "viz-benchmark-image: tag=${KUEUEVIZ_BENCHMARK_IMAGE_TAG:?}"
docker image inspect "$KUEUEVIZ_BENCHMARK_IMAGE_TAG" --format '{{.Id}}'
scratch=$(mktemp -d "${root}/.viz-benchmark.XXXXXX")
artifacts=${ARTIFACTS:-${root}/_artifacts}
files=(hack/make/test.mk hack/testing/e2e-kueueviz-backend.sh hack/testing/e2e-kueueviz-local.sh hack/testing/e2e-kueueviz-rbac-bypass.sh)
locks=(cmd/kueueviz/frontend/package-lock.json test/e2e/kueueviz/package-lock.json)

restore_candidate() {
  local path
  for path in "${files[@]}" "${locks[@]}"; do
    cp "$scratch/candidate/$path" "$root/$path"
  done
}
cleanup() {
  restore_candidate
  rm -r "$scratch"
}

for path in "${files[@]}" "${locks[@]}"; do
  mkdir -p "$scratch/candidate/$(dirname "$path")" "$scratch/baseline/$(dirname "$path")" "$scratch/c1/$(dirname "$path")"
  cp "$path" "$scratch/candidate/$path"
  git show "$baseline:$path" > "$scratch/baseline/$path"
  git show "$c1:$path" > "$scratch/c1/$path"
done
trap cleanup EXIT

# Warm registry, generated CRDs and downloaded module caches once. Each run gets
# a new Go compilation cache, tool directory, frontend dependencies and clusters.
index=0
for variant in baseline baseline c1 candidate candidate c1 baseline; do
  run="$scratch/run-$index"
  mkdir -p "$run/bin" "$run/go-cache" "$artifacts/viz-benchmark/$index-$variant"
  for path in "${files[@]}" "${locks[@]}"; do
    cp "$scratch/$variant/$path" "$root/$path"
  done
  for directory in cmd/kueueviz/frontend test/e2e/kueueviz; do
    if [[ -d "$directory/node_modules" ]]; then
      mv "$directory/node_modules" "$run/$(echo "$directory" | tr / -)-node_modules"
    fi
  done
  if [[ -f cmd/kueueviz/backend/bin/kueueviz ]]; then
    mv cmd/kueueviz/backend/bin/kueueviz "$run/kueueviz-previous"
  fi
  started=$SECONDS
  echo "viz-benchmark-start: run=$index variant=$variant started=$(date -u +%FT%TZ)"
  GOCACHE="$run/go-cache" KUEUEVIZ_BENCHMARK_CHILD=1 \
    make test-e2e-kueueviz GIT_TAG="$git_tag" BIN_DIR="$run/bin" E2E_BIN_DIR="$run/bin" \
    ARTIFACTS="$artifacts/viz-benchmark/$index-$variant" \
    2>&1 | tee "$artifacts/viz-benchmark/$index-$variant/build.log"
  echo "viz-benchmark-result: run=$index variant=$variant duration=$((SECONDS-started))s"
  index=$((index+1))
done
