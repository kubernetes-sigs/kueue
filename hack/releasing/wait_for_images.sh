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
  echo "${0} [-p|--prod] [-t|--timeout <duration>] <version>"
  echo
  echo "  Wait for images"
  echo
  echo "  Options:"
  echo "    -p, --prod               Check production registry (default: staging)"
  echo "    -t, --timeout <duration> Maximum time to wait (e.g., 3600 or 3600s). Default: no timeout."
  echo
  echo "  Example:"
  echo "    $0 v0.13.2"
  echo "    $0 -p v0.13.2"
  echo "    $0 --prod --timeout 3600s v0.13.2"
  echo
  exit 2
}

IMAGE_REGISTRY="us-central1-docker.pkg.dev/k8s-staging-images/kueue"
TIMEOUT_SECONDS=0

while [[ $# -gt 0 ]]; do
  case $1 in
    -p|--prod)
      IMAGE_REGISTRY="registry.k8s.io/kueue"
      shift
      ;;
    -t|--timeout)
      if [[ -z "${2:-}" ]]; then
        echo "!!! Error: --timeout requires an argument."
        usage
      fi
      # Strip trailing 's' if present (e.g., 3600s -> 3600)
      TIMEOUT_SECONDS="${2%s}"
      if [[ ! "$TIMEOUT_SECONDS" =~ ^[0-9]+$ ]]; then
        echo "!!! Error: Timeout must be a positive integer."
        exit 1
      fi
      shift 2
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

if ! command -v crane >/dev/null 2>&1; then
  echo "!!! crane is not installed. Please install it or use the github action step."
  exit 1
fi

SCRIPT_DIR="$(dirname "${BASH_SOURCE[0]}")"
# shellcheck source=hack/utils.sh
source "${SCRIPT_DIR}/../utils.sh"

IMAGES_YAML_URL="https://raw.githubusercontent.com/kubernetes/k8s.io/main/registry.k8s.io/images/k8s-staging-kueue/images.yaml"
TMP_IMAGES_FILE=$(mktemp)
declare -r TMP_IMAGES_FILE

function cleanup {
  rm -f "${TMP_IMAGES_FILE}"
}
trap cleanup EXIT

if ! curl -sSLo "${TMP_IMAGES_FILE}" "${IMAGES_YAML_URL}"; then
  echo "!!! Warning: Failed to download images.yaml from github. Version checks may be skipped."
  rm -f "${TMP_IMAGES_FILE}"
fi

# $1 - image name
# $2 - version
function check_image() {
  local image_name="$1"
  local version="$2"
  local full_image_name="${IMAGE_REGISTRY}/${image_name}:${version}"
  echo "  Checking if \"${full_image_name}\" is available."

  local digest
  if digest=$(crane digest "${full_image_name}" 2>/dev/null); then
    echo "    ✅ Image \"${full_image_name}@${digest}\" is available."
    echo "    ${digest}"
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
      kueue-priority-booster
  )

  local charts=(
      kueue
      kueue-populator
      kueue-priority-booster
  )

  echo "Images:"
  for image in "${images[@]}"; do
    if should_skip_image "${image}" "${RELEASE_VERSION}" "${TMP_IMAGES_FILE}"; then
      echo "  Skipping \"${image}\" check (introduced in later version)."
      continue
    fi
    echo ""
    if ! check_image "${image}" "${RELEASE_VERSION}"; then
      return 1
    fi
  done

  echo ""
  echo "Charts:"
  for chart in "${charts[@]}"; do
    if should_skip_image "charts/${chart}" "${RELEASE_VERSION}" "${TMP_IMAGES_FILE}"; then
      echo "  Skipping \"charts/${chart}\" check (introduced in later version)."
      continue
    fi
    echo ""
    if ! check_image "charts/${chart}" "${RELEASE_VERSION#v}"; then
      return 1
    fi
  done
}

# The built-in $SECONDS variable resets to 0 here to keep time tracking accurate
SECONDS=0

while true; do
  if check_images; then
    break
  fi

  # Verify if a timeout was requested and if we exceeded it
  if [[ "$TIMEOUT_SECONDS" -gt 0 && "$SECONDS" -ge "$TIMEOUT_SECONDS" ]]; then
    echo "!!! Error: Timed out waiting for images after ${TIMEOUT_SECONDS}s."
    exit 1
  fi

  echo "Try again in 5 seconds..."
  sleep 5
done