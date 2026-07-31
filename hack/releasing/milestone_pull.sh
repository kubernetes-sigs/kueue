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

# Resolved without `git rev-parse` so the file stays sourceable from the test harness.
MILESTONE_PULL_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
declare -r MILESTONE_PULL_ROOT

# shellcheck source=hack/utils.sh
source "${MILESTONE_PULL_ROOT}/hack/utils.sh"

MILESTONE_RESULT="not run"
PR_RESULT="not run"
TEST_INFRA_STARTING_BRANCH=""
TEST_INFRA_WORK_BRANCH=""

function usage() {
  echo "${0} <release-version>"
  echo
  echo "  Create the next minor's milestone and the prow milestone_applier PR in test-infra."
  echo
  echo "  Example:"
  echo "    $0 v0.20.0"
  echo
  echo "  Applies to major and minor releases only; patch releases have no milestone step."
  echo
  echo "  Set the DRY_RUN environment var to skip the milestone creation, git push and PR."
  echo "  When DRY_RUN is set the script will leave you in a branch containing the commits."
  echo
  echo "  Set SKIP_MILESTONE to skip the milestone phase, or SKIP_PR to skip the pull request"
  echo "  phase. The two phases write to different repositories and need different credentials."
  echo
  echo "  Set KUBERNETES_TEST_INFRA_UPSTREAM_REMOTE (default: upstream) and KUBERNETES_TEST_INFRA_FORK_REMOTE (default: origin)"
  echo "  to override the default remote names to what you have locally."
  echo
  echo "  Set KUBERNETES_REPOS_PATH (default: ../../kubernetes) and KUBERNETES_TEST_INFRA_PATH (default: ../../kubernetes/test-infra)"
  echo "  to override the default kubernetes paths to what you have locally."
}

