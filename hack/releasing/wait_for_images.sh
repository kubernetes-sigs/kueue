#!/usr/bin/env bash

# Copyright 2025 The Kubernetes Authors.
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

REPO_ROOT="$(git rev-parse --show-toplevel)"
declare -r REPO_ROOT

# shellcheck source=hack/releasing/common.sh
source "${REPO_ROOT}/hack/releasing/common.sh"

if [[ "$#" -ne 1 ]]; then
  echo "${0} <version>"
  echo
  echo "  Wait for images"
  echo
  echo "  Example:"
  echo "    $0 v0.13.2"
  echo
  exit 2
fi

declare -r RELEASE_VERSION="$1"

if [[ ! "$RELEASE_VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "!!! Invalid release version. It should be semantic version like v0.13.2"
  exit 1
fi

# $1 - image name
# $2 - version
function check_image() {
  local image_name="$1"
  local version="$2"
  local full_image_name=$(get_image_full_name "$image_name" "$version")
  echo "Checking if \"${full_image_name}\" is available."
  local image_details
  image_details=$(get_image_details "$full_image_name")
  if [ -n "$image_details" ]; then
    image_name_with_digest=$(echo "$image_details" | jq -r '.image_summary.fully_qualified_digest')
    echo " ✅ Image \"${image_name_with_digest}\" is available."
    return 0
  else
    echo " 🚫 Image \"${full_image_name}\" is not found."
    return 1
  fi
}

function check_images() {
  local images=(
      charts/kueue
      kueue
      kueueviz-backend
      kueueviz-frontend
  )
  for image in "${images[@]}"; do
    if ! check_image "${image}" "${RELEASE_VERSION}"; then
      return 1
    fi
  done
}

while true; do
  if check_images; then
    break
  fi
  echo "Try again in 5 seconds..."
  sleep 5
done
