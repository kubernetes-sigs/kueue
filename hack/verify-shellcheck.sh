#!/bin/bash

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

# check if any shellscript has changed
CHANGED=$(git diff $(find . -name "*.sh"))
if CHANGED=""; then
  echo "No shell scripts have changed"
  echo "Exiting"
  exit 0
else
 
# allow overriding docker cli, which should work fine for this script
DOCKER="${DOCKER:-docker}"


SHELLCHECK_VERSION="0.9.0"
SHELLCHECK_IMAGE="docker.io/koalaman/shellcheck-alpine:v0.9.0@sha256:e19ed93c22423970d56568e171b4512c9244fc75dd9114045016b4a0073ac4b7"

SHELLCHECK_DISABLED="SC2002,SC3028,SC3054,SC3014,SC3040,SC3046,SC3030,SC3010,SC3037,SC3045,SC3006,SC3018,SC3016,SC3011,SC3044,SC3043,SC3060,SC3024,SC1091,SC2066,SC2086,SC2034,SC1083,SC1009,SC1073,SC1072,SC2155,SC2046"
# common arguments we'll pass to shellcheck
SHELLCHECK_OPTIONS=(
  # allow following sourced files that are not specified in the command,
  # we need this because we specify one file at a time in order to trivially
  # detect which files are failing
  "--external-sources"
  # include our disabled lints
  "--exclude=${SHELLCHECK_DISABLED}"
  # set colorized output
  "--color=${SHELLCHECK_COLORIZED_OUTPUT}"
)

# Currently disabled these errors will take care of them later
SHELLCHECK_DISABLED="SC2002,SC3028,SC3054,SC3014,SC3040,SC3046,SC3030,SC3010,SC3037,SC3045,SC3006,SC3018,SC3016,SC3011,SC3044,SC3043,SC3060,SC3024,SC1091,SC2066,SC2086,SC2034,SC1083,SC1009,SC1073,SC1072,SC2155,SC2046"

# common arguments we'll pass to shellcheck
SHELLCHECK_OPTIONS=(
  # allow following sourced files that are not specified in the command,
  # we need this because we specify one file at a time in order to trivially
  # detect which files are failing
  "--external-sources"
  # include our disabled lints
  "--exclude=${SHELLCHECK_DISABLED}"
  # set colorized output
  "--color=${SHELLCHECK_COLORIZED_OUTPUT}"
)

scripts_to_check=("$@")
if [[ "$#" == 0 ]]; then
  # Find all shell scripts excluding:
  # - Anything git-ignored - No need to lint untracked files.
  # - ./_* - No need to lint output directories.
  # - ./.git/* - Ignore anything in the git object store.
  # - ./vendor* - Vendored code should be fixed upstream instead.
  # - ./third_party/*, but re-include ./third_party/forked/*  - only code we
  #    forked should be linted and fixed.
  while IFS=$'\n' read -r script;
    do git check-ignore -q "$script" || scripts_to_check+=("$script");
  done < <(find . -name "*.sh" \
    -not \( \
      -path ./_\*      -o \
      -path ./.git\*   -o \
      -path ./vendor\* -o \
      \( -path ./third_party\* -a -not -path ./third_party/forked\* \) \
    \))
fi

# Download shellcheck-alpine from Docker Hub
echo "Downloading ShellCheck Docker image..."
"${DOCKER}" pull "${SHELLCHECK_IMAGE}"

# Run ShellCheck on all shell script files, excluding those in the 'vendor' directory
echo "Running ShellCheck..."
"${DOCKER}" run \
  --rm -v "$(pwd)" -w "$(pwd)" \
    "${SHELLCHECK_IMAGE}" \
  shellcheck "${SHELLCHECK_OPTIONS[@]}" "${scripts_to_check[@]}" >&2 || res=$?

echo "Shellcheck ran successfully" 
fi

