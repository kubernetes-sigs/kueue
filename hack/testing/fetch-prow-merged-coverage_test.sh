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

# Self-test for fetch-prow-merged-coverage.sh, without network access:
# codecov_has_upload with a stub curl that replays canned Codecov API
# responses, then main with gh and curl replaced by functions that serve
# GitHub, GCS and Codecov responses from a fixture tree.
# Usage: fetch-prow-merged-coverage_test.sh

set -o errexit
set -o nounset
set -o pipefail

tmp=$(mktemp -d)
trap 'rm -rf "${tmp}"' EXIT
# shellcheck source=hack/testing/fetch-prow-merged-coverage.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/fetch-prow-merged-coverage.sh"

failures=0
fail() { echo "FAIL: $*" >&2; failures=$((failures + 1)); }

# codecov_has_upload (first upload wins).
SHA=0123456789abcdef0123456789abcdef01234567
CODECOV_POLL_TRIES=3
CODECOV_POLL_SECS=0

# The stub replays ${tmp}/responses, one "HTTP_CODE BODY" line per call, and
# repeats the last line once the others are used up. With -f it exits 22 on
# HTTP errors, still printing the status code like real curl.
curl() {
  local n line
  n=$(( $(cat "${tmp}/calls") + 1 ))
  echo "${n}" > "${tmp}/calls"
  line=$(sed -n "${n}p" "${tmp}/responses")
  [[ -n "${line}" ]] || line=$(tail -n 1 "${tmp}/responses")
  [[ "${line}" != "fail" ]] || return 7
  printf '%s\n%s' "${line#* }" "${line%% *}"
  [[ " $* " != *" -sSf "* ]] || ((${line%% *} < 400)) || return 22
}

# uploads "KIND..." [FLAGS] prints an uploads page in the shape of
# /api/v2/github/OWNER/repos/REPO/commits/SHA/uploads/. KIND is a coverage
# upload state, or test_results for a processed test-results upload.
uploads() {
  local kind flags=${2:-'["unit"]'} out="" s totals='{"files":1,"lines":10,"hits":8}'
  for kind in $1; do
    case "${kind}" in
      merged) s='"state":"merged","state_id":6,"state_name":"MERGED","totals":'"${totals}" ;;
      processed) s='"state":"processed","state_id":2,"state_name":"PROCESSED","totals":'"${totals}" ;;
      started | uploaded) s='"state":"'"${kind}"'","state_id":1,"state_name":"UPLOADED","totals":null' ;;
      error) s='"state":"error","state_id":3,"state_name":"ERROR","totals":null' ;;
      fully_overwritten) s='"state":"fully_overwritten","state_id":4,"state_name":"FULLY_OVERWRITTEN","totals":'"${totals}" ;;
      partially_overwritten) s='"state":"partially_overwritten","state_id":5,"state_name":"PARTIALLY_OVERWRITTEN","totals":'"${totals}" ;;
      test_results) s='"state":"processed","state_id":1,"state_name":"UPLOADED","totals":null' ;;
      *) echo "unknown upload kind ${kind}" >&2; exit 1 ;;
    esac
    out="${out:+${out},}{${s},\"flags\":${flags}}"
  done
  echo "{\"count\":$(wc -w <<<"$1"),\"results\":[${out}]}"
}

# expect NAME WANT(exists|missing) CALLS RESPONSE...
expect() {
  local name=$1 want=$2 calls=$3 got; shift 3
  printf '%s\n' "$@" > "${tmp}/responses"
  echo 0 > "${tmp}/calls"
  if codecov_has_upload "${SHA}" 2> "${tmp}/err"; then got=exists; else got=missing; fi
  if [[ "${got}" != "${want}" ]]; then
    fail "${name}: got ${got}, want ${want}: $(cat "${tmp}/err")"
  elif [[ "$(cat "${tmp}/calls")" != "${calls}" ]]; then
    fail "${name}: ${got} after $(cat "${tmp}/calls") calls, want ${calls}"
  else
    echo "ok: ${name} (${got} after ${calls} calls: $(tail -n 1 "${tmp}/err"))"
  fi
}

