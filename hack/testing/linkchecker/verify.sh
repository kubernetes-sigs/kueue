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

set -o errexit
set -o nounset
set -o pipefail

DOCKER="${DOCKER:-docker}"
SOURCE_DIR="$(cd "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT_DIR="${SOURCE_DIR}/../../.."

# Site to check. Defaults to the live site so the periodic job is unchanged;
# verify-preview.sh overrides this to point at a PR's Netlify deploy preview.
LINK_CHECK_URL="${LINK_CHECK_URL:-https://kueue.sigs.k8s.io/}"

echo "Building linkchecker Docker image..."
"${DOCKER}" build --load -f "${SOURCE_DIR}/Dockerfile" -t linkchecker "${SOURCE_DIR}"

echo "Running linkchecker against ${LINK_CHECK_URL} ..."
"${ROOT_DIR}/hack/testing/retry.sh" --attempts 5 --delay 5 --stream -- \
    "${DOCKER}" run --rm linkchecker --no-warnings --ignore-url='^mailto:' --ignore-url='^tel:' "${LINK_CHECK_URL}"

echo "Link check completed successfully"
