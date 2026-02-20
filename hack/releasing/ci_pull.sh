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
  echo "${0} <version>"
  echo
  echo "  Create test infra PR"
  echo
  echo "  Example:"
  echo "    $0 v0.13.2"
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

declare -r RELEASE_VERSION="$1"

if [[ ! "$RELEASE_VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "!!! Invalid release version. It should be semantic version like v0.13.2"
  exit 1
fi

IFS='.' read -r MAJOR MINOR PATCH <<< "${RELEASE_VERSION#v}"

if ! [ "$PATCH" -eq 0 ]; then
  echo "!!! This is patch release. Nothing to do."
  exit 1
fi

declare -r MAJOR_MINOR="$MAJOR.$MINOR"

LAST_SUPPORT_MINOR=$((MINOR - 1))
if [ "$LAST_SUPPORT_MINOR" -lt 0 ]; then
  LAST_SUPPORT_MINOR=0  # Prevent negative minors
fi

RELEASE_ISSUE_NAME="Release ${RELEASE_VERSION}"

RELEASE_ISSUE_NUMBER=$(gh issue list --repo="${KUBERNETES_SIGS_KUEUE_MAIN_REPO_ORG}/${KUBERNETES_SIGS_KUEUE_MAIN_REPO_NAME}" --search "in:title ${RELEASE_ISSUE_NAME}" | awk '{print $1}' || true)
if [ -z "$RELEASE_ISSUE_NUMBER" ]; then
  echo "!!! No release issue found for version ${RELEASE_VERSION}. Please create 'Release ${RELEASE_VERSION}' issue first."
  exit 1
fi

RELEASE_ISSUE=$(gh issue view "${RELEASE_ISSUE_NUMBER}" --repo="${KUBERNETES_SIGS_KUEUE_MAIN_REPO_ORG}/${KUBERNETES_SIGS_KUEUE_MAIN_REPO_NAME}" --json body || true)
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
# $2 - local branch
# $3 - commit message
function prepare_local_branch() {
  echo "+++ Creating local branch $2"
  git checkout -b "$2" "${KUBERNETES_TEST_INFRA_UPSTREAM_REMOTE}/$1"
  clean_branches+=("$2")

  local release_config_pattern="*release-*-*.yaml"

  find . -type f -name "$release_config_pattern" -maxdepth 1 | while read -r file; do
    # Extract version (adjust for full path)
    base="${file##*/}"  # Get basename to ignore path
    version=$(echo "$base" | grep -oE '[0-9]+-[0-9]+')

    IFS='-' read -r major_f minor_f <<< "${version#v}"
    if [ "$major_f" -lt "$MAJOR" ] || { [ "$major_f" -eq "$MAJOR" ] && [ "$minor_f" -lt "$LAST_SUPPORT_MINOR" ]; }; then
      echo "$file has version $major_f.$minor_f, which is lower than the latest supported $MAJOR.$MINOR."
      rm "$file"
    fi
  done

  local release_branch="release-$MAJOR.$MINOR"
  local release_suffix="release-$MAJOR-$MINOR"

  local periodics_file_name="kueue-periodics-$release_suffix.yaml"
  local presubmits_file_name="kueue-presubmits-$release_suffix.yaml"

  cp kueue-periodics-main.yaml "$periodics_file_name"
  cp kueue-presubmits-main.yaml "$presubmits_file_name"

  # Update periodics file: base_ref, name, and testgrid-tab-name
  yq eval "(.periodics[].extra_refs[].base_ref) = \"$release_branch\"" -i "$periodics_file_name"
  yq eval "(.periodics[].name) |= sub(\"-main\", \"-$release_suffix\")" -i "$periodics_file_name"
  yq eval "(.periodics[].annotations[\"testgrid-tab-name\"]) |= sub(\"-main\", \"-$release_suffix\")" -i "$periodics_file_name"

  # Update presubmits file: branches, name, and testgrid-tab-name
  yq eval "(.presubmits.kubernetes-sigs/kueue.[].branches[]) |= sub(\"\^main\", \"^$release_branch\")" -i "$presubmits_file_name"
  yq eval "(.presubmits.kubernetes-sigs/kueue.[].name) |= sub(\"-main\", \"-$release_suffix\")" -i "$presubmits_file_name"
  yq eval "(.presubmits.kubernetes-sigs/kueue.[].annotations[\"testgrid-tab-name\"]) |= sub(\"-main\", \"-$release_suffix\")" -i "$presubmits_file_name"

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

  pr_text=$(cat <<EOF
#### What this PR does / why we need it:
$3.

#### Which issue(s) this PR fixes:
Part of ${KUBERNETES_SIGS_KUEUE_MAIN_REPO_ORG}/${KUBERNETES_SIGS_KUEUE_MAIN_REPO_NAME}#${RELEASE_ISSUE_NUMBER}
EOF
)

   gh pr create --title="$3" --body="${pr_text}" --head "${GITHUB_USER}:$2" --base "${rel}" --repo="${KUBERNETES_TEST_INFRA_MAIN_REPO_ORG}/${KUBERNETES_TEST_INFRA_MAIN_REPO_NAME}"
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

CI_BRANCH="kueue-ci-${MAJOR_MINOR}"
declare -r CI_BRANCH
CI_BRANCH_UNIQUE="${CI_BRANCH}-$(date +%s)"
declare -r CI_BRANCH_UNIQUE
CI_PR_NAME="Kueue: CI for ${MAJOR_MINOR}"
declare -r CI_PR_NAME

prepare_local_branch master "${CI_BRANCH_UNIQUE}" "${CI_PR_NAME}"

if [[ -n "${DRY_RUN}" ]]; then
  echo "!!! Skipping git push, PR creation and update issue because you set DRY_RUN."
  exit 0
fi

push_and_create_pr master "${CI_BRANCH}" "${CI_BRANCH_UNIQUE}" "${CI_PR_NAME}"

CI_PR_NUMBER=$(gh pr list --repo="${KUBERNETES_TEST_INFRA_MAIN_REPO_ORG}/${KUBERNETES_TEST_INFRA_MAIN_REPO_NAME}" | grep "${CI_PR_NAME}" | awk '{print $1}' || true)
if [ -n "$CI_PR_NUMBER" ]; then
  NEW_RELEASE_ISSUE_BODY=${RELEASE_ISSUE_BODY//<!-- CI_PULL -->/${KUBERNETES_TEST_INFRA_MAIN_REPO_ORG}/${KUBERNETES_TEST_INFRA_MAIN_REPO_NAME}#${CI_PR_NUMBER}}
  gh issue edit "${RELEASE_ISSUE_NUMBER}" --body "${NEW_RELEASE_ISSUE_BODY}" --repo="${KUBERNETES_SIGS_KUEUE_MAIN_REPO_ORG}/${KUBERNETES_SIGS_KUEUE_MAIN_REPO_NAME}" || {
    echo "!!! Failed to edit release issue \"${RELEASE_ISSUE_NAME}\": gh issue edit command failed."
  }
fi
