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

test_dir=$(mktemp -d)
trap 'rm -rf "${test_dir}"' EXIT

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
export ROOT_DIR
export PATH="${test_dir}:$PATH"

# The gh stub records every invocation and answers from a per-case fixture file, so the
# milestone logic is exercised without touching GitHub.
cat >"${test_dir}/gh" <<'EOF'
#!/usr/bin/env bash
set -o errexit
set -o nounset
set -o pipefail

printf '%s\n' "$*" >>"${GH_FAKE_LOG:?}"

if [[ -n "${GH_FAKE_FAIL:-}" && "$*" == *"${GH_FAKE_FAIL}"* ]]; then
  exit 1
fi

case "$*" in
  *"milestones?state=all"*)
    cat "${GH_FAKE_MILESTONES:?}"
    ;;
  *"issue list"*)
    printf '%s' "${GH_FAKE_ISSUES:-}"
    ;;
  *)
    ;;
esac
EOF
chmod +x "${test_dir}/gh"

failures=0
current_case=""

function start_case() {
  current_case="$1"
}

function fail() {
  echo "FAIL: ${current_case}: $1" >&2
  failures=$((failures + 1))
}

function assert_eq() {
  local want="$1" got="$2" what="$3"
  if [[ "${want}" != "${got}" ]]; then
    fail "${what}: want [${want}], got [${got}]"
  fi
}

function assert_contains() {
  local haystack="$1" needle="$2" what="$3"
  if [[ "${haystack}" != *"${needle}"* ]]; then
    fail "${what}: [${haystack}] does not contain [${needle}]"
  fi
}

function assert_not_contains() {
  local haystack="$1" needle="$2" what="$3"
  if [[ "${haystack}" == *"${needle}"* ]]; then
    fail "${what}: [${haystack}] contains [${needle}]"
  fi
}

function write_plugins_fixture() {
  local path="$1" main_value="$2" extra_entry="${3:-}"
  {
    cat <<'EOF'
# Comment above an unrelated section.
approve:
  - repos:
      - kubernetes-sigs/some-other-project
    require_self_approval: false

milestone_applier:
  kubernetes-sigs/jobset:
    main: v0.2
    release-0.1: v0.1
  kubernetes-sigs/kueue:
EOF
    printf '    main: %s\n' "${main_value}"
    if [[ -n "${extra_entry}" ]]; then
      printf '    %s\n' "${extra_entry}"
    fi
    cat <<'EOF'
    release-0.19: v0.19
    release-0.18: v0.18
    release-0.9: v0.9
  kubernetes-sigs/kustomize:
    master: v5.0

repo_milestone:
  kubernetes-sigs/kueue:
    maintainers_team: kueue-maintainers
    maintainers_friendly_name: Kueue Maintainers

plugins:
  kubernetes-sigs/kueue:
    plugins:
      - approve
      - lgtm

external_plugins:
  kubernetes-sigs/kueue:
    - name: cherrypicker
      events:
        - issue_comment
EOF
  } >"${path}"
}

# shellcheck source=hack/releasing/milestone_pull.sh
source "${ROOT_DIR}/hack/releasing/milestone_pull.sh"

# --- derive_values ---------------------------------------------------------

start_case "derive_values on a minor release"
derive_values "v0.20.0"
assert_eq "v0.20" "${RELEASED_MINOR}" "released minor"
assert_eq "v0.21" "${NEXT_MINOR}" "next minor"
assert_eq "release-0.20" "${RELEASE_BRANCH}" "release branch"
assert_eq "v0.21" "${MILESTONE_TITLE}" "milestone title"
assert_eq "Kueue: add milestone for 0.20" "${PR_TITLE}" "pr title"
assert_eq "kueue-milestone-0.20" "${PR_BRANCH}" "pr branch"

start_case "derive_values rolls the major boundary"
derive_values "v1.9.0"
assert_eq "v1.10" "${NEXT_MINOR}" "next minor across a two-digit roll"

start_case "derive_values rejects a patch release"
rc=0
derive_values "v0.20.3" >/dev/null || rc=$?
assert_eq "2" "${rc}" "exit code for a patch release"

start_case "derive_values rejects a non-semver version"
rc=0
derive_values "0.20" >/dev/null || rc=$?
assert_eq "2" "${rc}" "exit code for a malformed version"

start_case "derive_values rejects a release candidate"
rc=0
derive_values "v0.20.0-rc.1" >/dev/null || rc=$?
assert_eq "2" "${rc}" "exit code for a pre-release version"

