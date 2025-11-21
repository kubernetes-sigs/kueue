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

resolve_path() {
  local path="$1"
  local -a stack
  local IFS='/'

  # Make relative paths absolute
  [[ "$path" != /* ]] && path="$PWD/$path"

  read -ra parts <<< "$path"
  for part in "${parts[@]}"; do
    case "$part" in
      '' | '.') continue ;;         # skip empty and '.'
      '..') [[ "${#stack[@]}" -gt 0 ]] && unset 'stack[${#stack[@]}-1]' ;; # pop
      *) stack+=("$part") ;;        # push
    esac
  done

  # Join the stack to form the resolved path
  local resolved="/${stack[*]}"
  echo "${resolved// /\/}"
}

declare -r STAGING_IMAGE_REGISTRY="us-central1-docker.pkg.dev/k8s-staging-images/kueue"

# $1 - image name
# $1 - version
function get_image_full_name() {
  local image_name="$1"
  local version="$2"
  if [ "${image_name}" == "charts/kueue" ]; then
    version="${version#v}"
  fi
  local full_image_name="${STAGING_IMAGE_REGISTRY}/${image_name}:${version}"
  echo ${full_image_name}
}

# $1 - full image name
function get_image_details() {
  local full_image_name="$1"
  gcloud container images describe "${full_image_name}" --verbosity error --format json || true
}
