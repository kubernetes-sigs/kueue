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
# `kustomize edit set image NAME=NEW_NAME[:NEW_TAG][@DIGEST]` run in DIR.
# The image needs a tag, a digest or both.
#
# Usage: kustomization-set-image.sh DIR NAME=NEW_NAME[:NEW_TAG][@DIGEST]

set -o errexit
set -o nounset
set -o pipefail

if [[ $# -ne 2 || $2 != *=* ]]; then
  echo "Usage: $0 DIR NAME=NEW_NAME[:NEW_TAG][@DIGEST]" >&2
  exit 1
fi

YQ="${YQ:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/bin/yq}"
kustomization="$1/kustomization.yaml"
name="${2%%=*}"
image="${2#*=}"

invalid_image() {
  echo "$0: image \"$image\" must have the form NEW_NAME:NEW_TAG, NEW_NAME@DIGEST or NEW_NAME:NEW_TAG@DIGEST" >&2
  exit 1
}

new_name="$image"
new_tag=""
digest=""
if [[ $new_name == *@* ]]; then
  digest="${new_name#*@}"
  new_name="${new_name%%@*}"
  if [[ -z $digest || $digest == *@* ]]; then
    invalid_image
  fi
fi
if [[ $new_name =~ ^(.+):([^:/]+)$ ]]; then
  new_name="${BASH_REMATCH[1]}"
  new_tag="${BASH_REMATCH[2]}"
fi
if [[ -z $new_name || (-z $new_tag && -z $digest) ]]; then
  invalid_image
fi

# yq -i needs to write a temporary file next to the kustomization file, and
# kustomize only needs to write the file itself. Write the result elsewhere
# and copy it into the file, so a read-only directory works too.
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

# Like kustomize, replace the whole entry, so a tag or digest that is not
# given is removed, and write its keys in kustomize's order. Add the entry
# when it does not exist, and keep the list sorted by name.
# shellcheck disable=SC2016 # $image is a yq variable
NAME="$name" NEW_NAME="$new_name" NEW_TAG="$new_tag" DIGEST="$digest" \
  "$YQ" --yaml-compact-seq-indent '
    ((if strenv(DIGEST) == "" then {} else {"digest": strenv(DIGEST)} end)
      + {"name": strenv(NAME), "newName": strenv(NEW_NAME)}
      + (if strenv(NEW_TAG) == "" then {} else {"newTag": strenv(NEW_TAG)} end)) as $image
    | if ((.images // []) | any_c(.name == strenv(NAME))) then
        (.images[] | select(.name == strenv(NAME))) |= $image
      else
        .images = ((.images // []) + [$image] | sort_by(.name))
      end
  ' "$kustomization" > "$tmp"
cat "$tmp" > "$kustomization"