# --- select_phases ---------------------------------------------------------

# A release manager running the suite may have these exported from a real release.
unset CREATE_MILESTONE SKIP_PR DRY_RUN GITHUB_USER

function selected_phases() {
  local rc=0
  select_phases "$1" "$2" >/dev/null || rc=$?
  if [[ "${rc}" -ne 0 ]]; then
    echo "rejected (${rc})"
    return 0
  fi
  echo "milestone=${RUN_MILESTONE:-no} pr=${RUN_PR:-no}"
}

start_case "select_phases keeps the existing behaviour when no flag is given"
assert_eq "milestone=no pr=yes" "$(selected_phases "" "")" "default"
assert_eq "milestone=yes pr=yes" "$(CREATE_MILESTONE=1 selected_phases "" "")" "CREATE_MILESTONE"
assert_eq "milestone=no pr=no" "$(SKIP_PR=1 selected_phases "" "")" "SKIP_PR"
assert_eq "milestone=yes pr=no" "$(CREATE_MILESTONE=1 SKIP_PR=1 selected_phases "" "")" "CREATE_MILESTONE and SKIP_PR"

start_case "select_phases runs exactly the flagged phases"
assert_eq "milestone=yes pr=no" "$(selected_phases yes "")" "--create-milestone"
assert_eq "milestone=no pr=yes" "$(selected_phases "" yes)" "--create-pr"
assert_eq "milestone=yes pr=yes" "$(selected_phases yes yes)" "both flags"

start_case "select_phases accepts environment variables that agree with the flags"
assert_eq "milestone=yes pr=no" "$(SKIP_PR=1 selected_phases yes "")" "--create-milestone with SKIP_PR"
assert_eq "milestone=yes pr=no" "$(CREATE_MILESTONE=1 selected_phases yes "")" "--create-milestone with CREATE_MILESTONE"
assert_eq "milestone=yes pr=yes" "$(CREATE_MILESTONE=1 selected_phases yes yes)" "both flags with CREATE_MILESTONE"

start_case "select_phases rejects environment variables that contradict the flags"
assert_eq "rejected (2)" "$(CREATE_MILESTONE=1 selected_phases "" yes)" "--create-pr with CREATE_MILESTONE"
assert_eq "rejected (2)" "$(SKIP_PR=1 selected_phases "" yes)" "--create-pr with SKIP_PR"
assert_eq "rejected (2)" "$(SKIP_PR=1 selected_phases yes yes)" "both flags with SKIP_PR"
assert_contains "$(CREATE_MILESTONE=1 select_phases "" yes || true)" "CREATE_MILESTONE" "message names the variable"
assert_contains "$(SKIP_PR=1 select_phases "" yes || true)" "SKIP_PR" "message names the variable"

start_case "select_phases reports why a phase will not run"
MILESTONE_RESULT="not run" PR_RESULT="not run"
select_phases "" "" >/dev/null
assert_eq "skipped (use /create-milestone, or set CREATE_MILESTONE)" "${MILESTONE_RESULT}" "milestone by default"
MILESTONE_RESULT="not run" PR_RESULT="not run"
SKIP_PR=1 select_phases "" "" >/dev/null
assert_eq "skipped (SKIP_PR)" "${PR_RESULT}" "pull request with SKIP_PR"
MILESTONE_RESULT="not run" PR_RESULT="not run"
select_phases yes "" >/dev/null
assert_eq "not run" "${MILESTONE_RESULT}" "milestone with --create-milestone"
assert_eq "not requested (--create-milestone)" "${PR_RESULT}" "pull request with --create-milestone"
MILESTONE_RESULT="not run" PR_RESULT="not run"
select_phases "" yes >/dev/null
assert_eq "not requested (--create-pr)" "${MILESTONE_RESULT}" "milestone with --create-pr"
assert_eq "not run" "${PR_RESULT}" "pull request with --create-pr"

# --- read_mapping_state ----------------------------------------------------

start_case "read_mapping_state before the change"
write_plugins_fixture "${test_dir}/before.yaml" "v0.20"
assert_eq "v0.20 no" "$(read_mapping_state "${test_dir}/before.yaml" "release-0.20")" "state before"

start_case "read_mapping_state after the change"
write_plugins_fixture "${test_dir}/after.yaml" "v0.21" "release-0.20: v0.20"
assert_eq "v0.21 yes" "$(read_mapping_state "${test_dir}/after.yaml" "release-0.20")" "state after"

