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

# Regression test for hack/helm-chart-package.sh: every image the packaged
# charts render must carry the GIT_TAG build tag, never the committed
# Chart.yaml appVersion. The experimental charts bundle the kueue chart via
# `helm dependency build`, which packs charts/kueue as it sits on disk, so a
# GIT_TAG differing from the committed appVersion would make every bundled
# image (controller, kueueViz backend and frontend) fall back to the stale
# committed appVersion; `--app-version` fixes only the top-level charts.
#
# The packaging script guards against that by syncing the kueue chart
# appVersion to GIT_TAG for the packaging window. This test runs it against a
# git archive copy of the tree with a deliberately mismatched GIT_TAG, so it
# never mutates the working tree and can run in parallel with other checks.

set -o errexit
set -o nounset
set -o pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
HELM="${HELM:-${ROOT_DIR}/bin/helm}"
YQ="${YQ:-${ROOT_DIR}/bin/yq}"

# A devel tag that cannot collide with the committed appVersion, which is
# always a plain release tag.
REGRESSION_TAG="v0.21.0-devel-12-gabcdef0"

tmp_dir=$(mktemp -d)
trap 'rm -rf "${tmp_dir}"' EXIT

# The top-level `examples` entry is a symlink to site/static/examples and
# cannot be extracted on hosts without symlink privileges; packaging never
# reads it, so skip it.
git -C "${ROOT_DIR}" archive HEAD | tar -x -C "${tmp_dir}" --exclude=examples

committed_app_version=$("${YQ}" '.appVersion' "${tmp_dir}/charts/kueue/Chart.yaml")
if [[ "${committed_app_version}" == "${REGRESSION_TAG}" ]]; then
  echo "ERROR: test tag ${REGRESSION_TAG} matches the committed appVersion; the regression would not reproduce" >&2
  exit 1
fi

failed=0

chart_version=${REGRESSION_TAG#v}

# assert_bundled_appversion <tgz> <subchart path inside the archive>: the
# kueue chart bundled by `helm dependency build` must carry the build tag as
# its appVersion, since that is the fallback every bundled image renders.
assert_bundled_appversion() {
  local tgz=$1 subchart=$2 got
  got=$(tar -xOf "${tgz}" "${subchart}/Chart.yaml" | "${YQ}" '.appVersion')
  if [[ "${got}" != "${REGRESSION_TAG}" ]]; then
    echo "ERROR: $(basename "${tgz}") bundles ${subchart} with appVersion ${got}, expected ${REGRESSION_TAG}" >&2
    failed=1
  fi
}

# assert_render_tags <label> <tgz> [extra helm args...]: every image the
# packaged chart renders must carry the regression tag. kubectl sidecars pin
# their own tag and are skipped.
assert_render_tags() {
  local label=$1 tgz=$2 line rendered
  shift 2
  # Run the render in a checked command first: a process substitution would
  # discard Helm's exit status, so a broken chart (or a helm wrapper that
  # fails) could silently produce zero image lines and a false pass.
  if ! rendered=$("${HELM}" template regression "${tgz}" "$@"); then
    echo "ERROR: ${label}: helm template failed" >&2
    failed=1
    return
  fi
  local images=()
  while IFS= read -r line; do
    images+=("${line}")
  done < <(printf '%s\n' "${rendered}" | grep -E "image: ['\"]" || true)
  if [[ "${#images[@]}" -eq 0 ]]; then
    echo "ERROR: ${label}: no images found in rendered chart" >&2
    failed=1
    return
  fi
  local found_tag=0
  for line in "${images[@]}"; do
    if [[ "${line}" == *"kubectl:"* ]]; then
      continue
    fi
    if [[ "${line}" != *":${REGRESSION_TAG}"* ]]; then
      echo "ERROR: ${label} rendered a stale image tag (expected ${REGRESSION_TAG}): ${line}" >&2
      failed=1
    else
      found_tag=1
    fi
  done
  if [[ "${found_tag}" -eq 0 ]]; then
    echo "ERROR: ${label}: no image rendered with ${REGRESSION_TAG}" >&2
    failed=1
  fi
}

(cd "${tmp_dir}" && \
  GIT_TAG="${REGRESSION_TAG}" \
  DEST_CHART_DIR="${tmp_dir}/bin" \
  HELM_CHART_PUSH=false \
  HELM="${HELM}" \
  YQ="${YQ}" \
  bash hack/helm-chart-package.sh) > /dev/null

assert_bundled_appversion \
  "${tmp_dir}/bin/kueue-populator-${chart_version}.tgz" \
  "kueue-populator/charts/kueue"
assert_bundled_appversion \
  "${tmp_dir}/bin/kueue-priority-booster-${chart_version}.tgz" \
  "kueue-priority-booster/charts/kueue"

assert_render_tags \
  "packaged kueue chart" \
  "${tmp_dir}/bin/kueue-${chart_version}.tgz" \
  --set enableKueueViz=true
assert_render_tags \
  "packaged kueue-populator chart (bundled kueue)" \
  "${tmp_dir}/bin/kueue-populator-${chart_version}.tgz" \
  --set kueue.enabled=true
assert_render_tags \
  "packaged kueue-priority-booster chart (bundled kueue)" \
  "${tmp_dir}/bin/kueue-priority-booster-${chart_version}.tgz" \
  --set kueue.enabled=true

if [[ "${failed}" -eq 0 ]]; then
  echo "helm-chart-package: all packaged charts render image tag ${REGRESSION_TAG}"
fi
exit "${failed}"
