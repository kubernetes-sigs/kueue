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

set -o errexit
set -o nounset
set -o pipefail

ROOT_PATH="$(git rev-parse --show-toplevel)"
declare -r ROOT_PATH

# shellcheck source=hack/utils.sh
source "${ROOT_PATH}/hack/utils.sh"

if [[ -v KUBERNETES_REPOS_PATH ]]; then
  KUBERNETES_REPOS_PATH=$(resolve_path "${KUBERNETES_REPOS_PATH}")
  if [[ ! -d "${KUBERNETES_REPOS_PATH}" ]]; then
    echo "!!! Invalid value for KUBERNETES_REPOS_PATH: the path \"${KUBERNETES_REPOS_PATH}\" does not exist."
    exit 1
  fi
else
  KUBERNETES_REPOS_PATH="$(resolve_path "${ROOT_PATH}/../../kubernetes")"
fi
declare -r KUBERNETES_REPOS_PATH

if [[ -v KUBERNETES_TEST_INFRA_PATH ]]; then
  KUBERNETES_TEST_INFRA_PATH=$(resolve_path "${KUBERNETES_TEST_INFRA_PATH}")
  if [[ ! -d "${KUBERNETES_TEST_INFRA_PATH}" ]]; then
    echo "!!! Invalid value for KUBERNETES_TEST_INFRA_PATH: the path \"${KUBERNETES_TEST_INFRA_PATH}\" does not exist."
    exit 1
  fi
else
  KUBERNETES_TEST_INFRA_PATH="${KUBERNETES_REPOS_PATH}/test-infra"
fi
declare -r KUBERNETES_TEST_INFRA_PATH

KUEUE_JOBS_PATH="$KUBERNETES_TEST_INFRA_PATH/config/jobs/kubernetes-sigs/kueue"
if [[ ! -d "${KUEUE_JOBS_PATH}" ]]; then
  echo "!!! The path \"${KUEUE_JOBS_PATH}\" does not exist."
  exit 1
fi
declare -r KUEUE_JOBS_PATH

cd "$KUEUE_JOBS_PATH"

STARTING_BRANCH=$(git symbolic-ref --short HEAD)
declare -r STARTING_BRANCH
declare -r REBASE_MAGIC=".git/rebase-apply"
DRY_RUN=${DRY_RUN:-""}

KUBERNETES_TEST_INFRA_UPSTREAM_REMOTE=${KUBERNETES_TEST_INFRA_UPSTREAM_REMOTE:-upstream}
KUBERNETES_TEST_INFRA_FORK_REMOTE=${KUBERNETES_TEST_INFRA_FORK_REMOTE:-origin}
KUBERNETES_TEST_INFRA_MAIN_REPO_ORG=${KUBERNETES_TEST_INFRA_MAIN_REPO_ORG:-$(git remote get-url "$KUBERNETES_TEST_INFRA_UPSTREAM_REMOTE" | awk '{gsub(/http[s]:\/\/|git@/,"")}1' | awk -F'[@:./]' 'NR==1{print $3}')}
KUBERNETES_TEST_INFRA_MAIN_REPO_NAME=${KUBERNETES_TEST_INFRA_MAIN_REPO_NAME:-$(git remote get-url "$KUBERNETES_TEST_INFRA_UPSTREAM_REMOTE" | awk '{gsub(/http[s]:\/\/|git@/,"")}1' | awk -F'[@:./]' 'NR==1{print $4}')}

if [[ -z ${GITHUB_USER:-} ]]; then
  echo "Please export GITHUB_USER=<your-user> (or GH organization, if that's where your fork lives)"
  exit 1
fi

if ! command -v gh > /dev/null; then
  echo "Can't find 'gh' tool in PATH, please install from https://github.com/cli/cli"
  exit 1
fi

if [[ "$#" -ne 1 ]]; then
  echo "${0} <k8s-version>"
  echo
  echo "  Create Bump k8s version PR"
  echo
  echo "  Example:"
  echo "    $0 v1.35"
  echo
  echo "  Set the DRY_RUN environment var to skip git push and creating PR."
  echo "  When DRY_RUN is set the script will leave you in a branch containing the commits."
  echo
  echo "  Set KUBERNETES_TEST_INFRA_UPSTREAM_REMOTE (default: upstream) and KUBERNETES_TEST_INFRA_FORK_REMOTE (default: origin)"
  echo "  to override the default remote names to what you have locally."
  echo
  echo "  Set KUBERNETES_REPOS_PATH (default: ../../kubernetes) and KUBERNETES_TEST_INFRA_PATH (default: ../../kubernetes/test-infra)"
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