start_case "read_mapping_state on the partially applied state"
write_plugins_fixture "${test_dir}/partial.yaml" "v0.20" "release-0.20: v0.20"
assert_eq "v0.20 yes" "$(read_mapping_state "${test_dir}/partial.yaml" "release-0.20")" "state partially applied"

start_case "read_mapping_state ignores the decoy blocks"
cat >"${test_dir}/no-section.yaml" <<'EOF'
repo_milestone:
  kubernetes-sigs/kueue:
    maintainers_team: kueue-maintainers
plugins:
  kubernetes-sigs/kueue:
    main: v9.9
EOF
rc=0
read_mapping_state "${test_dir}/no-section.yaml" "release-0.20" >/dev/null || rc=$?
assert_eq "3" "${rc}" "exit code when milestone_applier is absent"

start_case "read_mapping_state fails when the kueue block is absent"
cat >"${test_dir}/no-kueue.yaml" <<'EOF'
milestone_applier:
  kubernetes-sigs/jobset:
    main: v0.2
EOF
rc=0
read_mapping_state "${test_dir}/no-kueue.yaml" "release-0.20" >/dev/null || rc=$?
assert_eq "3" "${rc}" "exit code when the kueue block is absent"

start_case "read_mapping_state fails when main is absent"
cat >"${test_dir}/no-main.yaml" <<'EOF'
milestone_applier:
  kubernetes-sigs/kueue:
    release-0.19: v0.19
EOF
rc=0
read_mapping_state "${test_dir}/no-main.yaml" "release-0.20" >/dev/null || rc=$?
assert_eq "3" "${rc}" "exit code when main is absent"

# --- apply_mapping_edit ----------------------------------------------------

start_case "apply_mapping_edit changes exactly two lines"
write_plugins_fixture "${test_dir}/edit-in.yaml" "v0.20"
apply_mapping_edit "${test_dir}/edit-in.yaml" "${test_dir}/edit-out.yaml" "v0.21" "release-0.20" "v0.20" "no"

removed=$(diff "${test_dir}/edit-in.yaml" "${test_dir}/edit-out.yaml" | grep -c '^<' || true)
added=$(diff "${test_dir}/edit-in.yaml" "${test_dir}/edit-out.yaml" | grep -c '^>' || true)
assert_eq "1" "${removed}" "lines removed"
assert_eq "2" "${added}" "lines added"

assert_contains "$(cat "${test_dir}/edit-out.yaml")" "    main: v0.21" "bumped main entry"
assert_contains "$(cat "${test_dir}/edit-out.yaml")" "    release-0.20: v0.20" "inserted release entry"

start_case "apply_mapping_edit defaults to inserting when the state is not passed"
write_plugins_fixture "${test_dir}/default-in.yaml" "v0.20"
apply_mapping_edit "${test_dir}/default-in.yaml" "${test_dir}/default-out.yaml" "v0.21" "release-0.20" "v0.20"
assert_contains "$(cat "${test_dir}/default-out.yaml")" "    release-0.20: v0.20" "entry inserted with the default"

start_case "apply_mapping_edit inserts directly below main"
context=$(grep -A 2 '^  kubernetes-sigs/kueue:$' "${test_dir}/edit-out.yaml" | head -n 3 | tail -n 2)
assert_eq "    main: v0.21
    release-0.20: v0.20" "${context}" "insertion position"

start_case "apply_mapping_edit does not duplicate an existing release entry"
write_plugins_fixture "${test_dir}/partial-in.yaml" "v0.20" "release-0.20: v0.20"
apply_mapping_edit "${test_dir}/partial-in.yaml" "${test_dir}/partial-out.yaml" "v0.21" "release-0.20" "v0.20" "yes"

assert_eq "1" "$(grep -c '^    release-0.20: v0.20$' "${test_dir}/partial-out.yaml")" "release-0.20 key count"
assert_contains "$(cat "${test_dir}/partial-out.yaml")" "    main: v0.21" "main still bumped"

removed=$(diff "${test_dir}/partial-in.yaml" "${test_dir}/partial-out.yaml" | grep -c '^<' || true)
added=$(diff "${test_dir}/partial-in.yaml" "${test_dir}/partial-out.yaml" | grep -c '^>' || true)
assert_eq "1" "${removed}" "lines removed on the partial state"
assert_eq "1" "${added}" "lines added on the partial state"