expect "merged" exists 1 "200 $(uploads merged)"
expect "processed" exists 1 "200 $(uploads processed)"
expect "error only" missing 1 "200 $(uploads error)"
expect "error and merged" exists 1 "200 $(uploads 'error merged')"
expect "other flag" missing 1 "200 $(uploads merged '["e2e"]')"
expect "no flags" missing 1 "200 $(uploads merged null)"
expect "no uploads" missing 1 '200 {"count":0,"results":[]}'
expect "started then merged" exists 2 "200 $(uploads started)" "200 $(uploads merged)"
expect "uploaded then error" missing 2 "200 $(uploads uploaded)" "200 $(uploads error)"
expect "started stays" missing 3 "200 $(uploads started)"
expect "test results only" missing 1 "200 $(uploads test_results)"
expect "test results and started" missing 3 "200 $(uploads 'test_results started')"
expect "fully overwritten" missing 1 "200 $(uploads fully_overwritten)"
expect "partially overwritten" missing 1 "200 $(uploads partially_overwritten)"
expect "merged without totals" missing 1 '200 {"count":1,"results":[{"state":"merged","state_id":6,"state_name":"MERGED","totals":null,"flags":["unit"]}]}'
expect "state_id only" exists 1 '200 {"count":1,"results":[{"state":"merged","state_id":6,"totals":{"lines":1},"flags":["unit"]}]}'
expect "carryforward without flags" missing 1 '200 {"count":1,"results":[{"state":"processed","state_id":2,"state_name":"PROCESSED","totals":null,"flags":[],"upload_type":"carriedforward"}]}'
expect "404" missing 1 '404 {"detail":"Not found."}'
expect "503" missing 1 '503 <html>unavailable</html>'
expect "invalid JSON" missing 1 '200 {"results":['
expect "no results" missing 1 '200 {"detail":"x"}'
expect "curl error" missing 1 fail

CODECOV_FORCE=true
expect "force" missing 0 "200 $(uploads merged)"
CODECOV_FORCE=false

# main, end to end. The fixture tree ${tmp}/fx holds gh/<API path> for the gh
# CLI and web/<host>/<path> for curl.
HEAD_SHA=1111111111111111111111111111111111111111
BASE_SHA=2222222222222222222222222222222222222222
MAIN_SHA=3333333333333333333333333333333333333333
PR=18130
PULL_PATH="pr-logs/pull/kubernetes-sigs_kueue/${PR}/${PULL_JOB}/101"
POST_PATH="logs/${POST_JOB}/202"
PROFILE=$'mode: atomic\nsigs.k8s.io/kueue/pkg/a.go:1.1,2.2 1 1'
R=gh/repos/kubernetes-sigs/kueue
GCS_FX=web/storage.googleapis.com/kubernetes-ci-logs
VIEW=https://prow.k8s.io/view/gs/kubernetes-ci-logs
UPLOADS=web/api.codecov.io/api/v2/github/kubernetes-sigs/repos/kueue/commits

# put FILE CONTENT writes a fixture file under ${tmp}/fx.
put() { mkdir -p "$(dirname "${tmp}/fx/$1")"; printf '%s\n' "$2" > "${tmp}/fx/$1"; }

# gh api [--paginate] PATH: serves ${tmp}/fx/gh/PATH (query string dropped).
gh() {
  local path
  path=${*: -1}
  path=${path%%\?*}
  if [[ -f "${tmp}/fx/gh/${path}" ]]; then cat "${tmp}/fx/gh/${path}"; else echo "gh: Not Found (HTTP 404)" >&2; return 1; fi
}

# curl ... URL: serves ${tmp}/fx/web/HOST/PATH (query string and trailing
# slash dropped) like curl -w '\n%{http_code}', or writes it to the -o file
# (failing like -f when it is missing).
curl() {
  local url="" out="" w=false fail=false a f
  while (($#)); do
    a=$1; shift
    case "${a}" in
      -o) out=$1; shift ;;
      -w) w=true; shift ;;
      http*) url=${a} ;;
      --*) ;;
      -*f*) fail=true ;;
    esac
  done
  f="${tmp}/fx/web/${url#https://}"
  f=${f%%\?*}
  f=${f%/}
  if [[ -n "${out}" ]]; then
    [[ -f "${f}" ]] || return 22
    cp "${f}" "${out}"
  elif [[ -f "${f}" ]]; then
    cat "${f}"; ${w} && printf '\n200'
  else
    ${w} && printf '\n404'
    ! ${fail} || return 22
  fi
  return 0
}

status() { # CONTEXT STATE URL
  printf '{"context":"%s","state":"%s","target_url":"%s"}' "$1" "$2" "$3"
}

