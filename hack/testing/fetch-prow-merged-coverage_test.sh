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

# Self-test for fetch-prow-merged-coverage.sh, without network access: main
# with gh and curl replaced by functions that serve GitHub and GCS responses
# from a fixture tree.
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

# The fixture tree ${tmp}/fx holds gh/<API path> for the gh CLI and
# web/<host>/<path> for curl.
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

# base_fixtures: the PR head is from a fork; both merge builds succeeded.
base_fixtures() {
  rm -rf "${tmp}/fx"
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
  "{\"commit\":\"${MAIN_SHA}\",\"skipped\":\"no unit shard build tested the tree of ${MAIN_SHA}\"}"
run "main commit skipped by Prow" 0 "Prow skipped .*no unit shard build" "${MAIN_SHA}"

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

if ((failures > 0)); then
  echo "${failures} failure(s)" >&2
  exit 1
fi
echo "All tests passed."
