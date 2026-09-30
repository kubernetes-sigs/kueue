#!/usr/bin/env bash

# Copyright 2024 The Kubernetes Authors.
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

set -o errexit
set -o nounset
set -o pipefail

CGO_ENABLED=${CGO_ENABLED:-0}
GO_CMD=${GO_CMD:-go}
LD_FLAGS=${LD_FLAGS:-}

BUILD_NAME=${BUILD_NAME:-kueuectl}
PLATFORMS=${PLATFORMS:-linux/amd64}

CURRENT_DIR=$(dirname "${BASH_SOURCE[0]}")
ROOT_PATH=$(realpath "${CURRENT_DIR}/..")
BUILD_PATH=${BUILD_PATH:-${ROOT_PATH}/artifacts}
MAIN_PACKAGE=$1

mkdir -p "${BUILD_PATH}"

# Builds a single platform in its own subshell so that build_platform
# invocations can run concurrently in the background; each uses a
# platform-scoped tmp dir so parallel runs don't race on the same files.
build_platform() {
  local PLATFORM=$1
  local GOOS="${PLATFORM%/*}"
  local GOARCH="${PLATFORM#*/}"
  local EXTENSION=""

  if [ "${GOOS}" == "windows" ]; then
    EXTENSION=".exe"
  fi

  echo "Building for ${PLATFORM} platform"
  local FULL_NAME=${BUILD_NAME}-${GOOS}-${GOARCH}
  local TMP_PATH="${BUILD_PATH}/tmp-${GOOS}-${GOARCH}"
  GOOS="${GOOS}" GOARCH="${GOARCH}" "${GO_CMD}" build -ldflags="${LD_FLAGS}" -o "${BUILD_PATH}/${FULL_NAME}${EXTENSION}" "${MAIN_PACKAGE}"

  mkdir -p "${TMP_PATH}"
  cp "${ROOT_PATH}/LICENSE" "${TMP_PATH}"
  cp "${BUILD_PATH}/${FULL_NAME}${EXTENSION}" "${TMP_PATH}/${BUILD_NAME}${EXTENSION}"
  (cd "${TMP_PATH}" && tar -czf "${BUILD_PATH}/${FULL_NAME}.tar.gz" ./*)
  rm -R "${TMP_PATH}"
}

IFS=","
PIDS=()
for PLATFORM in ${PLATFORMS} ; do
  build_platform "${PLATFORM}" &
  PIDS+=("$!")
done

STATUS=0
for PID in "${PIDS[@]}"; do
  wait "${PID}" || STATUS=1
done
exit "${STATUS}"