# tree_fx ENTRY... prints a Git trees API response; ENTRY is "PATH MODE SHA".
tree_fx() {
  local e p m s out=""
  for e in "$@"; do
    read -r p m s <<<"${e}"
    out+="${out:+,}{\"path\":\"${p}\",\"mode\":\"${m}\",\"type\":\"$([[ "${m}" == 040000 ]] && echo tree || echo blob)\",\"sha\":\"${s}\"}"
  done
  echo "{\"tree\":[${out}],\"truncated\":false}"
}

YML_MAIN=4444444444444444444444444444444444444444
GO_MOD="go.mod 100644 0000000000000000000000000000000000000f00"

# base_fixtures: codecov.yml on the default branch; the PR head (from a fork)
# and its merge base have no Codecov YAML; the main commit has only
# codecov.yml; both merge builds succeeded; Codecov has no upload yet.
base_fixtures() {
  rm -rf "${tmp}/fx"
  put "${R}/contents/codecov.yml" '{"type":"file"}'
  put "${R}/git/trees/${HEAD_SHA}" "$(tree_fx "${GO_MOD}")"
  put "${R}/git/trees/${BASE_SHA}" "$(tree_fx "${GO_MOD}")"
  put "${R}/compare/${BASE_SHA}...${HEAD_SHA}" "{\"merge_base_commit\":{\"sha\":\"${BASE_SHA}\"}}"
  put "${R}/git/trees/${MAIN_SHA}" "$(tree_fx "${GO_MOD}" "codecov.yml 100644 ${YML_MAIN}")"
  put "${R}/pulls/${PR}" '{"head":{"ref":"feature","label":"someone:feature","repo":{"full_name":"someone/kueue"}}}'
  put "${R}/commits/${HEAD_SHA}/statuses" \
    "[$(status "${PULL_JOB}" success "${VIEW}/${PULL_PATH}"),$(status pull-kueue-verify-main success "${VIEW}/x/1")]"
  put "${R}/commits/${MAIN_SHA}/statuses" "[$(status "${POST_JOB}" success "${VIEW}/${POST_PATH}")]"
  put "${GCS_FX}/${PULL_PATH}/finished.json" '{"result":"SUCCESS"}'
  put "${GCS_FX}/${PULL_PATH}/prowjob.json" \
    "{\"spec\":{\"type\":\"presubmit\",\"job\":\"${PULL_JOB}\",\"refs\":{\"base_ref\":\"main\",\"base_sha\":\"${BASE_SHA}\",\"pulls\":[{\"number\":${PR},\"sha\":\"${HEAD_SHA}\"}]}}}"
  put "${GCS_FX}/${PULL_PATH}/artifacts/coverage-sources.json" "{\"commit\":\"${HEAD_SHA}\",\"shards\":[\"a\",\"b\"]}"
  put "${GCS_FX}/${PULL_PATH}/artifacts/filtered.cov" "${PROFILE}"
  put "${GCS_FX}/${POST_PATH}/finished.json" '{"result":"SUCCESS"}'
  put "${GCS_FX}/${POST_PATH}/prowjob.json" \
    "{\"spec\":{\"type\":\"postsubmit\",\"job\":\"${POST_JOB}\",\"refs\":{\"base_ref\":\"main\",\"base_sha\":\"${MAIN_SHA}\"}}}"
  put "${GCS_FX}/${POST_PATH}/artifacts/coverage-sources.json" "{\"commit\":\"${MAIN_SHA}\",\"shards\":[\"a\",\"b\"]}"
  put "${GCS_FX}/${POST_PATH}/artifacts/filtered.cov" "${PROFILE}"
}

# run NAME WANT_RC WANT_OUTPUT_REGEX SHA: runs main in a subshell; the regex
# is matched against stdout and stderr. The workflow reads only stdout, so a
# run that exits 0 must also print skip=false there when the regex is
# ^skip=false$, and skip=true otherwise.
run() {
  local name=$1 want_rc=$2 want=$3 sha=$4 rc=0 want_skip=true
  rm -rf "${tmp}/out"
  ( main "${sha}" "${tmp}/out" ) >"${tmp}/stdout" 2>"${tmp}/err" || rc=$?
  [[ "${want}" != '^skip=false$' ]] || want_skip=false
  if [[ "${rc}" != "${want_rc}" ]] || ! grep -Eq "${want}" "${tmp}/stdout" "${tmp}/err" \
    || { ((rc == 0)) && ! grep -qxF "skip=${want_skip}" "${tmp}/stdout"; }; then
    fail "${name}: rc=${rc} (want ${want_rc}, skip=${want_skip} on stdout), output:"$'\n'"$(cat "${tmp}/stdout" "${tmp}/err")"
    return
  fi
  echo "ok: ${name}"
}