start_case "apply_mapping_edit leaves the decoy blocks untouched"
for section in repo_milestone plugins external_plugins; do
  want=$(sed -n "/^${section}:/,/^[a-z_]*:$/p" "${test_dir}/edit-in.yaml")
  got=$(sed -n "/^${section}:/,/^[a-z_]*:$/p" "${test_dir}/edit-out.yaml")
  assert_eq "${want}" "${got}" "${section} block unchanged"
done

start_case "apply_mapping_edit leaves the sibling project untouched"
assert_contains "$(cat "${test_dir}/edit-out.yaml")" "  kubernetes-sigs/jobset:
    main: v0.2" "jobset mapping unchanged"

start_case "apply_mapping_edit fails when nothing matches"
rc=0
apply_mapping_edit "${test_dir}/no-section.yaml" "${test_dir}/never.yaml" "v0.21" "release-0.20" "v0.20" "no" || rc=$?
assert_eq "3" "${rc}" "exit code when the edit does not fire"

# --- ensure_milestone ------------------------------------------------------

export GH_FAKE_LOG="${test_dir}/gh.log"
export GH_FAKE_MILESTONES="${test_dir}/milestones.json"

function reset_gh_stub() {
  : >"${GH_FAKE_LOG}"
  printf '%s' "$1" >"${GH_FAKE_MILESTONES}"
  MILESTONE_RESULT="not run"
}

start_case "ensure_milestone creates an absent milestone"
reset_gh_stub '[{"title":"v0.20","state":"closed"}]'
ensure_milestone "kubernetes-sigs/kueue" "v0.21" >/dev/null
assert_eq "created" "${MILESTONE_RESULT}" "result"
assert_contains "$(cat "${GH_FAKE_LOG}")" "--method POST repos/kubernetes-sigs/kueue/milestones -f title=v0.21" "create call"

start_case "ensure_milestone leaves an existing open milestone alone"
reset_gh_stub '[{"title":"v0.21","state":"open"}]'
ensure_milestone "kubernetes-sigs/kueue" "v0.21" >/dev/null
assert_eq "already present" "${MILESTONE_RESULT}" "result"
if grep -q "POST" "${GH_FAKE_LOG}"; then
  fail "a POST was issued for an existing milestone"
fi

start_case "ensure_milestone leaves a closed milestone closed"
reset_gh_stub '[{"title":"v0.21","state":"closed"}]'
ensure_milestone "kubernetes-sigs/kueue" "v0.21" >/dev/null
assert_eq "already present (closed, left as-is)" "${MILESTONE_RESULT}" "result"
if grep -qE "POST|PATCH|DELETE" "${GH_FAKE_LOG}"; then
  fail "a mutating call was issued for a closed milestone"
fi

start_case "ensure_milestone matches titles exactly, not by prefix"
reset_gh_stub '[{"title":"v0.2","state":"open"},{"title":"v0.21-rc","state":"open"}]'
ensure_milestone "kubernetes-sigs/kueue" "v0.21" >/dev/null
assert_eq "created" "${MILESTONE_RESULT}" "result when only a prefix match exists"

start_case "ensure_milestone honours DRY_RUN"
reset_gh_stub '[]'
DRY_RUN=1 ensure_milestone "kubernetes-sigs/kueue" "v0.21" >/dev/null
assert_eq "skipped (DRY_RUN)" "${MILESTONE_RESULT}" "result"
if grep -q "POST" "${GH_FAKE_LOG}"; then
  fail "a POST was issued under DRY_RUN"
fi

start_case "ensure_milestone survives a listing large enough to fill the pipe buffer"
reset_gh_stub "$(python3 -c '
import json
print(json.dumps([{"title": "v0.21", "state": "open"}] * 200000))
')"
rc=0
ensure_milestone "kubernetes-sigs/kueue" "v0.21" >/dev/null || rc=$?
assert_eq "0" "${rc}" "exit code on a large listing"
assert_eq "already present" "${MILESTONE_RESULT}" "result on a large listing"

# --- confirm_push ----------------------------------------------------------

function confirm_rc() {
  local rc=0
  printf '%b' "$1" | confirm_push origin local-branch remote-branch >/dev/null || rc=$?
  echo "${rc}"
}

start_case "confirm_push proceeds on y or Y"
assert_eq "0" "$(confirm_rc 'y\n')" "exit code on y"
assert_eq "0" "$(confirm_rc 'Y\n')" "exit code on Y"

