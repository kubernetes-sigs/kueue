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

# Finds the merged unit coverage that Prow published for one commit and
# downloads it from the public GCS bucket. Prow does the merging:
# - pull-kueue-test-unit-coverage-main (presubmit) posts a status on a pull
#   request head once it merged the shards that ran on that head;
# - post-kueue-test-unit-coverage-main (postsubmit) posts a status on each
#   main commit after it merged the shard builds that tested that commit's
#   exact tree (or skipped it; coverage-sources.json then says why).
# Both write artifacts/filtered.cov and artifacts/coverage-sources.json
# (hack/testing/merge-unit-coverage.sh). This script never runs code from the
# commit under test; the profile is treated as data.
#
# Usage: fetch-prow-merged-coverage.sh SHA OUTDIR
#   Uses the newest status of either context on SHA. Prints skip=true when it
#   is not success or when the build skipped the commit. Fails when the
#   status or the Prow metadata does not describe a build of SHA.
#
# Prints key=value lines (skip, sha, pr, branch, build_id, build_url) for
# $GITHUB_OUTPUT, each checked by check_outputs, and writes
# OUTDIR/unit-coverage.out.
#
# Environment:
#   GH_TOKEN            used by the gh CLI for GitHub API calls
#   GITHUB_REPOSITORY   defaults to kubernetes-sigs/kueue

set -o errexit
set -o nounset
set -o pipefail

REPO="${GITHUB_REPOSITORY:-kubernetes-sigs/kueue}"
PULL_JOB="pull-kueue-test-unit-coverage-main"
POST_JOB="post-kueue-test-unit-coverage-main"
BUCKET="kubernetes-ci-logs"
GCS="https://storage.googleapis.com/${BUCKET}"
PROW_VIEW="https://prow.k8s.io/view/gs/${BUCKET}"
GCS_ORG_REPO="${REPO/\//_}"
MAX_PROFILE_BYTES=$((512 * 1024 * 1024))

# --- GCS reads and output checks.

die() { echo "Error: $*" >&2; exit 1; }
log() { echo "$*" >&2; }

# gcs_get OBJECT prints OBJECT. Returns 4 when GCS has no such object (HTTP
# 404), and 1 on any other failure, which callers treat as fatal.
gcs_get() {
  local out code
  # -f keeps the body of a failed attempt out of a retried response; curl
  # still writes the status code, and exit code 22 means an HTTP error.
  out=$(curl -sSfL --retry 3 --connect-timeout 20 --max-time 120 -w '\n%{http_code}' "${GCS}/$1" 2>/dev/null) \
    || [[ $? == 22 ]] || { log "Cannot fetch $1 from GCS."; return 1; }
  code=${out##*$'\n'}
  case "${code}" in
    200) printf '%s\n' "${out%$'\n'*}" ;;
    404) return 4 ;;
    *) log "GCS returned HTTP ${code} for $1."; return 1 ;;
  esac
}

# gcs_download OBJECT FILE
gcs_download() {
  curl -sSfL --retry 3 --connect-timeout 20 --max-time 120 --max-filesize "${MAX_PROFILE_BYTES}" \
    -o "$2" "${GCS}/$1"
}

require_sha() {
  [[ "$1" =~ ^[0-9a-f]{40}$ ]] || die "'$1' is not a 40-character commit SHA."
}

skip() { log "$*"; echo "skip=true"; exit 0; }

# The codecov-action wrapper (dist/codecov.sh) sets IFS=, and expands the
# branch, build, build URL, and directory inputs unquoted, through
# `eval echo`, so commas, spaces, glob characters, and leading dashes in them
# are not passed through as one value. A fork branch name such as
# "x,--sha=..." is a valid git ref; only plain names are used as the branch.
SAFE_LABEL_RE='^[A-Za-z0-9._/:][A-Za-z0-9._/:-]{0,199}$'

# safe_branch LABEL PR prints LABEL if it matches SAFE_LABEL_RE, else pr-PR.
safe_branch() {
  if [[ "$1" =~ ${SAFE_LABEL_RE} ]]; then
    echo "$1"
  else
    log "Branch label '$1' is not plain; using pr-$2."
    echo "pr-$2"
  fi
}

