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

set -o errexit
set -o nounset
set -o pipefail

# allow overriding docker cli, which should work fine for this script
DOCKER="${DOCKER:-docker}"
CURRENT_DIR=$(dirname "${BASH_SOURCE[0]}")

SHELLCHECK_IMAGE=$(grep '^FROM' "${CURRENT_DIR}/Dockerfile" | awk '{print $2}')

# Initialize an empty array for scripts to check
scripts_to_check=()

if [[ "$#" == 0 ]]; then
  # Find all shell scripts excluding certain directories and patterns
  while IFS=$'\n' read -r script; do
    if ! git check-ignore -q "$script"; then
      scripts_to_check+=("$script")
    fi
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
if [ "${#scripts_to_check[@]}" -ne 0 ]; then
  "${DOCKER}" run --rm -v "$(pwd)":/mnt -w /mnt "${SHELLCHECK_IMAGE}" shellcheck "${scripts_to_check[@]}" >&2 
else
  echo "No scripts to check"
fi

echo "Shellcheck ran successfully"