start_case "confirm_push aborts on anything else"
assert_eq "1" "$(confirm_rc 'n\n')" "exit code on n"
assert_eq "1" "$(confirm_rc '\n')" "exit code on a bare Enter"
assert_eq "1" "$(confirm_rc 'yes\n')" "exit code on yes, which the sibling scripts also reject"

# A closed stdin has to be told apart from a no: in a non-interactive run it means nobody
# answered, and treating it as a decline would let a misconfigured job finish green with no PR.
start_case "confirm_push reports no answer when stdin is closed"
rc=0
confirm_push origin local-branch remote-branch </dev/null >/dev/null || rc=$?
assert_eq "2" "${rc}" "exit code on a closed stdin"

start_case "confirm_push accepts an answer without a trailing newline"
assert_eq "0" "$(confirm_rc 'y')" "exit code on an unterminated y"

start_case "confirm_push shows the exact push it is about to run"
assert_contains "$(printf 'n\n' | confirm_push origin local-branch remote-branch 2>&1 || true)" \
  "  git push origin -f local-branch:remote-branch" "announced push command"

# --- main ------------------------------------------------------------------

export KUBERNETES_SIGS_KUEUE_MAIN_REPO_ORG="kubernetes-sigs"
export KUBERNETES_SIGS_KUEUE_MAIN_REPO_NAME="kueue"
release_issue="16900	OPEN	Release v0.21.0"

