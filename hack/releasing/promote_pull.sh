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

cd "$KUBERNETES_SIGS_KUEUE_PATH"

KUBERNETES_SIGS_KUEUE_UPSTREAM_REMOTE=${KUBERNETES_SIGS_KUEUE_UPSTREAM_REMOTE:-upstream}
KUBERNETES_SIGS_KUEUE_FORK_REMOTE=${KUBERNETES_SIGS_KUEUE_FORK_REMOTE:-origin}
KUBERNETES_SIGS_KUEUE_MAIN_REPO_ORG=${KUBERNETES_SIGS_KUEUE_MAIN_REPO_ORG:-$(git remote get-url "$KUBERNETES_SIGS_KUEUE_UPSTREAM_REMOTE" | awk '{gsub(/http[s]:\/\/|git@/,"")}1' | awk -F'[@:./]' 'NR==1{print $3}')}
KUBERNETES_SIGS_KUEUE_MAIN_REPO_NAME=${KUBERNETES_SIGS_KUEUE_MAIN_REPO_NAME:-$(git remote get-url "$KUBERNETES_SIGS_KUEUE_UPSTREAM_REMOTE" | awk '{gsub(/http[s]:\/\/|git@/,"")}1' | awk -F'[@:./]' 'NR==1{print $4}')}
KUBERNETES_SIGS_KUEUE_MAIN_REPO="${KUBERNETES_SIGS_KUEUE_MAIN_REPO_ORG}/${KUBERNETES_SIGS_KUEUE_MAIN_REPO_NAME}"

# shellcheck source=hack/utils.sh
source "${KUBERNETES_SIGS_KUEUE_PATH}/hack/utils.sh"

if [[ -v KUBERNETES_REPOS_PATH ]]; then
  KUBERNETES_REPOS_PATH=$(resolve_path "${KUBERNETES_REPOS_PATH}")
  if [[ ! -d "${KUBERNETES_REPOS_PATH}" ]]; then
    echo "!!! Invalid value for KUBERNETES_REPOS_PATH: the path \"${KUBERNETES_REPOS_PATH}\" does not exist."
    exit 1
  fi
else
  KUBERNETES_REPOS_PATH="$(resolve_path "${KUBERNETES_SIGS_KUEUE_PATH}/../../kubernetes")"
fi
declare -r KUBERNETES_REPOS_PATH

if [[ -v KUBERNETES_K8S_IO_PATH ]]; then
  KUBERNETES_K8S_IO_PATH=$(resolve_path "${KUBERNETES_K8S_IO_PATH}")
  if [[ ! -d "${KUBERNETES_K8S_IO_PATH}" ]]; then
    echo "!!! Invalid value for KUBERNETES_K8S_IO_PATH: the path \"${KUBERNETES_K8S_IO_PATH}\" does not exist."
    exit 1
  fi
else
  KUBERNETES_K8S_IO_PATH="${KUBERNETES_REPOS_PATH}/k8s.io"
fi
declare -r KUBERNETES_K8S_IO_PATH

K8S_STAGING_KUEUE_PATH="${KUBERNETES_K8S_IO_PATH}/registry.k8s.io/images/k8s-staging-kueue"
if [[ ! -d "${K8S_STAGING_KUEUE_PATH}" ]]; then
  echo "!!! The path \"${K8S_STAGING_KUEUE_PATH}\" does not exist."
  exit 1
fi
declare -r K8S_STAGING_KUEUE_PATH

cd "${K8S_STAGING_KUEUE_PATH}"

STARTING_BRANCH=$(git symbolic-ref --short HEAD)
declare -r STARTING_BRANCH
declare -r REBASE_MAGIC=".git/rebase-apply"
DRY_RUN=${DRY_RUN:-""}
KUBERNETES_K8S_IO_UPSTREAM_REMOTE=${KUBERNETES_K8S_IO_UPSTREAM_REMOTE:-upstream}
KUBERNETES_K8S_IO_FORK_REMOTE=${KUBERNETES_K8S_IO_FORK_REMOTE:-origin}
KUBERNETES_K8S_IO_MAIN_REPO_ORG=${KUBERNETES_K8S_IO_MAIN_REPO_ORG:-$(git remote get-url "${KUBERNETES_K8S_IO_UPSTREAM_REMOTE}" | awk '{gsub(/http[s]:\/\/|git@/,"")}1' | awk -F'[@:./]' 'NR==1{print $3}')}
KUBERNETES_K8S_IO_MAIN_REPO_NAME=${KUBERNETES_K8S_IO_MAIN_REPO_NAME:-$(git remote get-url "${KUBERNETES_K8S_IO_UPSTREAM_REMOTE}" | awk '{gsub(/http[s]:\/\/|git@/,"")}1' | awk -F'[@:./]' 'NR==1{print $4}')}
KUBERNETES_K8S_IO_MAIN_REPO="${KUBERNETES_K8S_IO_MAIN_REPO_ORG}/${KUBERNETES_K8S_IO_MAIN_REPO_NAME}"

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
  echo "  Create promote PR"
  echo
  echo "  Example:"
  echo "    $0 v0.13.2"
  echo
  echo "  Set the DRY_RUN environment var to skip git push and creating PR."
  echo "  When DRY_RUN is set the script will leave you in a branch containing the commits."
  echo
  echo "  Set KUBERNETES_K8S_IO_UPSTREAM_REMOTE (default: upstream) and KUBERNETES_K8S_IO_FORK_REMOTE (default: origin)"
  echo "  to override the default remote names to what you have locally."
  echo
  echo "  Set KUBERNETES_REPOS_PATH (default: ../../kubernetes) and KUBERNETES_K8S_IO_PATH (default: ../../kubernetes/k8s.io)"
  echo "  to override the default kubernetes paths to what you have locally."
  exit 2
