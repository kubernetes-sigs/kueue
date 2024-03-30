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

# allow overriding docker cli, which should work fine for this script
DOCKER="${DOCKER:-docker}"


SHELLCHECK_VERSION="0.9.0"
SHELLCHECK_IMAGE="docker.io/koalaman/shellcheck-alpine:v0.9.0@sha256:e19ed93c22423970d56568e171b4512c9244fc75dd9114045016b4a0073ac4b7"

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

# Run ShellCheck on all shell script files, excluding those in the 'vendor' directory.
# Configuration loaded from the .shelcheckrc file.
echo "Running ShellCheck..."
"${DOCKER}" run \
  --rm -v "$(pwd)" -w "$(pwd)" \
    "${SHELLCHECK_IMAGE}" \
  shellcheck "--color=${SHELLCHECK_COLORIZED_OUTPUT}" "${scripts_to_check[@]}" >&2 || res=$?

echo "Shellcheck ran successfully"