# run_main runs main in its own bash process, because main exits and relies on errexit, which a
# command substitution would switch off. submit_mapping_pr is replaced by a marker in the gh log so
# the pull request phase needs no test-infra checkout.
function run_main() {
  : >"${GH_FAKE_LOG}"
  MAIN_RC=0
  MAIN_OUTPUT=$(bash -c '
    source "${ROOT_DIR}/hack/releasing/milestone_pull.sh"
    function submit_mapping_pr() {
      echo "submit_mapping_pr" >>"${GH_FAKE_LOG}"
      PR_RESULT="submitted"
    }
    main "$@"
  ' run_main "$@" 2>&1) || MAIN_RC=$?
}

start_case "main --create-milestone needs no test-infra checkout, GITHUB_USER or release issue"
reset_gh_stub '[]'
KUBERNETES_TEST_INFRA_PATH="${test_dir}/missing" run_main --create-milestone v0.21.0
assert_eq "0" "${MAIN_RC}" "exit code"
assert_contains "$(cat "${GH_FAKE_LOG}")" "--method POST repos/kubernetes-sigs/kueue/milestones -f title=v0.22" "create call"
assert_not_contains "$(cat "${GH_FAKE_LOG}")" "branches/" "release branch lookup"
assert_not_contains "$(cat "${GH_FAKE_LOG}")" "issue list" "release issue lookup"
assert_not_contains "$(cat "${GH_FAKE_LOG}")" "submit_mapping_pr" "pull request phase"
assert_contains "${MAIN_OUTPUT}" "milestone v0.22 ......... created" "milestone summary"
assert_contains "${MAIN_OUTPUT}" "mapping pull request .... not requested (--create-milestone)" "pull request summary"

start_case "main --create-milestone honours DRY_RUN and accepts the flag after the version"
reset_gh_stub '[]'
DRY_RUN=1 run_main v0.21.0 --create-milestone
assert_eq "0" "${MAIN_RC}" "exit code"
assert_contains "$(cat "${GH_FAKE_LOG}")" "milestones?state=all" "lookup"
assert_not_contains "$(cat "${GH_FAKE_LOG}")" "POST" "create call"
assert_contains "${MAIN_OUTPUT}" "milestone v0.22 ......... skipped (DRY_RUN)" "milestone summary"

start_case "main reports a failed milestone creation as FAILED"
reset_gh_stub '[]'
GH_FAKE_FAIL="--method POST" run_main --create-milestone v0.21.0
assert_eq "1" "${MAIN_RC}" "exit code"
assert_contains "${MAIN_OUTPUT}" "milestone v0.22 ......... FAILED" "milestone summary"

start_case "main --create-pr makes no milestones call"
reset_gh_stub '[]'
GH_FAKE_ISSUES="${release_issue}" run_main --create-pr v0.21.0
assert_eq "0" "${MAIN_RC}" "exit code"
assert_not_contains "$(cat "${GH_FAKE_LOG}")" "milestones" "milestones calls"
assert_contains "$(cat "${GH_FAKE_LOG}")" "api repos/kubernetes-sigs/kueue/branches/release-0.21" "release branch lookup"
assert_contains "$(cat "${GH_FAKE_LOG}")" "submit_mapping_pr" "pull request phase"
assert_contains "${MAIN_OUTPUT}" "milestone v0.22 ......... not requested (--create-pr)" "milestone summary"

start_case "main without a flag runs only the pull request phase"
reset_gh_stub '[]'
GH_FAKE_ISSUES="${release_issue}" run_main v0.21.0
assert_eq "0" "${MAIN_RC}" "exit code"
assert_not_contains "$(cat "${GH_FAKE_LOG}")" "milestones" "milestones calls"
assert_contains "$(cat "${GH_FAKE_LOG}")" "submit_mapping_pr" "pull request phase"
assert_contains "${MAIN_OUTPUT}" "milestone v0.22 ......... skipped (use /create-milestone, or set CREATE_MILESTONE)" "milestone summary"

start_case "main runs the milestone phase before the pull request phase"
reset_gh_stub '[]'
GH_FAKE_ISSUES="${release_issue}" run_main --create-milestone --create-pr v0.21.0
assert_eq "0" "${MAIN_RC}" "exit code with both flags"
assert_eq "POST submit_mapping_pr" "$(grep -oE 'POST|submit_mapping_pr' "${GH_FAKE_LOG}" | paste -sd ' ' -)" "phase order with both flags"
reset_gh_stub '[]'
CREATE_MILESTONE=1 GH_FAKE_ISSUES="${release_issue}" run_main v0.21.0
assert_eq "0" "${MAIN_RC}" "exit code with CREATE_MILESTONE"
assert_eq "POST submit_mapping_pr" "$(grep -oE 'POST|submit_mapping_pr' "${GH_FAKE_LOG}" | paste -sd ' ' -)" "phase order with CREATE_MILESTONE"

start_case "main still requires the release issue for the pull request phase"
reset_gh_stub '[]'
run_main --create-pr v0.21.0
assert_eq "2" "${MAIN_RC}" "exit code"
assert_contains "${MAIN_OUTPUT}" "No release issue found" "error"
assert_not_contains "$(cat "${GH_FAKE_LOG}")" "submit_mapping_pr" "pull request phase"

start_case "main rejects contradicting environment variables before calling gh"
reset_gh_stub '[]'
CREATE_MILESTONE=1 run_main --create-pr v0.21.0
assert_eq "2" "${MAIN_RC}" "exit code with CREATE_MILESTONE"
assert_eq "" "$(cat "${GH_FAKE_LOG}")" "gh calls with CREATE_MILESTONE"
SKIP_PR=1 run_main --create-pr v0.21.0
assert_eq "2" "${MAIN_RC}" "exit code with SKIP_PR"
assert_eq "" "$(cat "${GH_FAKE_LOG}")" "gh calls with SKIP_PR"

start_case "main rejects invalid usage before calling gh"
run_main --create-milestones v0.21.0
assert_eq "2" "${MAIN_RC}" "exit code for an unknown option"
assert_contains "${MAIN_OUTPUT}" "Unknown option" "unknown option message"
assert_eq "" "$(cat "${GH_FAKE_LOG}")" "gh calls for an unknown option"
run_main --create-pr
assert_eq "2" "${MAIN_RC}" "exit code without a version"
assert_eq "" "$(cat "${GH_FAKE_LOG}")" "gh calls without a version"
run_main v0.21.0 v0.22.0
assert_eq "2" "${MAIN_RC}" "exit code for two versions"
assert_eq "" "$(cat "${GH_FAKE_LOG}")" "gh calls for two versions"
run_main --create-milestone v0.21.1
assert_eq "2" "${MAIN_RC}" "exit code for a patch release"
assert_eq "" "$(cat "${GH_FAKE_LOG}")" "gh calls for a patch release"

start_case "main --help prints the usage and succeeds"
run_main --help
assert_eq "0" "${MAIN_RC}" "exit code"
assert_contains "${MAIN_OUTPUT}" "--create-milestone" "usage mentions the flag"
assert_eq "" "$(cat "${GH_FAKE_LOG}")" "gh calls"

# --- result ----------------------------------------------------------------

if [[ "${failures}" -ne 0 ]]; then
  echo "${failures} test case(s) failed." >&2
  exit 1
fi

echo "All milestone_pull.sh test cases passed."