# has NAME LINE: the last run printed LINE on stdout.
has() { grep -qxF "$2" "${tmp}/stdout" || fail "$1: no line '$2' in:"$'\n'"$(cat "${tmp}/stdout")"; }

base_fixtures
run "PR head" 0 "^skip=false$" "${HEAD_SHA}"
has "PR head" "pr=${PR}"
has "PR head" "branch=someone:feature"
has "PR head" "build_id=101"
[[ "$(cat "${tmp}/out/unit-coverage.out")" == "${PROFILE}" ]] || fail "PR head: profile not downloaded"
run "main commit" 0 "^skip=false$" "${MAIN_SHA}"
has "main commit" "pr="
has "main commit" "branch=main"
has "main commit" "build_url=${VIEW}/${POST_PATH}"

base_fixtures
put "${R}/pulls/${PR}" '{"head":{"ref":"x,--sha=1","label":"someone:x,--sha=1","repo":{"full_name":"someone/kueue"}}}'
run "fork branch label that is not plain" 0 "^skip=false$" "${HEAD_SHA}"
has "fork branch label that is not plain" "branch=pr-${PR}"

base_fixtures
put "${R}/pulls/${PR}" '{"head":{"ref":"feature","label":"kubernetes-sigs:feature","repo":{"full_name":"kubernetes-sigs/kueue"}}}'
run "branch in the repository" 0 "^skip=false$" "${HEAD_SHA}"
has "branch in the repository" "branch=feature"

base_fixtures
put "${GCS_FX}/${POST_PATH}/artifacts/coverage-sources.json" \
  "{\"commit\":\"${MAIN_SHA}\",\"skipped\":\"batch member before the last\"}"
run "main commit skipped by Prow" 0 "Prow skipped .*batch member" "${MAIN_SHA}"

base_fixtures
put "${GCS_FX}/${POST_PATH}/artifacts/coverage-sources.json" "{\"commit\":\"${HEAD_SHA}\",\"shards\":[\"a\"]}"
run "coverage of another commit" 1 "describes another commit" "${MAIN_SHA}"

base_fixtures
put "${GCS_FX}/${POST_PATH}/artifacts/coverage-sources.json" "{\"commit\":\"${HEAD_SHA}\",\"skipped\":\"x\"}"
run "skip of another commit" 1 "describes another commit" "${MAIN_SHA}"

base_fixtures
put "${R}/commits/${HEAD_SHA}/statuses" \
  "[$(status "${PULL_JOB}" pending ""),$(status "${PULL_JOB}" success "${VIEW}/${PULL_PATH}")]"
run "newest status pending" 0 "is pending" "${HEAD_SHA}"

base_fixtures
put "${R}/commits/${HEAD_SHA}/statuses" "[$(status "${PULL_JOB}" success "https://prow.k8s.io/view/gs/other-bucket/${PULL_PATH}")]"
run "foreign target URL" 1 "unexpected target URL" "${HEAD_SHA}"

base_fixtures
put "${R}/commits/${HEAD_SHA}/statuses" "[$(status "${PULL_JOB}" success "${VIEW}/pr-logs/pull/kubernetes-sigs_kueue/${PR}/pull-kueue-verify-main/101")]"
run "target URL of another job" 1 "unexpected target URL" "${HEAD_SHA}"

base_fixtures
put "${R}/commits/${HEAD_SHA}/statuses" "[$(status "${POST_JOB}" success "${VIEW}/${POST_PATH}")]"
run "postsubmit status on another commit" 1 "not a postsubmit of main at ${HEAD_SHA}" "${HEAD_SHA}"

base_fixtures
put "${GCS_FX}/${PULL_PATH}/prowjob.json" \
  "{\"spec\":{\"type\":\"presubmit\",\"job\":\"${PULL_JOB}\",\"refs\":{\"base_sha\":\"${BASE_SHA}\",\"pulls\":[{\"number\":${PR},\"sha\":\"${BASE_SHA}\"}]}}}"
run "presubmit of another head" 1 "is not a presubmit of only PR" "${HEAD_SHA}"

base_fixtures
put "${GCS_FX}/${PULL_PATH}/finished.json" '{"result":"FAILURE"}'
run "build failed" 1 "did not succeed" "${HEAD_SHA}"

base_fixtures
rm "${tmp}/fx/${GCS_FX}/${PULL_PATH}/artifacts/filtered.cov"
run "no profile" 1 "cannot download a non-empty filtered.cov" "${HEAD_SHA}"

