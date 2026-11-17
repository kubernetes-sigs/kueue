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
  # List tracked and untracked, non-ignored shell scripts excluding certain
  # directories and patterns. Unlike find, git ls-files does not descend into
  # nested Git worktrees or clones under the repo.
  # Capture the listing first: a failure in a process substitution is not
  # caught by errexit, and the loop would see an empty listing.
  if ! listed_scripts=$(git -c core.quotePath=false ls-files --cached --others --exclude-standard -- '*.sh' \
    ':!:_*' \
    ':!:.git*' \
    ':!:vendor*'); then
    echo "Failed to list shell scripts with git ls-files" >&2
    exit 1
  fi
  while IFS=$'\n' read -r script; do
    if [[ "$script" == third_party* && "$script" != third_party/forked* ]]; then
      continue
    fi
    if [[ -e "$script" ]]; then
      scripts_to_check+=("$script")
    fi
  done <<< "${listed_scripts}"
  if [[ "${#scripts_to_check[@]}" == 0 ]]; then
    echo "No shell scripts found to check" >&2
    exit 1
  fi
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

