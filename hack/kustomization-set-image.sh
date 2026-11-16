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

# Sets an image in DIR/kustomization.yaml with yq, the same as
# `kustomize edit set image NAME=NEW_NAME:NEW_TAG` run in DIR. Only this
# form is supported: images with a digest or without a tag are rejected.
#
# Usage: kustomization-set-image.sh DIR NAME=NEW_NAME:NEW_TAG

set -o errexit
set -o nounset
set -o pipefail

if [[ $# -ne 2 || $2 != *=* ]]; then
  echo "Usage: $0 DIR NAME=NEW_NAME:NEW_TAG" >&2
  exit 1
fi

YQ="${YQ:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/bin/yq}"
kustomization="$1/kustomization.yaml"
name="${2%%=*}"
image="${2#*=}"

if [[ $image == *@* || ! $image =~ ^(.+):([^:/]+)$ ]]; then
  echo "$0: image \"$image\" must have the form NEW_NAME:NEW_TAG" >&2
  exit 1
fi

# Update the entry when it exists. Otherwise add it and keep the list
# sorted by name, as kustomize does.
NAME="$name" NEW_NAME="${BASH_REMATCH[1]}" NEW_TAG="${BASH_REMATCH[2]}" \
  "$YQ" -i --yaml-compact-seq-indent '
    if ((.images // []) | any_c(.name == strenv(NAME))) then
      (.images[] | select(.name == strenv(NAME))) |= (del(.digest) | .newName = strenv(NEW_NAME) | .newTag = strenv(NEW_TAG))
    else
      .images = ((.images // []) + [{"name": strenv(NAME), "newName": strenv(NEW_NAME), "newTag": strenv(NEW_TAG)}] | sort_by(.name))
    end
  ' "$kustomization"