# derive_values populates the globals every later phase reads.
# $1 - release version, e.g. v0.20.0
function derive_values() {
  RELEASE_VERSION="$1"

  if [[ ! "${RELEASE_VERSION}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "!!! Invalid release version \"${RELEASE_VERSION}\". It should be a semantic version like v0.20.0."
    return 2
  fi

  IFS='.' read -r MAJOR MINOR PATCH <<< "${RELEASE_VERSION#v}"

  if [ "${PATCH}" -ne 0 ]; then
    echo "!!! ${RELEASE_VERSION} is a patch release. The milestone step applies only to major and minor releases. Nothing to do."
    return 2
  fi

  RELEASED_MINOR="v${MAJOR}.${MINOR}"
  NEXT_MINOR="v${MAJOR}.$((MINOR + 1))"
  RELEASE_BRANCH="release-${MAJOR}.${MINOR}"
  MILESTONE_TITLE="${NEXT_MINOR}"
  PR_TITLE="Kueue: add milestone for ${MAJOR}.${MINOR}"
  PR_BRANCH="kueue-milestone-${MAJOR}.${MINOR}"
}

# read_mapping_state prints "<main-value> <yes|no>" for the milestone_applier Kueue block.
#
# Scoped to the `milestone_applier` section because plugins.yaml carries four blocks keyed
# `kubernetes-sigs/kueue:` — the others configure repo_milestone, plugins and external_plugins.
# Exits 3 when the section, the block, or its `main` entry cannot be found, so an upstream
# restructure surfaces as a refusal rather than an edit against a guess.
#
# $1 - path to plugins.yaml
# $2 - release branch key, e.g. release-0.20
function read_mapping_state() {
  awk -v branch="$2" '
    /^[A-Za-z_][A-Za-z0-9_]*:/ {
      in_section = ($0 ~ /^milestone_applier:[[:space:]]*$/) ? 1 : 0
      in_kueue = 0
      next
    }
    in_section && /^  [^ #]/ {
      in_kueue = ($0 ~ /^  kubernetes-sigs\/kueue:[[:space:]]*$/) ? 1 : 0
      next
    }
    in_kueue && /^    main:[[:space:]]/ { main_val = $2; next }
    in_kueue && $1 == (branch ":") { has_entry = 1; next }
    END {
      if (main_val == "") exit 3
      printf "%s %s\n", main_val, (has_entry ? "yes" : "no")
    }
  ' "$1"
}

# apply_mapping_edit rewrites the `main` mapping and inserts the new release-branch entry
# directly below it, streaming every other byte through unchanged. Exits 3 when nothing
# matched, so a silent no-op can never become an empty pull request.
#
# $1 - input plugins.yaml
# $2 - output path
# $3 - next minor, e.g. v0.21
# $4 - release branch key, e.g. release-0.20
# $5 - released minor, e.g. v0.20
function apply_mapping_edit() {
  awk -v next_minor="$3" -v branch="$4" -v released="$5" '
    /^[A-Za-z_][A-Za-z0-9_]*:/ {
      in_section = ($0 ~ /^milestone_applier:[[:space:]]*$/) ? 1 : 0
      in_kueue = 0
      print
      next
    }
    in_section && /^  [^ #]/ {
      in_kueue = ($0 ~ /^  kubernetes-sigs\/kueue:[[:space:]]*$/) ? 1 : 0
      print
      next
    }
    in_kueue && /^    main:[[:space:]]/ {
      printf "    main: %s\n", next_minor
      printf "    %s: %s\n", branch, released
      edited = 1
      next
    }
    { print }
    END { if (!edited) exit 3 }
  ' "$1" > "$2"
}

# ensure_milestone creates the milestone when absent and leaves any existing one alone,
# including a closed one — reopening it would be a surprising write to state the release
# team owns. The lookup uses state=all so a closed milestone is found rather than duplicated,
# which GitHub would reject with a 422.
#
# $1 - repository, e.g. kubernetes-sigs/kueue
# $2 - milestone title, e.g. v0.21
function ensure_milestone() {
  local repo="$1" title="$2" state

  state=$(gh api "repos/${repo}/milestones?state=all" --paginate \
    | jq -r --arg t "${title}" '.[] | select(.title == $t) | .state' \
    | head -n 1)

  if [[ -n "${state}" ]]; then
    if [[ "${state}" == "closed" ]]; then
      MILESTONE_RESULT="already present (closed, left as-is)"
    else
      MILESTONE_RESULT="already present"
    fi
    echo "+++ Milestone ${title} ${MILESTONE_RESULT}."
    return 0
  fi

  if [[ -n "${DRY_RUN:-}" ]]; then
    MILESTONE_RESULT="skipped (DRY_RUN)"
    echo "!!! Skipping creation of milestone ${title} because you set DRY_RUN."
    return 0
  fi

  echo "+++ Creating milestone ${title} in ${repo}"
  gh api --method POST "repos/${repo}/milestones" -f title="${title}" > /dev/null
  MILESTONE_RESULT="created"
}

# update_release_issue fills the checklist placeholder with the created pull request.
# Failure here is reported but not fatal: the pull request already exists.
# $1 - kueue repository, $2 - test-infra repository, $3 - pull request url
function update_release_issue() {
  local pr_number="${3##*/}"
  local body new_body

  body=$(gh issue view "${RELEASE_ISSUE_NUMBER}" --repo="$1" --json body | jq -r '.body')
  new_body=${body//<!-- MILESTONE_PULL -->/$2#${pr_number}}

  if [[ "${new_body}" == "${body}" ]]; then
    echo "!!! The <!-- MILESTONE_PULL --> placeholder was not found in the release issue; leaving the body unchanged."
    return 0
  fi

  gh issue edit "${RELEASE_ISSUE_NUMBER}" --body "${new_body}" --repo="$1" || {
    echo "!!! Failed to edit release issue \"${RELEASE_ISSUE_NAME}\": gh issue edit command failed."
  }
}

# submit_mapping_pr runs the whole pull request phase against the local test-infra clone.
# $1 - kueue repository, e.g. kubernetes-sigs/kueue
function submit_mapping_pr() {
  local kueue_repo="$1"

  if [[ -z "${GITHUB_USER:-}" ]]; then
    echo "!!! Please export GITHUB_USER=<your-user> (or GH organization, if that's where your fork lives)"
    PR_RESULT="FAILED"
    exit 2
  fi

  if [[ -n "${KUBERNETES_REPOS_PATH:-}" ]]; then
    KUBERNETES_REPOS_PATH=$(resolve_path "${KUBERNETES_REPOS_PATH}")
    if [[ ! -d "${KUBERNETES_REPOS_PATH}" ]]; then
      echo "!!! Invalid value for KUBERNETES_REPOS_PATH: the path \"${KUBERNETES_REPOS_PATH}\" does not exist."
      PR_RESULT="FAILED"
      exit 2
    fi
  else
    KUBERNETES_REPOS_PATH="$(resolve_path "${MILESTONE_PULL_ROOT}/../../kubernetes")"
  fi

  if [[ -n "${KUBERNETES_TEST_INFRA_PATH:-}" ]]; then
    KUBERNETES_TEST_INFRA_PATH=$(resolve_path "${KUBERNETES_TEST_INFRA_PATH}")
  else
    KUBERNETES_TEST_INFRA_PATH="${KUBERNETES_REPOS_PATH}/test-infra"
  fi
  if [[ ! -d "${KUBERNETES_TEST_INFRA_PATH}" ]]; then
    echo "!!! The path \"${KUBERNETES_TEST_INFRA_PATH}\" does not exist."
    PR_RESULT="FAILED"
    exit 2
  fi

  local plugins_file="config/prow/plugins.yaml"

  cd "${KUBERNETES_TEST_INFRA_PATH}"

  if [[ ! -f "${plugins_file}" ]]; then
    echo "!!! ${KUBERNETES_TEST_INFRA_PATH}/${plugins_file} does not exist."
    PR_RESULT="FAILED"
    exit 2
  fi

  if git_status=$(git status --porcelain --untracked=no 2>/dev/null) && [[ -n "${git_status}" ]]; then
    echo "!!! Dirty tree in ${KUBERNETES_TEST_INFRA_PATH}. Clean up and try again."
    PR_RESULT="FAILED"
    exit 2
  fi

  local upstream_remote fork_remote test_infra_url test_infra_repo
  upstream_remote=${KUBERNETES_TEST_INFRA_UPSTREAM_REMOTE:-upstream}
  fork_remote=${KUBERNETES_TEST_INFRA_FORK_REMOTE:-origin}
  test_infra_url=$(git remote get-url "${upstream_remote}")
  test_infra_repo="$(get_repo_org "${test_infra_url}")/$(get_repo_name "${test_infra_url}")"

  echo "+++ Fetching ${upstream_remote}..."
  git fetch "${upstream_remote}"

  # A CI checkout can leave a detached HEAD, so fall back to the commit.
  TEST_INFRA_STARTING_BRANCH=$(git symbolic-ref --short HEAD 2>/dev/null || git rev-parse HEAD)

  local state main_val has_entry
  if ! state=$(read_mapping_state "${plugins_file}" "${RELEASE_BRANCH}"); then
    echo "!!! Could not locate milestone_applier -> kubernetes-sigs/kueue -> main in ${plugins_file}."
    echo "!!! The upstream file layout has changed; refusing to edit a guess."
    PR_RESULT="FAILED"
    exit 2
  fi
  read -r main_val has_entry <<< "${state}"

  if [[ "${main_val}" == "${NEXT_MINOR}" && "${has_entry}" == "yes" ]]; then
    PR_RESULT="already applied upstream"
    echo "+++ Mapping is already up to date (main: ${main_val}, ${RELEASE_BRANCH} present). Nothing to do."
    return 0
  fi

  if [[ "${main_val}" != "${RELEASED_MINOR}" ]]; then
    echo "!!! Unexpected mapping state: found main: ${main_val}, expected ${RELEASED_MINOR}."
    echo "!!! Refusing to edit. Check ${test_infra_repo} ${plugins_file} before retrying."
    PR_RESULT="FAILED"
    exit 2
  fi

  local existing_pr
  existing_pr=$(gh pr list --repo="${test_infra_repo}" --search "${PR_TITLE} in:title" --json title,url \
    | jq -r --arg t "${PR_TITLE}" '.[] | select(.title == $t) | .url' | head -n 1)
  if [[ -n "${existing_pr}" ]]; then
    PR_RESULT="already open: ${existing_pr}"
    echo "+++ A pull request for this change is already open: ${existing_pr}"
    return 0
  fi

  # The remote branch name is stable so a retry force-pushes over the previous attempt, while
  # the local name is unique so a leftover local branch never blocks one.
  TEST_INFRA_WORK_BRANCH="${PR_BRANCH}-$(date +%s)"

  echo "+++ Creating local branch ${TEST_INFRA_WORK_BRANCH}"
  git checkout -b "${TEST_INFRA_WORK_BRANCH}" "${upstream_remote}/master"

  local tmp_file
  tmp_file=$(mktemp)
  if ! apply_mapping_edit "${plugins_file}" "${tmp_file}" "${NEXT_MINOR}" "${RELEASE_BRANCH}" "${RELEASED_MINOR}"; then
    rm -f "${tmp_file}"
    echo "!!! The milestone_applier edit did not match anything in ${plugins_file}. Refusing to open an empty pull request."
    PR_RESULT="FAILED"
    exit 1
  fi
  mv "${tmp_file}" "${plugins_file}"

  git add "${plugins_file}"
  git commit -m "${PR_TITLE}"

  if [[ -n "${DRY_RUN:-}" ]]; then
    PR_RESULT="skipped (DRY_RUN), branch ${TEST_INFRA_WORK_BRANCH} left in place"
    echo "!!! Skipping git push, PR creation and issue update because you set DRY_RUN."
    return 0
  fi

  echo "+++ Pushing ${TEST_INFRA_WORK_BRANCH} to ${fork_remote} as ${PR_BRANCH}"
  git push "${fork_remote}" -f "${TEST_INFRA_WORK_BRANCH}:${PR_BRANCH}"

  local pr_text pr_url
  pr_text=$(cat <<EOF
#### What this PR does / why we need it:
${PR_TITLE}.

#### Which issue(s) this PR fixes:
Part of ${kueue_repo}#${RELEASE_ISSUE_NUMBER}
EOF
)

  echo "+++ Creating a pull request on GitHub at ${GITHUB_USER}:${PR_BRANCH} for master"
  pr_url=$(gh pr create --title="${PR_TITLE}" --body="${pr_text}" \
    --head "${GITHUB_USER}:${PR_BRANCH}" --base master --repo="${test_infra_repo}")

  PR_RESULT="${pr_url}"

  update_release_issue "${kueue_repo}" "${test_infra_repo}" "${pr_url}"
}

# on_exit restores the test-infra clone to the branch the caller started on, then prints the
# per-phase summary. Both halves are reported separately because they are not transactional:
# a created milestone survives a failed pull request, and a retry is expected to find it.
function on_exit() {
  if [[ -n "${TEST_INFRA_WORK_BRANCH}" && -z "${DRY_RUN:-}" ]]; then
    git checkout -f "${TEST_INFRA_STARTING_BRANCH}" > /dev/null 2>&1 || {
      echo "!!! Failed to return to ${TEST_INFRA_STARTING_BRANCH}. Please check your git status."
    }
    git branch -D "${TEST_INFRA_WORK_BRANCH}" > /dev/null 2>&1 || true
  fi

  echo
  echo "+++ Summary for ${RELEASE_VERSION}"
  echo "    milestone ${MILESTONE_TITLE} ......... ${MILESTONE_RESULT}"
  echo "    mapping pull request .... ${PR_RESULT}"
}

function main() {
  if [[ "$#" -ne 1 ]]; then
    usage
    exit 2
  fi

  derive_values "$1" || exit $?

  local kueue_repo
  kueue_repo="${KUBERNETES_SIGS_KUEUE_MAIN_REPO_ORG}/${KUBERNETES_SIGS_KUEUE_MAIN_REPO_NAME}"

  if ! command -v gh > /dev/null; then
    echo "!!! Can't find 'gh' tool in PATH, please install from https://github.com/cli/cli"
    exit 2
  fi

  if ! gh auth status; then
    echo "!!! gh is not authenticated. Run 'gh auth login' or export GH_TOKEN."
    exit 2
  fi

  if ! gh api "repos/${kueue_repo}/branches/${RELEASE_BRANCH}" > /dev/null 2>&1; then
    echo "!!! Branch ${RELEASE_BRANCH} does not exist in ${kueue_repo}. Create the release branch first."
    exit 2
  fi

  RELEASE_ISSUE_NAME="Release ${RELEASE_VERSION}"
  RELEASE_ISSUE_NUMBER=$(gh issue list --repo="${kueue_repo}" --search "in:title ${RELEASE_ISSUE_NAME}" | awk '{print $1}' | head -n 1 || true)
  if [ -z "${RELEASE_ISSUE_NUMBER}" ]; then
    echo "!!! No release issue found for version ${RELEASE_VERSION}. Please create '${RELEASE_ISSUE_NAME}' issue first."
    exit 2
  fi

  trap on_exit EXIT

  if [[ -n "${SKIP_MILESTONE:-}" ]]; then
    MILESTONE_RESULT="skipped (SKIP_MILESTONE)"
  else
    ensure_milestone "${kueue_repo}" "${MILESTONE_TITLE}"
  fi

  if [[ -n "${SKIP_PR:-}" ]]; then
    PR_RESULT="skipped (SKIP_PR)"
    return 0
  fi

  submit_mapping_pr "${kueue_repo}"
}

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  KUBERNETES_SIGS_KUEUE_UPSTREAM_REMOTE=${KUBERNETES_SIGS_KUEUE_UPSTREAM_REMOTE:-upstream}
  KUBERNETES_SIGS_KUEUE_MAIN_REPO_ORG=${KUBERNETES_SIGS_KUEUE_MAIN_REPO_ORG:-$(get_repo_org "$(git -C "${MILESTONE_PULL_ROOT}" remote get-url "$KUBERNETES_SIGS_KUEUE_UPSTREAM_REMOTE")")}
  KUBERNETES_SIGS_KUEUE_MAIN_REPO_NAME=${KUBERNETES_SIGS_KUEUE_MAIN_REPO_NAME:-$(get_repo_name "$(git -C "${MILESTONE_PULL_ROOT}" remote get-url "$KUBERNETES_SIGS_KUEUE_UPSTREAM_REMOTE")")}
  main "$@"
fi
