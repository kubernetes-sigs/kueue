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

REPO_ROOT="$(git rev-parse --show-toplevel)"
declare -r REPO_ROOT
cd "${REPO_ROOT}"

STARTING_BRANCH=$(git symbolic-ref --short HEAD)
declare -r STARTING_BRANCH
declare -r REBASE_MAGIC="${REPO_ROOT}/.git/rebase-apply"
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
  echo "  Create prepare-release PRs for the release branch and main (for major/minor releases)"
  echo
  echo "  Example:"
  echo "    $0 v0.13.2"
  echo
  echo "  Set the DRY_RUN environment var to skip git push and creating PR."
  echo "  This is useful for creating patches to a release branch without making a PR."
  echo "  When DRY_RUN is set the script will leave you in a branch containing the commits."
  echo
  echo "  Set UPSTREAM_REMOTE (default: upstream) and FORK_REMOTE (default: origin)"
  echo "  to override the default remote names to what you have locally."
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

# Split into MAJOR, MINOR, PATCH
core_version=${RELEASE_VERSION#v}
MAJOR=$(echo "$core_version" | cut -d. -f1)
MINOR=$(echo "$core_version" | cut -d. -f2)

MAJOR_MINOR="${MAJOR}.${MINOR}"

RELEASE_BRANCH="release-${MAJOR_MINOR}"
declare -r RELEASE_BRANCH

echo "+++ Updating remotes..."
git remote update "${UPSTREAM_REMOTE}" "${FORK_REMOTE}"

# Check if the branch exists on the remote
if ! git ls-remote --heads "$UPSTREAM_REMOTE" "${RELEASE_BRANCH}" | grep -q "${RELEASE_BRANCH}"; then
  echo "!!! '$UPSTREAM_REMOTE/${RELEASE_BRANCH}' not found. Please create branch first."
  exit 1
fi

RELEASE_ISSUE_NAME="Release ${RELEASE_VERSION}"

RELEASE_ISSUE_NUMBER=$(gh issue list --repo="${MAIN_REPO_ORG}/${MAIN_REPO_NAME}" | grep "${RELEASE_ISSUE_NAME}" | awk '{print $1}' || true)
if [ -z "$RELEASE_ISSUE_NUMBER" ]; then
  echo "!!! No release issue found for version ${RELEASE_VERSION}. Please create 'Release ${RELEASE_VERSION}' issue first."
  exit 1
fi

RELEASE_ISSUE=$(gh issue view "${RELEASE_ISSUE_NUMBER}" --repo="${MAIN_REPO_ORG}/${MAIN_REPO_NAME}" --json body || true)
if [ -z "$RELEASE_ISSUE" ]; then
  echo "!!! No release issue found for version ${RELEASE_VERSION}. Please create 'Release ${RELEASE_VERSION}' issue first."
  exit 1
fi

RELEASE_ISSUE_BODY=$(echo "${RELEASE_ISSUE}" | jq -r '.body')

CHANGELOG_FILE="${REPO_ROOT}/CHANGELOG/CHANGELOG-${MAJOR_MINOR}.md"
declare -r CHANGELOG_FILE

# shellcheck disable=SC2016
CHANGELOG=$(echo "${RELEASE_ISSUE_BODY}" | sed -n '/^```markdown$/,/^```$/p' | sed '/^```markdown$/d;/^```$/d')
if [ -z "$CHANGELOG" ]; then
  echo "!!! No changelog found. Please update issue and add changelog."
fi

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

# $1 - version
# $2 - branch
function update_version_and_branch() {
  tmpfile=$(mktemp)
  while IFS= read -r line; do
      # Replace version numbers (vX.Y.Z with v0.13.2)
      if [[ $line =~ v[0-9]+\.[0-9]+\.[0-9]+ ]]; then
          new_line="${line//v[0-9]*\.[0-9]*\.[0-9]*/$1}"
      else
          new_line="$line"
      fi

      # Replace RELEASE_BRANCH value
      if [[ $new_line =~ RELEASE_BRANCH=.* ]]; then
          new_line="RELEASE_BRANCH=$2"
      fi

      # Write the modified line to the temporary file
      echo "$new_line" >> "$tmpfile"
  done < Makefile
  mv "$tmpfile" Makefile
}

# $1 - base branch
# $2 - local branch
# $3 - commit message
function prepare_local_branch() {
  echo "+++ Creating local branch $2"
  git checkout -b "$2" "${UPSTREAM_REMOTE}/$1"
  clean_branches+=("$2")

  update_version_and_branch "$RELEASE_VERSION" "$1"
  make prepare-release-branch

  # Ensure CHANGELOG dir exists
  mkdir -p "$(dirname "$CHANGELOG_FILE")"

  # Write new changelog and append existing file contents if any
  tmpfile=$(mktemp)
  {
    echo "## ${RELEASE_VERSION}"
    echo ""
    echo "${CHANGELOG}"
    echo ""
    if [ -f "${CHANGELOG_FILE}" ]; then
      cat "${CHANGELOG_FILE}"
    fi
  } > "$tmpfile"
  mv "$tmpfile" "${CHANGELOG_FILE}"

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
#### What type of PR is this?
/kind cleanup

#### What this PR does / why we need it:
$3.

#### Which issue(s) this PR fixes:
Part of #${RELEASE_ISSUE_NUMBER}

#### Special notes for your reviewer:

#### Does this PR introduce a user-facing change?
\`\`\`release-note
NONE
\`\`\`
EOF
)

   gh pr create --title="$3" --body="${pr_text}" --head "${GITHUB_USER}:$2" --base "${rel}" --repo="${MAIN_REPO_ORG}/${MAIN_REPO_NAME}"
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
  echo "+++ I'm about to do the following to push to GitHub (and I'm assuming ${FORK_REMOTE} is your personal fork):"
  echo
  echo "  git push ${FORK_REMOTE} ${3}:${2}"
  echo

  read -p "+++ Proceed (anything other than 'y' aborts it)? [y/n] " -r
  if ! [[ "${REPLY}" =~ ^[yY]$ ]]; then
    echo "Aborting." >&2
  else
    git push "${FORK_REMOTE}" -f "${3}:${2}"
    make_pr "$1" "$2" "$4"
  fi
}

PREPARE_RELEASE_BRANCH="prepare-${RELEASE_BRANCH}"
declare -r PREPARE_RELEASE_BRANCH
PREPARE_RELEASE_BRANCH_UNIQUE="${PREPARE_RELEASE_BRANCH}-$(date +%s)"
declare -r PREPARE_RELEASE_BRANCH_UNIQUE

PREPARE_RELEASE_PR_NAME="Prepare release ${RELEASE_VERSION}"
declare -r PREPARE_RELEASE_PR_NAME

prepare_local_branch "${RELEASE_BRANCH}" "${PREPARE_RELEASE_BRANCH_UNIQUE}" "${PREPARE_RELEASE_PR_NAME}"
push_and_create_pr "${RELEASE_BRANCH}" "${PREPARE_RELEASE_BRANCH}" "${PREPARE_RELEASE_BRANCH_UNIQUE}" "${PREPARE_RELEASE_PR_NAME}"

PREPARE_RELEASE_PR_NUMBER=$(gh pr list --repo="${MAIN_REPO_ORG}/${MAIN_REPO_NAME}" | grep "${PREPARE_RELEASE_PR_NAME}" | awk '{print $1}' || true)
if [ -n "$PREPARE_RELEASE_PR_NUMBER" ]; then
  NEW_RELEASE_ISSUE_BODY=${RELEASE_ISSUE_BODY//<!-- PREPARE_PULL -->/#${PREPARE_RELEASE_PR_NUMBER}}
  gh issue edit "${RELEASE_ISSUE_NUMBER}" --body "${NEW_RELEASE_ISSUE_BODY}" --repo="${MAIN_REPO_ORG}/${MAIN_REPO_NAME}" || {
    echo "!!! Failed to edit release issue \"${RELEASE_ISSUE_NAME}\": gh issue edit command failed."
  }
fi

LATEST_RELEASE_VERSION=$(git tag -l | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -n 1)

# Split into LATEST_MAJOR, LATEST_MINOR, LATEST_PATCH
latest_core_version=${LATEST_RELEASE_VERSION#v}
LATEST_MAJOR=$(echo "$latest_core_version" | cut -d. -f1)
LATEST_MINOR=$(echo "$latest_core_version" | cut -d. -f2)

if (( MAJOR > LATEST_MAJOR )) || (( MAJOR == LATEST_MAJOR && MINOR >= LATEST_MINOR )); then
  UPDATE_MAIN_WITH_LATEST_BRANCH="update-main-with-latest-${RELEASE_VERSION}"
  declare -r UPDATE_MAIN_WITH_LATEST_BRANCH
  UPDATE_MAIN_WITH_LATEST_BRANCH_UNIQUE="${UPDATE_MAIN_WITH_LATEST_BRANCH}-$(date +%s)"
  declare -r UPDATE_MAIN_WITH_LATEST_BRANCH_UNIQUE

  UPDATE_MAIN_WITH_LATEST_PR_NAME="Update main with the latest ${RELEASE_VERSION}"

  prepare_local_branch main "${UPDATE_MAIN_WITH_LATEST_BRANCH_UNIQUE}" "${UPDATE_MAIN_WITH_LATEST_PR_NAME}"
  push_and_create_pr main "${UPDATE_MAIN_WITH_LATEST_BRANCH}" "${UPDATE_MAIN_WITH_LATEST_BRANCH_UNIQUE}" "${UPDATE_MAIN_WITH_LATEST_PR_NAME}"

  git add .
  git commit -m "$PREPARE_RELEASE_PR_NAME"
fi