export NEW_VERSION="$1"

if [[ ! "$NEW_VERSION" =~ ^[0-9]+\.[0-9]+$ ]]; then
  echo "!!! Invalid new version. It should be semantic version like 1.35"
  exit 1
fi

echo "+++ Fetching remote..."
git fetch "${KUBERNETES_TEST_INFRA_UPSTREAM_REMOTE}"

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

function update_version() {
  echo "Updating E2E_K8S_VERSION to ${NEW_VERSION} in all YAML files..."
  for file in *.yaml; do
    if [ -f "$file" ]; then
      yq eval '
        (
          select(has("periodics")) | .periodics[] | select(.name | test("-1-[0-9][0-9]$") | not) | .spec.containers[].env[] | select(.name == "E2E_K8S_VERSION") | .value
        ) |= (env(NEW_VERSION) | . style="double") |
        (
          select(has("presubmits")) | .presubmits[][ ] | select(.name | test("-1-[0-9][0-9]$") | not) | .spec.containers[].env[] | select(.name == "E2E_K8S_VERSION") | .value
        ) |= (env(NEW_VERSION) | . style="double")
      ' "$file" > "${file}.tmp" && mv "${file}.tmp" "$file"
      echo "  Updated: $file"
    fi
  done
}

# $1 - base branch
# $2 - local branch
# $3 - commit message
function prepare_local_branch() {
  echo "+++ Creating local branch $2"
  git checkout -b "$2" "${KUBERNETES_TEST_INFRA_UPSTREAM_REMOTE}/$1"
  clean_branches+=("$2")
  update_version
  git add .
  git commit -m "$3"
}

# $1 - base branch
# $2 - remote branch
# $3 - pr name
function make_pr() {
  local rel
  rel="$(basename "$1")"
  echo
  echo "+++ Creating a pull request on GitHub at ${GITHUB_USER}:$2 for ${rel}"
  gh pr create --title="$3" --head "${GITHUB_USER}:$2" --base "${rel}" --repo="${KUBERNETES_TEST_INFRA_MAIN_REPO_ORG}/${KUBERNETES_TEST_INFRA_MAIN_REPO_NAME}"
}

# $1 - base branch
# $2 - remote branch
# $3 - local branch
# $4 - pr name
function push_and_create_pr() {
  echo
  echo "+++ I'm about to do the following to push to GitHub (and I'm assuming ${KUBERNETES_TEST_INFRA_FORK_REMOTE} is your personal fork):"
  echo
  echo "  git push ${KUBERNETES_TEST_INFRA_FORK_REMOTE} ${3}:${2}"
  echo

  read -p "+++ Proceed (anything other than 'y' aborts it)? [y/N] " -r
  if ! [[ "${REPLY}" =~ ^[yY]$ ]]; then
    echo "Aborting." >&2
  else
    git push "${KUBERNETES_TEST_INFRA_FORK_REMOTE}" -f "${3}:${2}"
    make_pr "$1" "$2" "$4"
  fi
}

BUMP_K8S_VERSION_BRANCH="kueue-bump-k8s-version-to-${NEW_VERSION}"
declare -r BUMP_K8S_VERSION_BRANCH
BUMP_K8S_VERSION_BRANCH_UNIQUE="${BUMP_K8S_VERSION_BRANCH}-$(date +%s)"
declare -r BUMP_K8S_VERSION_BRANCH_UNIQUE
BUMP_K8S_VERSION_PR_NAME="Kueue: Bump k8s version to ${NEW_VERSION}"
declare -r BUMP_K8S_VERSION_PR_NAME

prepare_local_branch master "${BUMP_K8S_VERSION_BRANCH_UNIQUE}" "${BUMP_K8S_VERSION_PR_NAME}"

if [[ -n "${DRY_RUN}" ]]; then
  echo "!!! Skipping git push, PR creation and update issue because you set DRY_RUN."
  exit 0
fi

push_and_create_pr master "${BUMP_K8S_VERSION_BRANCH}" "${BUMP_K8S_VERSION_BRANCH_UNIQUE}" "${BUMP_K8S_VERSION_PR_NAME}"
