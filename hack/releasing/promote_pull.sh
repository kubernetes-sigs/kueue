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

KUBERNETES_SIGS_KUEUE_PATH="$(git rev-parse --show-toplevel)"
declare -r KUBERNETES_SIGS_KUEUE_PATH

# shellcheck source=hack/releasing/common.sh
source "${KUBERNETES_SIGS_KUEUE_PATH}/hack/releasing/common.sh"

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

if [[ -v KUBERNETES_REPOS_PATH ]]; then
  KUBERNETES_REPOS_PATH=$(resolve_path "${KUBERNETES_REPOS_PATH}")
  if [[ ! -d "${KUBERNETES_REPOS_PATH}" ]]; then
    echo "!!! Invalid value for KUBERNETES_REPOS_PATH: the path \"${KUBERNETES_REPOS_PATH}\" does not exist."
    exit 1
  fi
else
  KUBERNETES_REPOS_PATH="${KUBERNETES_SIGS_KUEUE_PATH}/../../kubernetes"
fi
declare -r KUBERNETES_REPOS_PATH

if [[ -v KUBERNETES_K8S_IO_PATH ]]; then
  KUBERNETES_K8S_IO_PATH=$(resolve_path "${KUBERNETES_K8S_IO_PATH}")
  if [[ ! -d "${KUBERNETES_K8S_IO_PATH}" ]]; then
    echo "!!! Invalid value for KUBERNETES_K8S_IO_PATH: the path \"${KUBERNETES_K8S_IO_PATH}\" does not exist."
    exit 1
  fi
else
  KUBERNETES_K8S_IO_PATH="${KUBERNETES_K8S_IO_PATH}/k8s.io"
fi
declare -r KUBERNETES_K8S_IO_PATH

K8S_STAGING_KUEUE_PATH="$KUBERNETES_K8S_IO_PATH/registry.k8s.io/images/k8s-staging-kueue"
if [[ ! -d "${K8S_STAGING_KUEUE_PATH}" ]]; then
  echo "!!! The path \"${K8S_STAGING_KUEUE_PATH}\" does not exist."
  exit 1
fi
declare -r K8S_STAGING_KUEUE_PATH

cd "$K8S_STAGING_KUEUE_PATH"

STARTING_BRANCH=$(git symbolic-ref --short HEAD)
declare -r STARTING_BRANCH
declare -r REBASE_MAGIC=".git/rebase-apply"
DRY_RUN=${DRY_RUN:-""}
UPSTREAM_REMOTE=${UPSTREAM_REMOTE:-upstream}
FORK_REMOTE=${FORK_REMOTE:-origin}
MAIN_REPO_ORG=${MAIN_REPO_ORG:-$(git remote get-url "$UPSTREAM_REMOTE" | awk '{gsub(/http[s]:\/\/|git@/,"")}1' | awk -F'[@:./]' 'NR==1{print $3}')}
MAIN_REPO_NAME=${MAIN_REPO_NAME:-$(git remote get-url "$UPSTREAM_REMOTE" | awk '{gsub(/http[s]:\/\/|git@/,"")}1' | awk -F'[@:./]' 'NR==1{print $4}')}

if [[ -z ${GITHUB_USER:-} ]]; then
  echo "Please export GITHUB_USER=<your-user> (or GH organization, if that's where your fork lives)"
  exit 1
fi

if ! command -v gh > /dev/null; then
  echo "Can't find 'gh' tool in PATH, please install from https://github.com/cli/cli"
  exit 1
fi

if [[ "$#" -ne 1 ]]; then
  echo "${0} <version>"
  echo
  echo "  Create image promotion PR"
  echo
  echo "  Example:"
  echo "    $0 v0.13.2"
  echo
  echo "  Set the DRY_RUN environment var to skip git push and creating PR."
  echo "  When DRY_RUN is set the script will leave you in a branch containing the commits."
  echo
  echo "  Set UPSTREAM_REMOTE (default: upstream) and FORK_REMOTE (default: origin)"
  echo "  to override the default remote names to what you have locally."
  echo
  echo "  Set KUBERNETES_REPOS_PATH (default: ../../kubernetes) and KUBERNETES_K8S_IO_PATH (default: ../../kubernetes/k8s.io)"
  echo "  to override the default kubernetes paths to what you have locally."
  exit 2
fi

# Checks if you are logged in. Will error/bail if you are not.
gh auth status

