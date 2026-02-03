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

function usage() {
  echo "${0} [-p|--prod] <version>"
  echo
  echo "  Wait for images"
  echo
  echo "  Options:"
  echo "    -p, --prod  Check production registry (default: staging)"
  echo
  echo "  Example:"
  echo "    $0 v0.13.2"
  echo "    $0 -p v0.13.2"
  echo "    $0 --prod v0.13.2"
  echo
  exit 2
}

IMAGE_REGISTRY="us-central1-docker.pkg.dev/k8s-staging-images/kueue"
while [[ $# -gt 0 ]]; do
  case $1 in
    -p|--prod)
      IMAGE_REGISTRY="registry.k8s.io/kueue"
      shift
      ;;
    -*)
      usage
      ;;
    *)
      break
      ;;
  esac
done

if [[ "$#" -ne 1 ]]; then
  usage
fi

declare -r RELEASE_VERSION="$1"

if [[ ! "$RELEASE_VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9]+\.[0-9]+)?$ ]]; then
  echo "!!! Invalid release version. It should be semantic version like v0.13.2 or v0.13.2-rc.0"
  exit 1
fi

if ! command -v gcloud >/dev/null 2>&1; then
  echo "!!! gcloud is not installed. Please install the Google Cloud SDK. See https://cloud.google.com/sdk/docs/install for details."
  exit 1
fi

# $1 - image name
# $2 - version
function check_image() {
  local image_name="$1"
  local version="$2"
  local full_image_name="${IMAGE_REGISTRY}/${image_name}:${version}"
  echo "  Checking if \"${full_image_name}\" is available."
  local image_details
  image_details=$(gcloud container images describe "${full_image_name}" --verbosity error --format json || true)
  if [ -n "$image_details" ]; then
    image_name_with_digest=$(echo "$image_details" | jq -r '.image_summary.fully_qualified_digest')
    echo "    ✅ Image \"${image_name_with_digest}\" is available."
    hash=$(echo "$image_name_with_digest" | grep -o -E "sha256:.+")
    echo "    $hash"
    return 0
  else
    echo "    🚫 Image \"${full_image_name}\" is not found."
    return 1
  fi
}

function check_images() {
  local images=(
      kueue
      kueueviz-backend
      kueueviz-frontend
      kueue-populator
  )

    local charts=(
      kueue
      kueue-populator
  )

  echo "Images:"
  for image in "${images[@]}"; do
    echo ""
    if ! check_image "${image}" "${RELEASE_VERSION}"; then
      return 1
    fi
  done

  echo ""
  echo "Charts:"
  for chart in "${charts[@]}"; do
    echo ""
    # Charts require tag without `v` prefix.
    if ! check_image "charts/${chart}" "${RELEASE_VERSION#v}"; then
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
