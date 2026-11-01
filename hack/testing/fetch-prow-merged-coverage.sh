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
#   is not success, when the build skipped the commit, while the default
#   branch has no codecov.yml, when the Codecov YAML that would apply to SHA
#   may differ from main's (main_config_ok, pr_config_unchanged), and when
#   Codecov already has a finished CODECOV_FLAG upload for SHA
#   (codecov_has_upload) unless CODECOV_FORCE is true. Fails when the status
#   or the Prow metadata does not describe a build of SHA.
#
# Prints key=value lines (skip, sha, pr, branch, build_id, build_url) for
# $GITHUB_OUTPUT, each checked by check_outputs, and writes
# OUTDIR/unit-coverage.out.
#
# Environment:
#   GH_TOKEN            used by the gh CLI for GitHub API calls
#   GITHUB_REPOSITORY   defaults to kubernetes-sigs/kueue
#   CODECOV_FLAG        upload flag of the workflow, defaults to unit
#   CODECOV_FORCE       the string true to upload even when Codecov has an upload
#   CODECOV_POLL_TRIES  Codecov queries while an upload is unfinished, defaults to 10
#   CODECOV_POLL_SECS   seconds between those queries, defaults to 30

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
CODECOV_FLAG="${CODECOV_FLAG:-unit}"
CODECOV_FORCE="${CODECOV_FORCE:-false}"
CODECOV_POLL_TRIES="${CODECOV_POLL_TRIES:-10}"
CODECOV_POLL_SECS="${CODECOV_POLL_SECS:-30}"
CODECOV_API="https://api.codecov.io/api/v2/github/${REPO%%/*}/repos/${REPO#*/}"

# --- GCS reads, the Codecov YAML guard, first upload wins.

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

gcs_exists() {
  curl -sfI --retry 3 --connect-timeout 20 --max-time 60 -o /dev/null "${GCS}/$1"
}

# gcs_download OBJECT FILE
gcs_download() {
  curl -sSfL --retry 3 --connect-timeout 20 --max-time 120 --max-filesize "${MAX_PROFILE_BYTES}" \
    -o "$2" "${GCS}/$1"
}

require_sha() {
  [[ "$1" =~ ^[0-9a-f]{40}$ ]] || die "'$1' is not a 40-character commit SHA."
}

# Codecov reads the YAML of a commit from the first of these names in the
# repository root, else from the dev/ or .github/ folder:
# determine_commit_yaml_location in codecov/umbrella
# libs/shared/shared/yaml/fetcher.py (also listed in
# https://docs.codecov.com/docs/codecov-yaml). get_final_yaml in
# libs/shared/shared/yaml/user_yaml.py applies that commit YAML when the
# commit has one, else the repository YAML saved from main
# (strict_yaml_branch).
CODECOV_YAML_NAMES='["codecov.yml", ".codecov.yml", "codecov.yaml", ".codecov.yaml"]'
CODECOV_YAML_DIRS=(dev .github)

# main_has_codecov_yml returns 1 while the default branch has no codecov.yml
# file (the rollout is not done).
main_has_codecov_yml() {
  local out
  if ! out=$(gh api "repos/${REPO}/contents/codecov.yml" 2>&1); then
    [[ "${out}" == *"HTTP 404"* ]] || die "cannot read codecov.yml on the default branch: ${out}"
    return 1
  fi
  out=$(jq -r 'type == "object" and .type == "file"' <<<"${out}" 2>/dev/null) \
    || die "cannot parse codecov.yml on the default branch."
  [[ "${out}" == "true" ]]
}

# tree_config PREFIX TREE_JSON prints "PREFIX<path> <mode> <sha>" for each
# entry of a Git trees API response whose name Codecov reads as its YAML.
tree_config() {
  jq -r --arg p "$1" --argjson names "${CODECOV_YAML_NAMES}" '
      if (.tree | type) == "array" and (.truncated | not) then .tree[] else error("unexpected tree") end
      | select(.path | IN($names[])) | "\($p)\(.path) \(.mode) \(.sha)"' <<<"$2"
}