base_fixtures
put "${GCS_FX}/${PULL_PATH}/artifacts/filtered.cov" "<html>not a profile</html>"
run "not a coverprofile" 1 "not a Go coverprofile" "${HEAD_SHA}"

base_fixtures
put "${R}/commits/5555555555555555555555555555555555555555/statuses" "[$(status pull-kueue-verify-main success "${VIEW}/x/1")]"
run "no merge-job status" 0 "No ${PULL_JOB} or ${POST_JOB} status" 5555555555555555555555555555555555555555

base_fixtures
put "${R}/commits/5555555555555555555555555555555555555555/statuses" "[]"
run "no status at all" 0 "No ${PULL_JOB} or ${POST_JOB} status" 5555555555555555555555555555555555555555

base_fixtures
rm "${tmp}/fx/${R}/commits/${HEAD_SHA}/statuses"
run "statuses API fails" 1 "cannot read the statuses of ${HEAD_SHA}" "${HEAD_SHA}"

base_fixtures
put "${R}/commits/${HEAD_SHA}/statuses" "<html>bad gateway</html>"
run "statuses not JSON" 1 "cannot read the statuses of ${HEAD_SHA}" "${HEAD_SHA}"

base_fixtures
rm "${tmp}/fx/${R}/contents/codecov.yml"
run "default branch without codecov.yml" 0 "rollout not done" "${MAIN_SHA}"

base_fixtures
put "${UPLOADS}/${MAIN_SHA}/uploads" \
  '{"count":1,"results":[{"state":"processed","state_id":2,"state_name":"PROCESSED","totals":{"lines":1},"flags":["unit"]}]}'
run "Codecov already has an upload" 0 "keeping the first one" "${MAIN_SHA}"
has "Codecov already has an upload" "skip=true"
[[ ! -e "${tmp}/out/unit-coverage.out" ]] || fail "Codecov already has an upload: profile downloaded"

# The Codecov YAML guard.
base_fixtures
put "${R}/git/trees/${HEAD_SHA}" "$(tree_fx "${GO_MOD}" "codecov.yml 100644 ${YML_MAIN}")"
put "${R}/git/trees/${BASE_SHA}" "$(tree_fx "${GO_MOD}" "codecov.yml 100644 ${YML_MAIN}")"
run "PR keeps the codecov.yml of its merge base" 0 "^skip=false$" "${HEAD_SHA}"

base_fixtures
put "${R}/git/trees/${HEAD_SHA}" "$(tree_fx "${GO_MOD}" "codecov.yml 100644 6666666666666666666666666666666666666666")"
put "${R}/git/trees/${BASE_SHA}" "$(tree_fx "${GO_MOD}" "codecov.yml 100644 ${YML_MAIN}")"
run "PR edits codecov.yml" 0 "PR adds or edits Codecov config" "${HEAD_SHA}"

base_fixtures
put "${R}/git/trees/${HEAD_SHA}" "$(tree_fx "${GO_MOD}" ".github 040000 7777777777777777777777777777777777777777")"
put "${R}/git/trees/7777777777777777777777777777777777777777" "$(tree_fx "codecov.yaml 100644 ${YML_MAIN}")"
run "PR adds .github/codecov.yaml" 0 "PR adds or edits Codecov config" "${HEAD_SHA}"

base_fixtures
put "${R}/git/trees/${MAIN_SHA}" "$(tree_fx "${GO_MOD}" "codecov.yml 100644 ${YML_MAIN}" ".codecov.yml 100644 ${YML_MAIN}")"
run "main commit with a second Codecov YAML" 0 "has another Codecov YAML path" "${MAIN_SHA}"

base_fixtures
put "${R}/git/trees/${MAIN_SHA}" "$(tree_fx "${GO_MOD}" "codecov.yml 100755 ${YML_MAIN}")"
run "main commit with an executable codecov.yml" 0 "^skip=false$" "${MAIN_SHA}"

base_fixtures
put "${R}/git/trees/${MAIN_SHA}" "$(tree_fx "${GO_MOD}" "codecov.yml 120000 ${YML_MAIN}")"
run "main commit with a codecov.yml symlink" 0 "has another Codecov YAML path" "${MAIN_SHA}"

base_fixtures
put "${R}/git/trees/${MAIN_SHA}" '{"tree":[],"truncated":true}'
run "truncated tree" 1 "cannot parse the tree" "${MAIN_SHA}"

if ((failures > 0)); then
  echo "${failures} failure(s)" >&2
  exit 1
fi
echo "All tests passed."
