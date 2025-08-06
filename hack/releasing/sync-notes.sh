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

UPSTREAM_REMOTE=${UPSTREAM_REMOTE:-upstream}
MAIN_REPO_ORG=${MAIN_REPO_ORG:-$(git remote get-url "$UPSTREAM_REMOTE" | awk '{gsub(/http[s]:\/\/|git@/,"")}1' | awk -F'[@:./]' 'NR==1{print $3}')}
MAIN_REPO_NAME=${MAIN_REPO_NAME:-$(git remote get-url "$UPSTREAM_REMOTE" | awk '{gsub(/http[s]:\/\/|git@/,"")}1' | awk -F'[@:./]' 'NR==1{print $4}')}

if ! command -v gh > /dev/null; then
  echo "Can't find 'gh' tool in PATH, please install from https://github.com/cli/cli"
  exit 1
fi

if [[ "$#" -ne 1 ]]; then
  echo "${0} <version>"
  echo
  echo "  Sync notes on the release issue"
  echo
  echo "  Example:"
  echo "    $0 v0.13.2"
  exit 2
fi

# Checks if you are logged in. Will error/bail if you are not.
gh auth status

declare -r RELEASE_VERSION="$1"

if [[ ! "$RELEASE_VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "!!! Invalid release version. It should be semantic version like v0.13.2"
  exit 1
fi

# $1 - release version
find_previous_version() {
  local release_version="$1"
  IFS='.' read -r major minor patch <<< "${release_version#v}"
  for tag in $(git tag -l | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V -r); do
    IFS='.' read -r t_major t_minor t_patch <<< "${tag#v}"
    if [ "$t_major" -lt "$major" ] || \
       { [ "$t_major" -eq "$major" ] && [ "$t_minor" -lt "$minor" ]; } || \
       { [ "$t_major" -eq "$major" ] && [ "$t_minor" -eq "$minor" ] && [ "$t_patch" -lt "$patch" ]; }; then
      echo "$tag"
      return 0
    fi
  done
}

# $1 - release version (e.g. v0.13.1)
function find_head_branch() {
  local release_version="$1"
  local head_branch="main"

  IFS='.' read -r major minor patch <<< "${release_version#v}"

  # Get the latest release version tag (sorted semver)
  latest_release_version=$(git tag -l | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -n 1)
  IFS='.' read -r latest_major latest_minor <<< "${latest_release_version#v}"

  # Check if current major.minor is less than latest
  if [[ "$major" -lt "$latest_major" ]] || { [[ "$major" -eq "$latest_major" ]] && [[ "$minor" -lt "$latest_minor" ]]; }; then
    candidate_branch="release-${major}.${minor}"

    # Use release branch if it exists
    if git ls-remote --heads "$UPSTREAM_REMOTE" "$candidate_branch" | grep -q "$candidate_branch"; then
      head_branch="$candidate_branch"
    fi
  fi

  echo "$head_branch"
}

HEAD_BRANCH=$(find_head_branch "$RELEASE_VERSION")
declare HEAD_BRANCH

PREVIOUS_VERSION=$(find_previous_version "$RELEASE_VERSION")
declare PREVIOUS_VERSION

START_SHA=$(git rev-parse "${PREVIOUS_VERSION}^{commit}" 2>/dev/null)
declare START_SHA

END_SHA=$(git rev-parse "${UPSTREAM_REMOTE}/${HEAD_BRANCH}" 2>/dev/null)
declare END_SHA

CHANGELOG_FILE=$(mktemp)
declare CHANGELOG_FILE

echo "+++ Running release-notes:"
echo
echo "release-notes --org \"${MAIN_REPO_ORG}\" \\"
echo "              --repo \"${MAIN_REPO_NAME}\" \\"
echo "              --branch \"${HEAD_BRANCH}\" \\"
echo "              --start-sha \"${START_SHA}\" \\"
echo "              --end-sha \"${END_SHA}\" \\"
echo "              --dependencies=false \\"
echo "              --required-author=\"\" "
echo "              --output=\"${CHANGELOG_FILE}\" "

GITHUB_TOKEN=${GITHUB_TOKEN:-$(gh auth status --show-token | awk '/Token:/ {print $3}')}
declare GITHUB_TOKEN

GITHUB_TOKEN=${GITHUB_TOKEN} release-notes --org "${MAIN_REPO_ORG}" \
              --repo "${MAIN_REPO_NAME}" \
              --branch "${HEAD_BRANCH}" \
              --start-sha "${START_SHA}" \
              --end-sha "${END_SHA}" \
              --dependencies=false \
              --required-author="" \
              --output="${CHANGELOG_FILE}"

cat "$CHANGELOG_FILE"

echo
echo
read -p "+++ Do you want to update release issue? [y/N] " -r
if ! [[ "${REPLY}" =~ ^[yY]$ ]]; then
  echo "Aborting." >&2
  exit 1
fi

RELEASE_ISSUE_NAME="Release ${RELEASE_VERSION}"
declare RELEASE_ISSUE_NAME

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

NEW_RELEASE_ISSUE_BODY=$(awk -v previous_version="$PREVIOUS_VERSION" -v changelog_file="$CHANGELOG_FILE" '
  BEGIN {
    in_block=0
    while ((getline line < changelog_file) > 0) {
      changelog_lines = changelog_lines line "\n"
    }
    close(changelog_file)
  }
  /^```markdown$/ {
    print $0
    printf "Changes since `%s`:\n\n", previous_version
    print changelog_lines
    in_block=1
    next
  }
  /^```$/ && in_block {
    print $0
    in_block=0
    next
  }
  !in_block {
    print $0
  }
' <<< "$RELEASE_ISSUE_BODY")

gh issue edit "${RELEASE_ISSUE_NUMBER}" --body "${NEW_RELEASE_ISSUE_BODY}" --repo="${MAIN_REPO_ORG}/${MAIN_REPO_NAME}"