#!/usr/bin/env bash

# Copyright 2022 The Kubernetes Authors.
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

# Usage Instructions: https://git.k8s.io/community/contributors/devel/sig-release/cherry-picks.md

# Checkout a PR from GitHub. (Yes, this is sitting in a Git tree. How
# meta.) Assumes you care about pulls from remote "upstream" and
# checks them out to a branch named:
#  automated-cherry-pick-of-<pr>-<target branch>-<timestamp>
#
# Each PR is cherry-picked as a single squashed commit (the PR's net change,
# built with 'git commit-tree' and applied with 'git cherry-pick --no-commit')
# rather than replaying every commit with 'git am', so conflicts for large
# multi-commit PRs are resolved once instead of per commit.
#
# If this script misbehaves, hack/legacy_cherry_pick_pull.sh preserves the
# original per-commit ('git am') behavior as a fallback.

set -o errexit
set -o nounset
set -o pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
declare -r REPO_ROOT
cd "${REPO_ROOT}"

STARTINGBRANCH=$(git symbolic-ref --short HEAD)
declare -r STARTINGBRANCH
declare -r REBASEMAGIC="${REPO_ROOT}/.git/rebase-apply"
DRY_RUN=${DRY_RUN:-""}
REGENERATE_DOCS=${REGENERATE_DOCS:-""}
UPSTREAM_REMOTE=${UPSTREAM_REMOTE:-upstream}
FORK_REMOTE=${FORK_REMOTE:-origin}
# The owner and repo are the last two parts of the URL, whatever its form.
upstream_url=$(git remote get-url "$UPSTREAM_REMOTE")
upstream_url=${upstream_url%/}
upstream_url=${upstream_url%.git}
MAIN_REPO_NAME=${MAIN_REPO_NAME:-${upstream_url##*[/:]}}
upstream_url=${upstream_url%[/:]*}
MAIN_REPO_ORG=${MAIN_REPO_ORG:-${upstream_url##*[/:]}}

if [[ -z ${GITHUB_USER:-} ]]; then
  echo "Please export GITHUB_USER=<your-user> (or GH organization, if that's where your fork lives)"
  exit 1
fi

if ! command -v gh > /dev/null; then
  echo "Can't find 'gh' tool in PATH, please install from https://github.com/cli/cli"
  exit 1
fi

if [[ "$#" -lt 2 ]]; then
  echo "${0} <remote branch> <pr-number>...: cherry pick one or more <pr> onto <remote branch> and leave instructions for proposing pull request"
  echo
  echo "  Checks out <remote branch> and handles the cherry-pick of <pr> (possibly multiple) for you."
  echo "  Examples:"
  echo "    $0 upstream/release-3.14 12345        # Cherry-picks PR 12345 onto upstream/release-3.14 and proposes that as a PR."
  echo "    $0 upstream/release-3.14 12345 56789  # Cherry-picks PR 12345, then 56789 and proposes the combination as a single PR."
  echo
  echo "  Set the DRY_RUN environment var to skip git push and creating PR."
  echo "  This is useful for creating patches to a release branch without making a PR."
  echo "  When DRY_RUN is set the script will leave you in a branch containing the commits you cherry-picked."
  echo
  echo "  Set the REGENERATE_DOCS environment var to regenerate documentation for the target branch after picking the specified commits."
  echo "  This is useful when picking commits containing changes to API documentation."
  echo
  echo "  Set UPSTREAM_REMOTE (default: upstream) and FORK_REMOTE (default: origin)"
  echo "  to override the default remote names to what you have locally."
  echo
  echo "  For merge process info, see https://git.k8s.io/community/contributors/devel/sig-release/cherry-picks.md"
  exit 2
fi

# Checks if you are logged in. Will error/bail if you are not.
gh auth status

if git_status=$(git status --porcelain --untracked=no 2>/dev/null) && [[ -n "${git_status}" ]]; then
  echo "!!! Dirty tree. Clean up and try again."
  exit 1
fi

if [[ -e "${REBASEMAGIC}" ]]; then
  echo "!!! 'git rebase' or 'git am' in progress. Clean up and try again."
  exit 1
fi

declare -r BRANCH="$1"
shift 1
declare -r PULLS=( "$@" )

function join { local IFS="$1"; shift; echo "$*"; }
PULLDASH=$(join - "${PULLS[@]/#/#}") # Generates something like "#12345-#56789"
declare -r PULLDASH
PULLSUBJ=$(join " " "${PULLS[@]/#/#}") # Generates something like "#12345 #56789"
declare -r PULLSUBJ

echo "+++ Updating remotes..."
git remote update "${UPSTREAM_REMOTE}" "${FORK_REMOTE}"

if ! git log -n1 --format=%H "${BRANCH}" >/dev/null 2>&1; then
  echo "!!! '${BRANCH}' not found. The second argument should be something like ${UPSTREAM_REMOTE}/release-0.21."
  echo "    (In particular, it needs to be a valid, existing remote branch that I can 'git checkout'.)"
  exit 1
fi

NEWBRANCHREQ="automated-cherry-pick-of-${PULLDASH}" # "Required" portion for tools.
declare -r NEWBRANCHREQ
NEWBRANCH="$(echo "${NEWBRANCHREQ}-${BRANCH}" | sed 's/\//-/g')"
declare -r NEWBRANCH
NEWBRANCHUNIQ="${NEWBRANCH}-$(date +%s)"
declare -r NEWBRANCHUNIQ
echo "+++ Creating local branch ${NEWBRANCHUNIQ}"

cleanbranch=""
pickcleanup=false
function return_to_kansas {
  if [[ "${pickcleanup}" == "true" ]]; then
    echo
    echo "+++ Aborting in-progress squashed cherry-pick."
    git reset -q --hard HEAD >/dev/null 2>&1 || true
  fi

  # return to the starting branch and delete the PR text file
  if [[ -z "${DRY_RUN}" ]]; then
    echo
    echo "+++ Returning you to the ${STARTINGBRANCH} branch and cleaning up."
    git checkout -f "${STARTINGBRANCH}" >/dev/null 2>&1 || true
    if [[ -n "${cleanbranch}" ]]; then
      git branch -D "${cleanbranch}" >/dev/null 2>&1 || true
    fi
  fi
}
trap return_to_kansas EXIT

SUBJECTS=()
RELEASE_NOTES=()
KIND_LABELS=()
function make-a-pr() {
  local rel
  rel="$(basename "${BRANCH}")"
  echo
  echo "+++ Creating a pull request on GitHub at ${GITHUB_USER}:${NEWBRANCH}"

  local numandtitle
  numandtitle=$(printf '%s\n' "${SUBJECTS[@]}")

  local kind_commands=""
  if [[ ${#KIND_LABELS[@]} -gt 0 ]]; then
    while IFS= read -r label; do
      if [[ -n "${label}" ]]; then
        kind_commands+="/kind ${label#kind/}"$'\n'
      fi
    done < <(printf '%s\n' "${KIND_LABELS[@]}" | sort -u)
  fi

  prtext=$(cat <<EOF
Cherry pick of ${PULLSUBJ} on ${rel}.

${numandtitle}

For details on the cherry pick process, see the [cherry pick requests](https://git.k8s.io/community/contributors/devel/sig-release/cherry-picks.md) page.

#### What type of PR is this?
${kind_commands}

\`\`\`release-note
$(printf '%s\n' "${RELEASE_NOTES[@]}")
\`\`\`
EOF
)

  gh pr create --title="Automated cherry pick of ${numandtitle}" --body="${prtext}" --head "${GITHUB_USER}:${NEWBRANCH}" --base "${rel}" --repo="${MAIN_REPO_ORG}/${MAIN_REPO_NAME}"
}

git checkout -b "${NEWBRANCHUNIQ}" "${BRANCH}"
cleanbranch="${NEWBRANCHUNIQ}"

pickcleanup=true
for pull in "${PULLS[@]}"; do
  # Read every PR field in one call, as plain JSON even with forced color or TTY.
  pr_json=$(CLICOLOR_FORCE=0 GH_FORCE_TTY='' gh pr view "$pull" --repo="${MAIN_REPO_ORG}/${MAIN_REPO_NAME}" --json title,baseRefOid,author,commits,body,labels)
  # set the subject first so we can use it as the squashed commit message
  subject=$(jq -r '.title' <<<"${pr_json}")
  SUBJECTS+=("#${pull}: ${subject}")

  echo "+++ Fetching PR ${pull} head"
  git fetch "${UPSTREAM_REMOTE}" "pull/${pull}/head"
  PR_HEAD=$(git rev-parse FETCH_HEAD)
  # A shallow clone can silently truncate 'git log PR_BASE..PR_HEAD' (dropping
  # Co-authored-by trailers) and make 'git merge-base' fail. Unshallow before
  # either is used, and fail loudly if the fetch does not complete.
  if [[ "$(git rev-parse --is-shallow-repository)" == "true" ]]; then
    git fetch --unshallow "${UPSTREAM_REMOTE}" || {
      echo "!!! Repository is shallow and 'git fetch --unshallow' failed. Aborting." >&2
      exit 1
    }
  fi
  # Squash only the PR's own changes: compute the fork point against the PR's
  # own base commit, not an assumed default branch. A PR based on a release
  # branch (e.g. a cherry-pick into release-0.15 now going to release-0.14)
  # would otherwise merge-base against main and pull in the whole release
  # history. baseRefOid stays fetchable by SHA even after the base branch is
  # deleted.
  pr_base_oid=$(jq -r '.baseRefOid' <<<"${pr_json}")
  git fetch "${UPSTREAM_REMOTE}" "${pr_base_oid}"
  PR_BASE=$(git merge-base "${PR_HEAD}" "${pr_base_oid}")

  # Squash the PR's changes into one commit (tree = PR_HEAD, parent = PR_BASE),
  # then cherry-pick it onto the release branch. cherry-pick uses the merge
  # machinery rather than 'git apply', so a delete/modify conflict - the target
  # deleted a file the PR edits, or the PR deletes a file the target edits - is
  # recorded as an unmerged path the user can resolve, not a hard apply failure.
  # It also handles binary files natively, so no diff-flag tuning is needed.
  squashed_commit=$(git commit-tree "${PR_HEAD}^{tree}" -p "${PR_BASE}" -m "${subject}")
  echo
  echo "+++ About to cherry-pick the squashed PR. To reattempt:"
  echo "  $ git cherry-pick --no-rerere-autoupdate --no-commit ${squashed_commit}"
  echo
  pick_base=$(git rev-parse HEAD)
  # Ignore rerere.autoupdate so a reused resolution still stops at the prompt.
  if ! git cherry-pick --no-rerere-autoupdate --no-commit "${squashed_commit}"; then
    # --diff-filter=U catches every unmerged state (UU, AA, DD, AU, DU, ...),
    # unlike 'grep ^U' which misses AA/DD/AU/DU. A cherry-pick that fails without
    # leaving conflicts is a hard error, not a resolve prompt: abort before it
    # can commit an incomplete tree.
    if [[ -z "$(git diff --name-only --diff-filter=U)" ]]; then
      git cherry-pick --quit
      echo "!!! Cherry-pick failed for PR ${pull} with no conflicts to resolve. Aborting." >&2
      exit 1
    fi
    while [[ -n "$(git diff --name-only --diff-filter=U)" ]]; do
      echo
      echo "+++ Conflicts detected:"
      echo
      # --no-pager: a straight 'git diff' starts the pager, and with a LESS that
      # lacks -F/-X the list is cleared on quit and never stays on screen.
      git --no-pager diff --name-only --diff-filter=U
      echo
      echo "+++ Please resolve the conflicts in another window (and remember to 'git add'/'git rm' resolved files)."
      read -p "+++ Proceed (anything other than 'y' aborts the cherry-pick)? [y/n] " -r
      echo
      if ! [[ "${REPLY}" =~ ^[yY]$ ]]; then
        git cherry-pick --quit
        echo "Aborting." >&2
        exit 1
      fi
    done
  fi

  # Record the resolution for rerere before --quit drops its state.
  git rerere
  # Clear the cherry-pick sequencer state while keeping the staged changes.
  git cherry-pick --quit
  # A resolution committed in the other window moved HEAD; stage it again.
  if [[ "$(git symbolic-ref -q --short HEAD)" == "${NEWBRANCHUNIQ}" ]] &&
    git merge-base --is-ancestor "${pick_base}" HEAD; then
    git reset -q --soft "${pick_base}"
  fi

  # A cherry-pick that left nothing staged and no conflicts is a real error,
  # not something to commit.
  if git diff --cached --quiet; then
    echo "!!! Nothing staged for PR ${pull}; the cherry-pick produced no changes. Aborting."
    exit 1
  fi

  # Author the squashed commit as the PR author, mirroring GitHub's squash-merge
  # on main (not whoever runs the script). Resolve the PR author's name/email
  # from the PR commit whose GitHub login matches the PR author; fall back to the
  # oldest commit's author when no commit email maps to that login.
  pr_author_login=$(jq -r '.author.login' <<<"${pr_json}")
  author=$(jq -r "[.commits[].authors[] | select(.login == \"${pr_author_login}\") | \"\(.name) <\(.email)>\"] | first // empty" <<<"${pr_json}")
  # Keep log.showSignature output out of the commit in the git log calls below.
  if [[ -z "${author}" ]]; then
    # 'sed -n 1p' (not 'head -n1') reads the whole stream: 'head' closes the
    # pipe after one line, so on a large range 'git log' gets SIGPIPE and
    # 'pipefail'/'errexit' would abort before the commit.
    author=$(git log --no-show-signature --no-merges --reverse --format='%an <%ae>' "${PR_BASE}..${PR_HEAD}" | sed -n '1p')
  fi

  # Number of the PR's own commits (merges excluded).
  commit_count=$(git rev-list --no-merges --count "${PR_BASE}..${PR_HEAD}")

  # Every other commit author, plus any existing Co-authored-by trailers, becomes
  # a Co-authored-by trailer so GitHub still credits them; the PR author is the
  # commit author, not a co-author.
  # 'awk' (not 'grep') filters so a PR with no co-authors yields an empty result
  # without a non-zero exit that 'set -o pipefail'/'errexit' would turn into an
  # abort. '|| exit 1' on each 'git log' still aborts on a real history failure
  # rather than silently dropping authors.
  coauthors=$(
    {
      git log --no-show-signature --no-merges --format='Co-authored-by: %an <%ae>' "${PR_BASE}..${PR_HEAD}" || exit 1
      git log --no-show-signature --no-merges --format='%(trailers:key=Co-authored-by)' "${PR_BASE}..${PR_HEAD}" || exit 1
    } | awk -v primary="Co-authored-by: ${author}" '
      tolower($0) ~ /^co-authored-by:/ && tolower($0) != tolower(primary)
    ' | sort -u
  )

  if [[ "${commit_count}" -eq 1 ]]; then
    # Single-commit PR: keep the original commit message verbatim.
    message=$(git log --no-show-signature --no-merges --format='%B' "${PR_BASE}..${PR_HEAD}")
  else
    # Multi-commit PR: the PR title is the subject (a leading '#<pull>:' would be
    # dropped as a comment on cleanup, and bare '#' refs are disallowed in Kueue
    # commit messages), with each commit's subject and body kept as a bullet.
    commit_bodies=$(git log --no-show-signature --no-merges --reverse --format='* %s%n%w(0,2,2)%b' "${PR_BASE}..${PR_HEAD}")
    message=$(printf '%s\n\n%s' "${subject}" "${commit_bodies}")
  fi

  # Merge the collected Co-authored-by trailers into the message's existing
  # trailer block via 'git interpret-trailers' rather than appending a second
  # block: a verbatim single-commit message may already carry a Co-authored-by,
  # and a separate block would duplicate authors and make '--parse' drop the
  # original trailers (e.g. Signed-off-by). 'addIfDifferent' skips exact dupes.
  if [[ -n "${coauthors}" ]]; then
    trailer_args=()
    while IFS= read -r line; do
      [[ -n "${line}" ]] && trailer_args+=(--trailer "${line}")
    done <<< "${coauthors}"
    # --no-divider: without it 'git interpret-trailers' treats a bare '---' line
    # as end-of-input and would add trailers above it, duplicating a trailer a
    # single-commit message already carries below the divider.
    message=$(printf '%s\n' "${message}" | git interpret-trailers --no-divider --if-exists addIfDifferent --if-missing add "${trailer_args[@]}")
  fi

  # Create a single commit for the whole PR. '--author' sets the PR author; '-s'
  # adds the running user's Signed-off-by to record who did the cherry-pick
  # (the per-commit sign-offs are lost on squash).
  printf '%s\n' "${message}" | git commit -s --author="${author}" --file=- # -m fails at 128 KiB

  # set the release note
  release_note=$(jq -r '.body' <<<"${pr_json}" | awk '/```release-note/{f=1;next} /```/{f=0} f')
  RELEASE_NOTES+=("${release_note}")

  # collect kind/* labels from the original PR
  while IFS= read -r label; do
    if [[ -n "${label}" ]]; then
      KIND_LABELS+=("${label}")
    fi
  done < <(jq -r '.labels[].name | select(startswith("kind/"))' <<<"${pr_json}")
done
pickcleanup=false

# Re-generate docs (if needed)
if [[ -n "${REGENERATE_DOCS}" ]]; then
  echo
  echo "Regenerating docs..."
  if ! hack/generate-docs.sh; then
    echo
    echo "hack/generate-docs.sh FAILED to complete."
    exit 1
  fi
fi

if [[ -n "${DRY_RUN}" ]]; then
  echo "!!! Skipping git push and PR creation because you set DRY_RUN."
  echo "To return to the branch you were in when you invoked this script:"
  echo
  echo "  git checkout ${STARTINGBRANCH}"
  echo
  echo "To delete this branch:"
  echo
  echo "  git branch -D ${NEWBRANCHUNIQ}"
  exit 0
fi

if git remote -v | grep ^"${FORK_REMOTE}" | grep "${MAIN_REPO_ORG}/${MAIN_REPO_NAME}.git"; then
  echo "!!! You have ${FORK_REMOTE} configured as your ${MAIN_REPO_ORG}/${MAIN_REPO_NAME}.git"
  echo "This isn't normal. Leaving you with push instructions:"
  echo
  echo "+++ First manually push the branch this script created:"
  echo
  echo "  git push REMOTE ${NEWBRANCHUNIQ}:${NEWBRANCH}"
  echo
  echo "where REMOTE is your personal fork (maybe ${UPSTREAM_REMOTE}? Consider swapping those.)."
  echo "OR consider setting UPSTREAM_REMOTE and FORK_REMOTE to different values."
  echo
  make-a-pr
  cleanbranch=""
  exit 0
fi

echo
echo "+++ I'm about to do the following to push to GitHub (and I'm assuming ${FORK_REMOTE} is your personal fork):"
echo
echo "  git push ${FORK_REMOTE} ${NEWBRANCHUNIQ}:${NEWBRANCH}"
echo
read -p "+++ Proceed (anything other than 'y' aborts the cherry-pick)? [y/n] " -r
if ! [[ "${REPLY}" =~ ^[yY]$ ]]; then
  echo "Aborting." >&2
  exit 1
fi

git push "${FORK_REMOTE}" -f "${NEWBRANCHUNIQ}:${NEWBRANCH}"
make-a-pr