# check_outputs SHA PR BRANCH BUILD_ID BUILD_URL dies unless every value that
# reaches the codecov-action inputs has the expected plain form.
check_outputs() {
  require_sha "$1"
  [[ -z "$2" || "$2" =~ ^[0-9]+$ ]] || die "unexpected pull request number '$2'."
  [[ "$3" =~ ${SAFE_LABEL_RE} ]] || die "unexpected branch label '$3'."
  [[ "$4" =~ ^[0-9]+(-[0-9]+)*$ ]] || die "unexpected build ID '$4'."
  [[ "$5" =~ ^https://prow\.k8s\.io/[A-Za-z0-9._/?\&=-]+$ ]] || die "unexpected build URL '$5'."
}

# --- The Prow merge build of one commit.

# newest_status SHA prints "<context> <state> <target_url>" for the newest
# status of PULL_JOB or POST_JOB on SHA (the statuses API lists newest
# first), or nothing.
newest_status() {
  gh api --paginate "repos/${REPO}/commits/$1/statuses?per_page=100" \
    | jq -rs --arg a "${PULL_JOB}" --arg b "${POST_JOB}" '
        add // [] | map(select(.context == $a or .context == $b))
        | first // empty | "\(.context) \(.state) \(.target_url // "")"'
}

# check_build JOB PATH SHA [PR] validates the build a status points at and
# prints its base SHA: a successful build of JOB that tested SHA, either the
# head of PR (presubmit) or the main commit itself (postsubmit).
check_build() {
  local job=$1 path=$2 sha=$3 pr=${4:-} finished prowjob
  finished=$(gcs_get "${path}/finished.json") || die "cannot read ${path}/finished.json."
  [[ "$(jq -r '.result // ""' <<<"${finished}")" == "SUCCESS" ]] || die "${path} did not succeed."
  prowjob=$(gcs_get "${path}/prowjob.json") || die "cannot read ${path}/prowjob.json."
  if [[ -n "${pr}" ]]; then
    jq -e --arg j "${job}" --argjson n "${pr}" --arg s "${sha}" \
      '.spec.type == "presubmit" and .spec.job == $j and .spec.refs.pulls == [.spec.refs.pulls[0]]
       and .spec.refs.pulls[0].number == $n and .spec.refs.pulls[0].sha == $s' <<<"${prowjob}" >/dev/null \
      || die "${path} is not a presubmit of only PR ${pr} at ${sha}."
  else
    jq -e --arg j "${job}" --arg s "${sha}" \
      '.spec.type == "postsubmit" and .spec.job == $j and .spec.refs.base_ref == "main"
       and .spec.refs.base_sha == $s and ((.spec.refs.pulls // []) | length) == 0' <<<"${prowjob}" >/dev/null \
      || die "${path} is not a postsubmit of main at ${sha}."
  fi
  jq -r '.spec.refs.base_sha' <<<"${prowjob}"
}

main() {
  local sha=${1:-} outdir=${2:-}
  [[ "$#" -eq 2 ]] || die "usage: $0 SHA OUTDIR"
  require_sha "${sha}"
  mkdir -p "${outdir}"

  local status context state url path id pr="" base branch=main build_url
  status=$(newest_status "${sha}") || die "cannot read the statuses of ${sha}."
  read -r context state url <<<"${status}"
  [[ -n "${context:-}" ]] || skip "No ${PULL_JOB} or ${POST_JOB} status on ${sha}."
  [[ "${state}" == "success" ]] || skip "${context} is ${state} on ${sha}."
  local view_re="^${PROW_VIEW//./\\.}/"
  if [[ "${context}" == "${PULL_JOB}" && "${url}" =~ ${view_re}(pr-logs/pull/${GCS_ORG_REPO}/([0-9]+)/${PULL_JOB}/([0-9]+))/?$ ]]; then
    path=${BASH_REMATCH[1]} pr=${BASH_REMATCH[2]} id=${BASH_REMATCH[3]}
  elif [[ "${context}" == "${POST_JOB}" && "${url}" =~ ${view_re}(logs/${POST_JOB}/([0-9]+))/?$ ]]; then
    path=${BASH_REMATCH[1]} id=${BASH_REMATCH[2]}
  else
    die "unexpected target URL for ${context}: ${url}"
  fi
  base=$(check_build "${context}" "${path}" "${sha}" "${pr}") || exit 1
  require_sha "${base}"
  # The postsubmit succeeds without a profile when no shard build tested the
  # commit's exact tree; it says why.
  local sources reason
  sources=$(gcs_get "${path}/artifacts/coverage-sources.json") \
    || die "cannot read ${path}/artifacts/coverage-sources.json."
  [[ "$(jq -r '.commit // ""' <<<"${sources}")" == "${sha}" ]] \
    || die "${path}/artifacts/coverage-sources.json describes another commit."
  reason=$(jq -r '.skipped // ""' <<<"${sources}") || die "cannot parse coverage-sources.json."
  [[ -z "${reason}" ]] || skip "Prow skipped ${sha}: ${reason}"
  build_url="${PROW_VIEW}/${path}"

  if [[ -n "${pr}" ]]; then
    # Codecov names fork branches owner:branch.
    branch=$(gh api "repos/${REPO}/pulls/${pr}" | jq -r --arg r "${REPO}" \
      'if .head.repo.full_name == $r then .head.ref else .head.label end') \
      || die "cannot read pull request ${pr}."
    branch=$(safe_branch "${branch}" "${pr}")
  fi
  log "${context} build ${id} merged the unit coverage of ${sha}."

  check_outputs "${sha}" "${pr}" "${branch}" "${id}" "${build_url}"
  local file="${outdir}/unit-coverage.out"
  log "Downloading ${GCS}/${path}/artifacts/filtered.cov"
  gcs_download "${path}/artifacts/filtered.cov" "${file}" && [[ -s "${file}" ]] \
    || die "cannot download a non-empty filtered.cov from ${path}."
  [[ "$(head -c 6 "${file}")" == "mode: " ]] || die "${path}/artifacts/filtered.cov is not a Go coverprofile."
  echo "skip=false"
  echo "sha=${sha}"
  echo "pr=${pr}"
  echo "branch=${branch}"
  echo "build_id=${id}"
  echo "build_url=${build_url}"
}

# A test can source this file.
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
