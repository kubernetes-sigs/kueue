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

GO_BUILD_ENV=${GO_BUILD_ENV:-}
GO_CMD=${GO_CMD:-go}
LD_FLAGS=${LD_FLAGS:-}

BUILD_DIR=${BUILD_DIR:-bin}
BUILD_NAME=${BUILD_NAME:-kueuectl}
PLATFORMS=${PLATFORMS:-linux/amd64}

mkdir -p BUILD_DIR

IFS=","
for PLATFORM in ${PLATFORMS} ; do
  export GOOS="${PLATFORM%/*}"
  export GOARCH="${PLATFORM#*/}"
  mkdir -p "${BUILD_DIR}/${GOOS}/${GOARCH}"
  EXTENSION=""

  if [ ${GOOS} == "windows" ]; then
    EXTENSION=".exe"
  fi

  echo "Building for $PLATFORM platform"
  ${GO_BUILD_ENV} ${GO_CMD} build -ldflags="${LD_FLAGS}" -o ${BUILD_DIR}/${GOOS}/${GOARCH}/${BUILD_NAME}${EXTENSION} $1
done