fi

# Checks if you are logged in. Will error/bail if you are not.
gh auth status

if git_status=$(git status --porcelain --untracked=no 2>/dev/null) && [[ -n "${git_status}" ]]; then
  echo "!!! Dirty tree. Clean up and try again."
  exit 1
fi

if [[ -e "${REBASE_MAGIC}" ]]; then
  echo "!!! 'git rebase' or 'git am' in progress. Clean up and try again."
  exit 1
fi

declare -r RELEASE_VERSION="$1"

if [[ ! "$RELEASE_VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "!!! Invalid release version. It should be semantic version like v0.13.2"
  exit 1
fi

IFS='.' read -r MAJOR MINOR _ <<< "${RELEASE_VERSION#v}"

declare -r MAJOR_MINOR="$MAJOR.$MINOR"

LAST_SUPPORT_MINOR=$((MINOR - 1))
if [ "$LAST_SUPPORT_MINOR" -lt 0 ]; then
  LAST_SUPPORT_MINOR=0  # Prevent negative minors
fi

RELEASE_ISSUE_NAME="Release ${RELEASE_VERSION}"

RELEASE_ISSUE_NUMBER=$(gh issue list --repo="${KUBERNETES_SIGS_KUEUE_MAIN_REPO}" --search "in:title ${RELEASE_ISSUE_NAME}" | awk '{print $1}' || true)
if [ -z "$RELEASE_ISSUE_NUMBER" ]; then
  echo "!!! No release issue found for version ${RELEASE_VERSION}. Please create 'Release ${RELEASE_VERSION}' issue first."
  exit 1
fi

RELEASE_ISSUE=$(gh issue view "${RELEASE_ISSUE_NUMBER}" --repo="${KUBERNETES_SIGS_KUEUE_MAIN_REPO}" --json body || true)
if [ -z "$RELEASE_ISSUE" ]; then
  echo "!!! No release issue found for version ${RELEASE_VERSION}. Please create 'Release ${RELEASE_VERSION}' issue first."
  exit 1
fi

RELEASE_ISSUE_BODY=$(echo "${RELEASE_ISSUE}" | jq -r '.body')

clean_branches=()
function cleanup {
  # Return to the starting branch and delete specified branches
  echo
  echo "+++ Returning to the ${STARTING_BRANCH} branch."
  git checkout -f "${STARTING_BRANCH}" >/dev/null 2>&1 || {
    echo "!!! Failed to return to ${STARTING_BRANCH}. Please check your git status."
    return 1
  }

  if [[ ${#clean_branches[@]} -eq 0 ]]; then
    echo "!!! No branches specified for cleanup."
    return 0
  fi

  for branch in "${clean_branches[@]}"; do
    if [[ -n "${branch}" && -z "${DRY_RUN}" ]]; then
      echo "!!! Deleting branch ${branch}."
      git branch -D "${branch}" >/dev/null 2>&1 || {
        echo "!!! Failed to delete branch ${branch}. It may not exist or is currently checked out."
      }
    else
      echo "!!! Skipping deletion of branch ${branch} because DRY_RUN is set."
      echo "To delete this branch manually:"
      echo "  git branch -D ${branch}"
    fi
  done
}
trap cleanup EXIT

# $1 - base branch
# $2 - remote branch
# $3 - pr name
function make_pr() {
  local rel
  rel="$(basename "$1")"

  echo
  echo "+++ Creating a pull request on GitHub repo ${KUBERNETES_K8S_IO_MAIN_REPO} at ${GITHUB_USER}:$2 for ${rel}"

  pr_text=$(cat <<EOF
#### What this PR does / why we need it:
$3.

#### Which issue(s) this PR fixes:
Part of ${KUBERNETES_SIGS_KUEUE_MAIN_REPO}#${RELEASE_ISSUE_NUMBER}
EOF
)

   gh pr create --title="$3" --body="${pr_text}" --head "${GITHUB_USER}:$2" --base "${rel}" --repo="${KUBERNETES_K8S_IO_MAIN_REPO}"
}

# $1 - images file
# $2 - release version
# $3 - digest
# $4 - component
function insert_image() {
  local images_file="$1"
  local release_version="$2"
  local digest="$3"
  local component="$4"

  echo "+++ Insert image $component@$release_version:$digest"

  tmp_file=$(mktemp)

  awk -v component="${component}" -v release_version="${release_version}" -v digest="${digest}" '
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
    in_component && !inserted && $0 ~ /v?[0-9]+\.[0-9]+\.[0-9]+/ {
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
        if (version ~ /^v?[0-9]+\.[0-9]+\.[0-9]+$/) {
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

  mv "$tmp_file" "$IMAGES_FILE"
}

declare -r IMAGES_FILE="images.yaml"
declare -r STAGING_IMAGE_REGISTRY="us-central1-docker.pkg.dev/k8s-staging-images/kueue"

# $1 - image name
# $1 - version
function get_image_full_name() {
  local image_name="$1"
  local version="$2"
  local full_image_name="${STAGING_IMAGE_REGISTRY}/${image_name}:${version}"
  echo "${full_image_name}"
}

# $1 - full image name
function get_image_details() {
  local full_image_name="$1"
  gcloud container images describe "${full_image_name}" --verbosity error --format json || true
}

# $1 - base branch
# $2 - local branch
# $3 - commit message
function prepare_local_branch() {
  echo "+++ Creating local branch $2"
  git checkout -b "$2" "${KUBERNETES_K8S_IO_UPSTREAM_REMOTE}/$1"
  clean_branches+=("$2")

  while IFS= read -r name; do
    version="$RELEASE_VERSION"
    if [[ "${name}" == "charts/kueue" || "${name}" == "charts/kueue-populator" ]]; then
      version="${version#v}"
    fi

    full_image_name=$(get_image_full_name "$name" "$version")
    image_details=$(get_image_details "$full_image_name")
    if [ -z "$image_details" ]; then
      echo " !!! Image \"${full_image_name}\" is not found."
      exit 1
    fi
    digest=$(echo "$image_details" | jq -r '.image_summary.digest')
    insert_image "$IMAGES_FILE" "$version" "$digest" "$name"
  done < <(yq e '.[] | .name' "${IMAGES_FILE}")

  git add .
  git commit -m "$3"
}

# $1 - base branch
# $2 - remote branch
# $3 - local branch
# $4 - pr name
function push_and_create_pr() {
  if [[ -n "${DRY_RUN}" ]]; then
    echo "!!! Skipping git push and PR creation because you set DRY_RUN."
    return
  fi

  echo
  echo "+++ I'm about to do the following to push to GitHub (and I'm assuming ${KUBERNETES_K8S_IO_FORK_REMOTE} is your personal fork):"
  echo
  echo "  git push ${KUBERNETES_K8S_IO_FORK_REMOTE} ${3}:${2}"
  echo

  read -p "+++ Proceed (anything other than 'y' aborts it)? [y/N] " -r
  if ! [[ "${REPLY}" =~ ^[yY]$ ]]; then
    echo "Aborting." >&2
  else
    git push "${KUBERNETES_K8S_IO_FORK_REMOTE}" -f "${3}:${2}"
    make_pr "$1" "$2" "$4"
  fi
}

K8S_IO_BRANCH="kueue-promote-${MAJOR_MINOR}"
declare -r K8S_IO_BRANCH
K8S_IO_BRANCH_UNIQUE="${K8S_IO_BRANCH}-$(date +%s)"
declare -r K8S_IO_BRANCH_UNIQUE
K8S_IO_PR_NAME="Kueue: Promote ${MAJOR_MINOR}"
declare -r K8S_IO_PR_NAME

prepare_local_branch main "${K8S_IO_BRANCH_UNIQUE}" "${K8S_IO_PR_NAME}"
push_and_create_pr main "${K8S_IO_BRANCH}" "${K8S_IO_BRANCH_UNIQUE}" "${K8S_IO_PR_NAME}"

K8S_IO_PR_NUMBER=$(gh pr list --repo="${KUBERNETES_K8S_IO_MAIN_REPO}" | grep "${K8S_IO_PR_NAME}" | awk '{print $1}' || true)
if [ -n "$K8S_IO_PR_NUMBER" ]; then
  NEW_RELEASE_ISSUE_BODY=${RELEASE_ISSUE_BODY//<!-- K8S_IO_PULL -->/${KUBERNETES_K8S_IO_MAIN_REPO}#${K8S_IO_PR_NUMBER}}
  echo "+++ Editing release issue ${RELEASE_ISSUE_NUMBER} on GitHub repo ${KUBERNETES_SIGS_KUEUE_MAIN_REPO}"
  gh issue edit "${RELEASE_ISSUE_NUMBER}" --body "${NEW_RELEASE_ISSUE_BODY}" --repo="${KUBERNETES_SIGS_KUEUE_MAIN_REPO}" || {
    echo "!!! Failed to edit release issue \"${RELEASE_ISSUE_NAME}\": gh issue edit command failed."
  }
fi
