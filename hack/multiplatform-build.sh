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
BUILD_PATH=${ROOT_PATH}/${BUILD_DIR}

mkdir -p "${BUILD_PATH}"

IFS=","
for PLATFORM in ${PLATFORMS} ; do
  export GOOS="${PLATFORM%/*}"
  export GOARCH="${PLATFORM#*/}"
  EXTENSION=""

  if [ "${GOOS}" == "windows" ]; then
    EXTENSION=".exe"
  fi

  echo "Building for $PLATFORM platform"
  FULL_NAME=${BUILD_NAME}-${GOOS}-${GOARCH}
  "${GO_CMD}" build -ldflags="${LD_FLAGS}" -o "${BUILD_PATH}/${FULL_NAME}${EXTENSION}" "$1"

  mkdir -p "${BUILD_PATH}/tmp"
  cp "${ROOT_PATH}/LICENSE" "${BUILD_PATH}/tmp"
  cp "${BUILD_PATH}/${FULL_NAME}${EXTENSION}" "${BUILD_PATH}/tmp/${BUILD_NAME}${EXTENSION}"
  (cd "${BUILD_PATH}/tmp" && tar -czf "${BUILD_PATH}/${FULL_NAME}.tar.gz" ./*)
  rm -R "${BUILD_PATH}/tmp"
done