# codecov_config REF prints "PATH MODE SHA" for every Codecov YAML path at
# the commit REF, sorted, or nothing. Reads the root tree and the dev/ and
# .github/ trees through the Git trees API, which also serves fork heads. The
# compare API would need only one call, but it diffs against the merge base
# and lists at most 300 files.
codecov_config() {
  local ref=$1 root sub dir out
  root=$(gh api "repos/${REPO}/git/trees/${ref}") || die "cannot read the tree of ${ref}."
  out=$(tree_config "" "${root}") || die "cannot parse the tree of ${ref}."
  for dir in "${CODECOV_YAML_DIRS[@]}"; do
    sub=$(jq -r --arg d "${dir}" '.tree[] | select(.path == $d and .type == "tree") | .sha' <<<"${root}") \
      || die "cannot parse the tree of ${ref}."
    [[ -n "${sub}" ]] || continue
    [[ "${sub}" =~ ^[0-9a-f]{40}$ ]] || die "unexpected tree SHA of ${dir}/ at ${ref}: ${sub}"
    sub=$(gh api "repos/${REPO}/git/trees/${sub}") || die "cannot read ${dir}/ at ${ref}."
    sub=$(tree_config "${dir}/" "${sub}") || die "cannot parse ${dir}/ at ${ref}."
    out+=${sub:+$'\n'${sub}}
  done
  grep . <<<"${out}" | sort || true
}

# main_config_ok SHA: Codecov saves the YAML of a main commit as the
# repository YAML that pull request uploads fall back to. Returns 1 unless SHA
# has codecov.yml as a regular file (mode 100644 or 100755) and no other
# Codecov YAML path.
main_config_ok() {
  local sha=$1 cfg
  cfg=$(codecov_config "${sha}") || exit 1
  [[ "${cfg}" =~ ^codecov\.yml\ 100(644|755)\ [0-9a-f]{40}$ ]] && return 0
  log "Codecov YAML at ${sha}: ${cfg:-none}."
  return 1
}

# pr_config_unchanged BASE HEAD: a pull request head may carry its own Codecov
# YAML, which Codecov would apply to its codecov/* statuses (for example
# without informational) and so could block Tide. A HEAD without any Codecov
# YAML path gets the repository YAML saved from main, so it passes. Otherwise
# returns 1 unless every Codecov YAML path has the same blob (or is missing)
# at HEAD and at its merge base with BASE, the main commit Prow tested HEAD
# on: HEAD then carries main's YAML from where it branched, possibly older
# than BASE's, and adds or edits none. Prow tests on the current main, so
# comparing HEAD with BASE itself would skip every pull request branched
# before the latest change of the YAML on main. The compare API also serves
# fork heads.
pr_config_unchanged() {
  local base=$1 head=$2 h mb m
  h=$(codecov_config "${head}") || exit 1
  [[ -n "${h}" ]] || return 0
  mb=$(gh api "repos/${REPO}/compare/${base}...${head}?per_page=1" | jq -r '.merge_base_commit.sha // ""') \
    || die "cannot compare ${base}...${head}."
  require_sha "${mb}"
  m=$(codecov_config "${mb}") || exit 1
  [[ "${m}" == "${h}" ]] && return 0
  log "Codecov YAML at merge base ${mb}: ${m:-none}; at ${head}: ${h}."
  return 1
}