# TODO: Uncomment
#if git_status=$(git status --porcelain --untracked=no 2>/dev/null) && [[ -n "${git_status}" ]]; then
#  echo "!!! Dirty tree. Clean up and try again."
#  exit 1
#fi

if [[ -e "${REBASE_MAGIC}" ]]; then
  echo "!!! 'git rebase' or 'git am' in progress. Clean up and try again."
  exit 1
fi

declare -r RELEASE_VERSION="$1"

if [[ ! "$RELEASE_VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "!!! Invalid release version. It should be semantic version like v0.13.2"
  exit 1
fi

declare -r IMAGES_FILE="images.yaml"

#while IFS= read -r name; do
#  full_image_name=$(get_image_full_name "$name" "$RELEASE_VERSION")
#  image_details=$(get_image_details "$full_image_name")
#  if [ -z "$image_details" ]; then
#    echo " !!! Image \"${full_image_name}\" is not found."
#    exit 1
#  fi
#  digest=$(echo "$image_details" | jq -r '.image_summary.digest')
#  echo $digest
#done < <(yq e '.[] | .name' "${IMAGES_FILE}")


#tmpfile=$(mktemp)
## Use awk to process the file and remove lines containing the specified version
#awk -v version="${RELEASE_VERSION}" '$0 !~ version' "${IMAGES_FILE}" > "$tmpfile"
#mv "$tmpfile" "$IMAGES_FILE"

DIGEST="sha256:f77326c7250c01a5389aa857a89e5ad69fc5700b811cea1c108c3da66b516090"
NEW_ENTRY="    \"sha256:f77326c7250c01a5389aa857a89e5ad69fc5700b811cea1c108c3da66b516090\": [\"${RELEASE_VERSION}\"]"

# $1 - images file
# $2 - release version
# $3 - digest
function insert_image() {
  local images_file="$1"
  local release_version="$2"
  local digest="$3"

  tmp_file=$(mktemp)

  awk -v component="kueue" -v release_version="${release_version}" -v digest="${digest}" '
    BEGIN {
      new_line = sprintf("    \"%s\": [\"%s\"]", digest, release_version);
      found_component = 0;
      in_component = 0;
      inserted = 0;

      # Remove "v" prefix
      core_release_version = gsub(/^v/, "", version);

      # Split release_version into major, minor, patch
      split(release_version, vref, ".");

      ref_major = vref[1] + 0;  # Convert to number
      ref_minor = vref[2] + 0;
      ref_patch = vref[3] + 0;
    }

    # Skip empty lines
    /^[[:space:]]*$/ {
       next;
    }

    # Match lines starting with "- name: " followed by the component name
    /^[[:space:]]*- name: / {
      if (in_component && !inserted) {
        print new_line;
      }

      found_component = 1;
      in_component = ($3 == component) ? 1 : 0;
      inserted = 0;

      print;
      next;
    }

    # Insert the new entry before the line containing the version reference
    in_component && !inserted && $0 ~ /v[0-9]+\.[0-9]+\.[0-9]+/ {
      line = $0;

      # Remove everything before the array
      sub(/.*:[[:space:]]*\[/, "", line);
      # Remove trailing ] and anything after
      sub(/\][[:space:]]*.*$/, "", line);

      # Split into versions (e.g., "v0.1.0", "v0.1.12")
      n = split(line, versions, /,[[:space:]]*/);

      for (i = 1; i <= n; i++) {
        version = versions[i];

        gsub(/^"/, "", version);
        gsub(/"$/, "", version);

        # Skip empty versions
        if (version == "") {
          continue;
        }

        # Check if version matches vMAJOR.MINOR.PATCH
        if (version ~ /^v[0-9]+\.[0-9]+\.[0-9]+$/) {
          # Remove "v" prefix
          gsub(/^v/, "", version);

          # Split version into components
          split(version, v, "\.");

          major = v[1] + 0;
          minor = v[2] + 0;
          patch = v[3] + 0;

          # Compare with release_version
          if (major < ref_major ||
              (major == ref_major && minor < ref_minor) ||
              (major == ref_major && minor == ref_minor && patch < ref_patch)) {
              print new_line;
              inserted = 1;
              break;
          }
        }
      }
      print;
      next;
    }
    # Print all other lines unchanged
    { print; }
  ' "${images_file}" > "$tmp_file"

  # TODO: Revert it
  # mv "$tmp_file" "$IMAGES_FILE"
  mv "$tmp_file" tmpfile.yaml
}

insert_image "$IMAGES_FILE" "$RELEASE_VERSION" "$DIGEST"