# Codecov unions all uploads of a commit. Another passing pair of shard builds
# for the same commit (a retest on a newer base, or a Tide retest of a pull
# request head) would add a second tree to the report, so the first upload
# wins. Only finished coverage uploads count: a CODECOV_FLAG upload whose
# state_name is MERGED (state_id 6) or, for older uploads, PROCESSED (2), and
# whose totals are set. Test-results uploads show state "processed" but
# state_name UPLOADED (1) and no totals, so they do not count. While a
# CODECOV_FLAG upload is "started" or "uploaded" without totals, poll for up
# to CODECOV_POLL_TRIES * CODECOV_POLL_SECS seconds. Any other state (error,
# fully_overwritten, partially_overwritten) neither counts nor waits.
# Errors and uploads that stay unfinished fail open: an occasional union is
# less harmful than missing coverage. CODECOV_FORCE=true skips the check, to
# replace a bad or orphaned upload.
codecov_has_upload() {
  local sha=$1 try resp code body counts processed pending
  if [[ "${CODECOV_FORCE}" == "true" ]]; then
    log "CODECOV_FORCE is set; uploading without checking Codecov for ${sha}."
    return 1
  fi
  for ((try = 1; ; try++)); do
    # -f as in gcs_get.
    resp=$(curl -sSf --retry 3 --connect-timeout 20 --max-time 60 -w '\n%{http_code}' \
        "${CODECOV_API}/commits/${sha}/uploads/?page_size=100" 2>/dev/null) || [[ $? == 22 ]] || {
      log "Warning: cannot query Codecov for ${sha}; uploading anyway."
      return 1
    }
    code=${resp##*$'\n'}
    body=${resp%$'\n'*}
    case "${code}" in
      404) log "Codecov has no record of ${sha}."; return 1 ;;
      200) ;;
      *) log "Warning: Codecov returned HTTP ${code} for ${sha}; uploading anyway."; return 1 ;;
    esac
    # "<finished> <unfinished>" coverage uploads with the flag.
    if ! counts=$(jq -r --arg f "${CODECOV_FLAG}" '
        [.results[] | select((.flags // []) | index($f))] as $u
        | ($u | map(select(
              ((.state_name // "") as $n | $n == "MERGED" or $n == "PROCESSED"
                or .state_id == 6 or .state_id == 2)
              and .totals != null)) | length | tostring)
          + " " + ($u | map(select(
              (.state == "started" or .state == "uploaded") and .totals == null))
            | length | tostring)
        ' <<<"${body}" 2>/dev/null) || [[ ! "${counts}" =~ ^[0-9]+\ [0-9]+$ ]]; then
      log "Warning: cannot parse the Codecov uploads of ${sha}; uploading anyway."
      return 1
    fi
    read -r processed pending <<<"${counts}"
    if [[ "${processed}" -gt 0 ]]; then
      log "Codecov already has a finished ${CODECOV_FLAG} upload for ${sha}; keeping the first one."
      return 0
    fi
    if [[ "${pending}" -eq 0 ]]; then
      return 1
    fi
    if [[ "${try}" -ge "${CODECOV_POLL_TRIES}" ]]; then
      log "Warning: ${pending} ${CODECOV_FLAG} upload(s) for ${sha} still unfinished after ${try} checks; uploading anyway."
      return 1
    fi
    log "Codecov is still processing ${pending} ${CODECOV_FLAG} upload(s) for ${sha}; checking again in ${CODECOV_POLL_SECS}s."
    sleep "${CODECOV_POLL_SECS}"
  done
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
  main_has_codecov_yml || skip "The default branch has no codecov.yml (rollout not done); skipping."

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
    pr_config_unchanged "${base}" "${sha}" || skip "PR adds or edits Codecov config; skipping."
    # Codecov names fork branches owner:branch.
    branch=$(gh api "repos/${REPO}/pulls/${pr}" | jq -r --arg r "${REPO}" \
      'if .head.repo.full_name == $r then .head.ref else .head.label end') \
      || die "cannot read pull request ${pr}."
    branch=$(safe_branch "${branch}" "${pr}")
  else
    main_config_ok "${sha}" || skip "${sha} lacks codecov.yml or has another Codecov YAML path; skipping."
  fi
  log "${context} build ${id} merged the unit coverage of ${sha}."

  if codecov_has_upload "${sha}"; then
    echo "skip=true"
    return 0
  fi

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
