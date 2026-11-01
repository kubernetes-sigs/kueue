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

# Finds the Prow unit-test shard builds that tested one commit's tree and
# downloads their coverprofiles from the public GCS bucket. It never runs code
# from the commit under test; the profiles are treated as data. The workflow
# merges and filters them with gopherage.
#
# Usage:
#   fetch-prow-unit-coverage.sh pr SHA OUTDIR
#       SHA is a pull request head. Uses the newest base on which every
#       pull-kueue-test-unit-shard-*-main job has a success status on SHA
#       (crier appends BaseSHA:<base> to the description), so a newer failed
#       or pending retest of one shard does not hide an older passing pair.
#       Prints skip=true while no such base exists.
#   fetch-prow-unit-coverage.sh main SHA OUTDIR
#       SHA is a squash commit on main. Tide merges only after the required
#       presubmits passed on the current main, so the tested tree usually is
#       SHA's tree. The pull request comes from commits/SHA/pulls (retried,
#       because GitHub can list it a few seconds after the push), else from
#       the "(#N)" suffix of the squash title, accepted only if pulls/N was
#       merged into main as SHA. A single-parent commit that maps to no pull
#       request is an error. Uses, in order:
#       1. the merged pull request's shard statuses (target URL of a build of
#          that pull request) whose description ends in BaseSHA:<parent of
#          SHA> (a presubmit run on that base);
#       2. a Tide batch whose base and pull requests are the run of squash
#          commits ending at SHA (batches post no statuses, so their GCS
#          directory is listed);
#       3. otherwise skip=true: a batch member that is not the last one, or a
#          pull request that only changes paths the unit jobs skip. Codecov
#          then compares against the nearest ancestor with a report.
# Both modes print skip=true while the default branch has no codecov.yml, when
# the Codecov YAML that would apply to SHA may differ from main's (see
# main_config_ok and pr_config_unchanged), and when Codecov already has a
# finished CODECOV_FLAG upload for SHA (see codecov_has_upload) unless
# CODECOV_FORCE is true. Both fail when a successful shard status lacks the
# BaseSHA:<base> suffix, so a change of crier's format does not skip silently,
# and when a GitHub or GCS read fails in a way that leaves open whether a
# matching build exists (only a 404 or a mismatch skips a build).
#
# Prints key=value lines (skip, sha, pr, branch, build_id, build_url, profile)
# for $GITHUB_OUTPUT, each checked by check_outputs, and writes OUTDIR/cover-shard-N.out: each shard's
# filtered.cov when every shard archived one, else the raw cover-shard-N.out.
#
# Environment:
#   GH_TOKEN            used by the gh CLI for GitHub API calls
#   GITHUB_REPOSITORY   defaults to kubernetes-sigs/kueue
#   UNIT_TOTAL_SHARDS   defaults to 2
#   BATCH_LOOKBACK      newest batch builds per shard to inspect, defaults to 30
#   CODECOV_FLAG        upload flag of the workflow, defaults to unit
#   CODECOV_FORCE       the string true to upload even when Codecov has an upload
#   CODECOV_POLL_TRIES  Codecov queries while an upload is unfinished, defaults to 10
#   CODECOV_POLL_SECS   seconds between those queries, defaults to 30
#   PULLS_TRIES         commits/SHA/pulls reads before the title fallback, defaults to 3
#   PULLS_RETRY_SECS    seconds between those reads, defaults to 20

set -o errexit
set -o nounset
set -o pipefail

REPO="${GITHUB_REPOSITORY:-kubernetes-sigs/kueue}"
SHARDS="${UNIT_TOTAL_SHARDS:-2}"
BATCH_LOOKBACK="${BATCH_LOOKBACK:-30}"
MAX_BATCH=10 # largest Tide batch to match; observed batches had 2-4 pull requests
BUCKET="kubernetes-ci-logs"
GCS="https://storage.googleapis.com/${BUCKET}"
PROW_VIEW="https://prow.k8s.io/view/gs/${BUCKET}"
GCS_ORG_REPO="${REPO/\//_}"
MAX_PROFILE_BYTES=$((512 * 1024 * 1024))
CODECOV_FLAG="${CODECOV_FLAG:-unit}"
CODECOV_FORCE="${CODECOV_FORCE:-false}"
CODECOV_POLL_TRIES="${CODECOV_POLL_TRIES:-10}"
CODECOV_POLL_SECS="${CODECOV_POLL_SECS:-30}"
PULLS_TRIES="${PULLS_TRIES:-3}"
PULLS_RETRY_SECS="${PULLS_RETRY_SECS:-20}"
CODECOV_API="https://api.codecov.io/api/v2/github/${REPO%%/*}/repos/${REPO#*/}"

die() { echo "Error: $*" >&2; exit 1; }
log() { echo "$*" >&2; }
job_name() { echo "pull-kueue-test-unit-shard-$1-main"; }

# gcs_get OBJECT prints OBJECT. Returns 4 when GCS has no such object (HTTP
# 404), and 1 on any other failure, which callers treat as fatal.
gcs_get() {
  local out code
  out=$(curl -sSL --retry 3 --connect-timeout 20 --max-time 120 -w '\n%{http_code}' "${GCS}/$1") || return 1
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
  curl -sSfL --retry 3 --connect-timeout 20 --max-time 600 --max-filesize "${MAX_PROFILE_BYTES}" \
    -o "$2" "${GCS}/$1"
}

# Prints the newest BATCH_LOOKBACK build IDs of a batch job, newest first.
# Build IDs grow over time. Returns 1 when a listing page cannot be read or
# parsed: callers run this in an || list, where errexit is off, so every step
# checks its own exit code.
gcs_batch_builds() {
  local job=$1 token="" ids="" page page_ids
  while :; do
    page=$(curl -sSfL --retry 3 --connect-timeout 20 --max-time 120 -G \
      "https://storage.googleapis.com/storage/v1/b/${BUCKET}/o" \
      --data-urlencode "prefix=pr-logs/pull/batch/${job}/" --data-urlencode "delimiter=/" \
      --data-urlencode "fields=prefixes,nextPageToken" \
      ${token:+--data-urlencode "pageToken=${token}"}) || return 1
    page_ids=$(jq -r 'if type == "object" then (.prefixes // [])[] else error("not an object") end
        | split("/")[4] | select(test("^[0-9]+$"))' <<<"${page}") \
      || { log "cannot parse the batch listing of ${job}."; return 1; }
    ids+=${page_ids}$'\n'
    token=$(jq -r '.nextPageToken // ""' <<<"${page}") \
      || { log "cannot parse the batch listing of ${job}."; return 1; }
    [[ -n "${token}" ]] || break
  done
  sort -n <<<"${ids}" | grep . | tail -n "${BATCH_LOOKBACK}" | sort -rn || true
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
# has codecov.yml as a regular file and no other Codecov YAML path.
main_config_ok() {
  local sha=$1 cfg
  cfg=$(codecov_config "${sha}") || exit 1
  [[ "${cfg}" =~ ^codecov\.yml\ 100644\ [0-9a-f]{40}$ ]] && return 0
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
    if ! resp=$(curl -sS --retry 3 --connect-timeout 20 --max-time 60 -w '\n%{http_code}' \
        "${CODECOV_API}/commits/${sha}/uploads/?page_size=100"); then
      log "Warning: cannot query Codecov for ${sha}; uploading anyway."
      return 1
    fi
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

# Selected builds, one per shard, set by the find_* functions.
BUILD_PATHS=()
BUILD_IDS=""
TREE=""

# add_build PATH ID BASE COMMIT records a shard build. Prow tests a merge of
# the pull requests into BASE; started.json repo-commit is that merge. Returns
# 1 when the build tested a different tree than the shards added before it:
# the union of two trees is meaningless.
add_build() {
  local path=$1 id=$2 tree="$3/$4"
  require_sha "$3"
  require_sha "$4"
  if [[ -n "${TREE}" && "${tree}" != "${TREE}" ]]; then
    log "Shards tested different trees (base/merge ${TREE} and ${tree})."
    return 1
  fi
  TREE="${tree}"
  BUILD_PATHS+=("${path}")
  BUILD_IDS="${BUILD_IDS:+${BUILD_IDS}-}${id}"
}

# check_presubmit JOB TARGET_URL PR HEAD [BASE] validates the build a status
# points at: a successful presubmit of JOB that tested only PR at HEAD (and on
# BASE, when given). Adds it with add_build; returns 1 if its tree differs.
check_presubmit() {
  local job=$1 url=$2 pr=$3 head=$4 want_base=${5:-}
  local re="^${PROW_VIEW//./\\.}/(pr-logs/pull/${GCS_ORG_REPO}/([0-9]+)/${job}/([0-9]+))/?$"
  [[ "${url}" =~ ${re} ]] || die "unexpected target URL for ${job}: ${url}"
  local path="${BASH_REMATCH[1]}" url_pr="${BASH_REMATCH[2]}" id="${BASH_REMATCH[3]}"
  [[ "${url_pr}" == "${pr}" ]] || die "${job} build ${id} belongs to PR ${url_pr}, expected ${pr}."
  local finished prowjob base started
  finished=$(gcs_get "${path}/finished.json") || die "cannot read ${path}/finished.json."
  [[ "$(jq -r '.result // ""' <<<"${finished}")" == "SUCCESS" ]] || die "${job} build ${id} did not succeed."
  [[ "$(jq -r '.revision // ""' <<<"${finished}")" == "${head}" ]] || die "${job} build ${id} did not test ${head}."
  prowjob=$(gcs_get "${path}/prowjob.json") || die "cannot read ${path}/prowjob.json."
  jq -e --arg j "${job}" --argjson n "${pr}" --arg s "${head}" \
    '.spec.type == "presubmit" and .spec.job == $j and .spec.refs.pulls == [.spec.refs.pulls[0]]
     and .spec.refs.pulls[0].number == $n and .spec.refs.pulls[0].sha == $s' <<<"${prowjob}" >/dev/null \
    || die "${job} build ${id} is not a presubmit of only PR ${pr} at ${head}."
  base=$(jq -r '.spec.refs.base_sha // ""' <<<"${prowjob}")
  [[ -z "${want_base}" || "${base}" == "${want_base}" ]] || die "${job} build ${id} tested base ${base}, expected ${want_base}."
  started=$(gcs_get "${path}/started.json") || die "cannot read ${path}/started.json."
  add_build "${path}" "${id}" "${base}" "$(jq -r '."repo-commit" // ""' <<<"${started}")"
}

# statuses SHA prints every commit status of SHA as one JSON array. The
# combined status endpoint returns at most 100 contexts, so list them all.
statuses() {
  gh api --paginate "repos/${REPO}/commits/$1/statuses?per_page=100" | jq -cs 'add // []'
}

# shard_contexts prints the shard job names as a JSON array.
shard_contexts() {
  local shard
  for ((shard = 0; shard < SHARDS; shard++)); do job_name "${shard}"; done | jq -Rcn '[inputs]'
}

# require_base_suffix HEAD STATUSES fails when a successful shard status lacks
# the BaseSHA:<40-hex> suffix that crier appends to the description. Both
# modes select builds by that suffix; without this check, a format change
# would skip every upload without an error.
require_base_suffix() {
  local head=$1 bad
  bad=$(jq -r --argjson cs "$(shard_contexts)" '
      [.[] | select(.state == "success" and (.context | IN($cs[]))
                    and ((.description // "") | test("BaseSHA:[0-9a-f]{40}$") | not))]
      | length' <<<"$2") || die "cannot parse the statuses of ${head}."
  [[ "${bad}" == "0" ]] \
    || die "Prow status format changed: ${bad} successful shard status(es) on ${head} lack the BaseSHA:<40-hex> description suffix."
}

# find_pr_builds HEAD: the newest base on which every shard has a success
# status on HEAD. Statuses are grouped by the BaseSHA:<base> suffix that crier
# appends to the description, so a newer failed or pending retest of one
# shard does not hide an older passing pair on one base, and by the pull
# request in the target URL, since two pull requests can share a head. Sets
# FOUND_PR and FOUND_BASE.
find_pr_builds() {
  local head=$1 all contexts pairs line shard job state missing=0
  local -a f
  contexts=$(shard_contexts)
  all=$(statuses "${head}") || die "cannot list the statuses of ${head}."
  require_base_suffix "${head}" "${all}"
  # One line per (base, pull request) on which every shard succeeded, newest
  # pair first: the base, the pull request, then the target URL of each
  # shard's newest success on it. check_presubmit rejects a target URL
  # without a pull request number ("-").
  pairs=$(jq -r --argjson cs "${contexts}" '
      [.[] | select(.state == "success" and (.context | IN($cs[]))
                    and ((.description // "") | test("BaseSHA:[0-9a-f]{40}$")))
           | .base = .description[-40:]
           | .pr = ((.target_url // "") | capture("/pr-logs/pull/[^/]+/(?<n>[0-9]+)/").n // "-")]
      | group_by([.base, .pr])
      | map(group_by(.context) | map(max_by([.created_at, .id])))
      | map(select(length == ($cs | length)))
      | sort_by(map([.created_at, .id]) | max) | reverse | .[]
      | [.[0].base, .[0].pr] + (. as $g | $cs | map(. as $c | $g[] | select(.context == $c) | .target_url))
      | join(" ")' <<<"${all}") || die "cannot parse the statuses of ${head}."
  while IFS= read -r line; do
    [[ -n "${line}" ]] || continue
    read -r -a f <<<"${line}"
    BUILD_PATHS=(); BUILD_IDS=""; TREE=""
    for ((shard = 0; shard < SHARDS; shard++)); do
      check_presubmit "$(job_name "${shard}")" "${f[shard + 2]}" "${f[1]}" "${head}" "${f[0]}" || continue 2
    done
    FOUND_BASE="${f[0]}"
    FOUND_PR="${f[1]}"
    return 0
  done <<<"${pairs}"
  for ((shard = 0; shard < SHARDS; shard++)); do
    job=$(job_name "${shard}")
    state=$(jq -r --arg c "${job}" 'map(select(.context == $c)) | max_by([.created_at, .id]) | .state // "missing"' <<<"${all}")
    [[ "${state}" == "success" ]] || log "The newest ${job} status on ${head} is ${state}."
    jq -e --arg c "${job}" 'any(.[]; .context == $c and .state == "success")' <<<"${all}" >/dev/null || missing=1
  done
  if ((missing == 1)); then
    log "Not every shard has succeeded on ${head}; nothing to upload yet."
  else
    local cmds=""
    for ((shard = 0; shard < SHARDS; shard++)); do cmds+="${cmds:+ and }'/test $(job_name "${shard}")'"; done
    log "The shards passed only on different bases or trees. To get a report, post ${cmds} in one PR comment, so that all shards test one base."
  fi
  return 1
}

# find_serial_builds PARENT PR HEAD: presubmits of PR at HEAD on base PARENT,
# found through their statuses on HEAD (crier appends BaseSHA:<base>). Two
# pull requests can share a head, so only statuses whose target URL is a
# build of PR count; without one per shard, the caller tries the batches.
find_serial_builds() {
  local parent=$1 pr=$2 head=$3 all shard job url re
  all=$(statuses "${head}") || die "cannot list the statuses of ${head}."
  require_base_suffix "${head}" "${all}"
  for ((shard = 0; shard < SHARDS; shard++)); do
    job=$(job_name "${shard}")
    re="^${PROW_VIEW//./\\.}/pr-logs/pull/${GCS_ORG_REPO}/${pr}/${job}/[0-9]+/?$"
    url=$(jq -r --arg c "${job}" --arg b "BaseSHA:${parent}" --arg re "${re}" \
      'map(select(.context == $c and .state == "success" and ((.description // "") | endswith($b))
                  and ((.target_url // "") | test($re))))
       | max_by([.created_at, .id]) | .target_url // ""' <<<"${all}") \
      || die "cannot parse the statuses of ${head}."
    if [[ -z "${url}" ]]; then
      log "${job} has no successful status of PR ${pr} on head ${head} with BaseSHA:${parent}."
      return 1
    fi
    check_presubmit "${job}" "${url}" "${pr}" "${head}" "${parent}" || return 1
  done
}

# merged_pr SHA prints "PARENT PR HEAD" for a squash commit on main. Returns
# 3 when SHA does not have exactly one parent, and 4 when no pull request into
# main was merged as SHA. GitHub can list a pull request under
# commits/SHA/pulls a few seconds after the push, so that list is read
# PULLS_TRIES times, PULLS_RETRY_SECS apart. Then the "(#N)" suffix of the
# squash title is used, if pulls/N was merged into main as SHA.
merged_pr() {
  local sha=$1 commit parents pr="" n try
  # shellcheck disable=SC2016 # a jq program
  local match='select(.merge_commit_sha == $s and .base.ref == "main") | "\(.number) \(.head.sha)"'
  commit=$(gh api "repos/${REPO}/commits/${sha}") || die "cannot read commit ${sha}."
  parents=$(jq -r '[.parents[].sha] | join(" ")' <<<"${commit}") || die "cannot parse commit ${sha}."
  [[ "${parents}" =~ ^[0-9a-f]{40}$ ]] || { log "${sha} does not have exactly one parent (${parents})."; return 3; }
  for ((try = 1; try <= PULLS_TRIES; try++)); do
    pr=$(gh api "repos/${REPO}/commits/${sha}/pulls" | jq -r --arg s "${sha}" "first(.[] | ${match}) // empty") \
      || die "cannot read the pull requests of ${sha}."
    [[ -z "${pr}" && "${try}" -lt "${PULLS_TRIES}" ]] || break
    log "GitHub lists no pull request merged as ${sha} yet; checking again in ${PULLS_RETRY_SECS}s."
    sleep "${PULLS_RETRY_SECS}"
  done
  if [[ -z "${pr}" ]]; then
    n=$(jq -r '.commit.message // "" | split("\n")[0]' <<<"${commit}" | sed -nE 's/.*\(#([0-9]+)\)$/\1/p')
    if [[ -n "${n}" ]]; then
      pr=$(gh api "repos/${REPO}/pulls/${n}" | jq -r --arg s "${sha}" "${match}") \
        || die "cannot read pull request ${n}."
      [[ -z "${pr}" ]] || log "${sha} maps to PR ${n} through its title."
    fi
  fi
  [[ -n "${pr}" ]] || { log "No pull request into main was merged as ${sha}."; return 4; }
  echo "${parents} ${pr}"
}

# find_batch_builds SHA "PARENT PR HEAD": a Tide batch whose base plus pull
# requests equal the run of squash commits ending at SHA (compared as sets).
# Tide merges a batch one pull request at a time, so only the last commit of
# the run has the tested tree. Collects every successful matching build of
# each shard, then uses the newest tree that every shard tested. A missing
# object (HTTP 404) or a mismatch skips a build; any other read or parse
# failure exits, since it leaves open whether a matching build exists.
find_batch_builds() {
  local sha=$1 shard job ids id path pj m k base i want have rc obj tree
  local -a run=("$2") found=()   # run: "PARENT PR HEAD" per commit, newest first
  local pr=${run[0]#* }
  for ((shard = 0; shard < SHARDS; shard++)); do
    job=$(job_name "${shard}")
    ids=$(gcs_batch_builds "${job}") || die "cannot list the batch builds of ${job}."
    found[shard]=""   # "BASE/REPO_COMMIT ID" per successful match, newest first
    for id in ${ids}; do
      path="pr-logs/pull/batch/${job}/${id}"
      rc=0
      pj=$(gcs_get "${path}/prowjob.json") || rc=$?
      ((rc == 0)) || { ((rc == 4)) || die "cannot read ${path}/prowjob.json."; continue; }
      # "<number of pull requests> <base>" for a batch of job on main that
      # includes the pull request, else "no".
      m=$(jq -r --arg j "${job}" --arg r "${REPO}" --arg p "${pr}" '
          if .spec.type == "batch" and .spec.job == $j and "\(.spec.refs.org)/\(.spec.refs.repo)" == $r
             and .spec.refs.base_ref == "main" and any(.spec.refs.pulls[]; "\(.number) \(.sha)" == $p)
          then "\(.spec.refs.pulls | length) \(.spec.refs.base_sha)" else "no" end' <<<"${pj}") \
        || die "cannot parse ${path}/prowjob.json."
      [[ "${m}" != "no" ]] || continue
      read -r k base <<<"${m}"
      if ((k > MAX_BATCH)); then
        log "${job} build ${id}: batch larger than MAX_BATCH (${k} > ${MAX_BATCH} pull requests); ignoring it."
        continue
      fi
      for ((i = ${#run[@]}; i < k; i++)); do
        rc=0
        run[i]=$(merged_pr "${run[i - 1]%% *}") || rc=$?
        ((rc == 0)) || { ((rc == 3 || rc == 4)) || exit "${rc}"; unset 'run[i]'; continue 2; }
      done
      want=$(for ((i = 0; i < k; i++)); do echo "${run[i]#* }"; done | sort)
      have=$(jq -r '.spec.refs.pulls[] | "\(.number) \(.sha)"' <<<"${pj}" | sort) \
        || die "cannot parse ${path}/prowjob.json."
      [[ "${want}" == "${have}" && "${base}" == "${run[k - 1]%% *}" ]] || continue
      rc=0
      obj=$(gcs_get "${path}/finished.json") || rc=$?
      # No finished.json: the build has not finished.
      ((rc == 0)) || { ((rc == 4)) || die "cannot read ${path}/finished.json."; continue; }
      obj=$(jq -r '.result // ""' <<<"${obj}") || die "cannot parse ${path}/finished.json."
      [[ "${obj}" == "SUCCESS" ]] || continue
      rc=0
      obj=$(gcs_get "${path}/started.json") || rc=$?
      ((rc == 0)) || { ((rc == 4)) || die "cannot read ${path}/started.json."; continue; }
      obj=$(jq -r '."repo-commit" // ""' <<<"${obj}") || die "cannot parse ${path}/started.json."
      require_sha "${base}"
      require_sha "${obj}"
      found[shard]+="${base}/${obj} ${id}"$'\n'
    done
    if [[ -z "${found[shard]}" ]]; then
      log "No successful ${job} batch among the newest ${BATCH_LOOKBACK} tested the run of merges ending at ${sha}."
      return 1
    fi
  done
  # The newest tree of shard 0 that every shard tested; add_build records the
  # newest build of each shard on it.
  while read -r tree _; do
    [[ -n "${tree}" ]] || continue
    BUILD_PATHS=(); BUILD_IDS=""; TREE=""
    for ((shard = 0; shard < SHARDS; shard++)); do
      id=$(awk -v t="${tree}" '$1 == t { print $2; exit }' <<<"${found[shard]}")
      [[ -n "${id}" ]] || continue 2
      add_build "pr-logs/pull/batch/$(job_name "${shard}")/${id}" "${id}" "${tree%/*}" "${tree#*/}"
    done
    return 0
  done <<<"${found[0]}"
  BUILD_PATHS=(); BUILD_IDS=""; TREE=""
  log "The successful batch builds of the shards for the run ending at ${sha} tested different trees."
  return 1
}

# download_profiles OUTDIR writes one profile per shard to
# OUTDIR/cover-shard-N.out: filtered.cov from every build when all of them
# have it, else the raw cover-shard-N.out. Sets PROFILE_KIND to "filtered" or
# "raw". Exits non-zero, and removes the profiles, unless every shard's
# profile downloaded and is non-empty, so one shard is never uploaded alone.
PROFILE_KIND=""
download_profiles() {
  local outdir=$1 kind=filtered shard path name file
  [[ "${#BUILD_PATHS[@]}" -eq "${SHARDS}" ]] || die "found ${#BUILD_PATHS[@]} shard builds, expected ${SHARDS}."
  for path in "${BUILD_PATHS[@]}"; do
    gcs_exists "${path}/artifacts/filtered.cov" || kind=raw
  done
  rm -f "${outdir}"/cover-shard-*.out
  for ((shard = 0; shard < SHARDS; shard++)); do
    name="filtered.cov"
    [[ "${kind}" == "filtered" ]] || name="cover-shard-${shard}.out"
    file="${outdir}/cover-shard-${shard}.out"
    log "Downloading ${GCS}/${BUILD_PATHS[shard]}/artifacts/${name}"
    if ! gcs_download "${BUILD_PATHS[shard]}/artifacts/${name}" "${file}" || [[ ! -s "${file}" ]]; then
      rm -f "${outdir}"/cover-shard-*.out
      die "cannot download a non-empty ${name} of shard ${shard} from ${BUILD_PATHS[shard]}."
    fi
  done
  PROFILE_KIND="${kind}"
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

main() {
  local mode=${1:-} sha=${2:-} outdir=${3:-}
  [[ "$#" -eq 3 && ( "${mode}" == "pr" || "${mode}" == "main" ) ]] || die "usage: $0 {pr|main} SHA OUTDIR"
  require_sha "${sha}"
  mkdir -p "${outdir}"
  main_has_codecov_yml || skip "The default branch has no codecov.yml (rollout not done); skipping."

  local pr="" branch=main build_url info parent head
  if [[ "${mode}" == "pr" ]]; then
    FOUND_PR=""
    FOUND_BASE=""
    find_pr_builds "${sha}" || skip "No pair of successful shard builds on one tree for ${sha}."
    pr_config_unchanged "${FOUND_BASE}" "${sha}" || skip "PR adds or edits Codecov config; skipping."
    pr="${FOUND_PR}"
    # Codecov names fork branches owner:branch.
    branch=$(gh api "repos/${REPO}/pulls/${pr}" | jq -r --arg r "${REPO}" \
      'if .head.repo.full_name == $r then .head.ref else .head.label end') \
      || die "cannot read pull request ${pr}."
    branch=$(safe_branch "${branch}" "${pr}")
    # One upload combines several Prow builds; link the PR's Prow history.
    build_url="https://prow.k8s.io/pr-history/?org=${REPO%%/*}&repo=${REPO#*/}&pr=${pr}"
  else
    main_config_ok "${sha}" || skip "${sha} lacks codecov.yml or has another Codecov YAML path; skipping."
    local rc=0
    info=$(merged_pr "${sha}") || rc=$?
    case "${rc}" in
      0) ;;
      3) skip "${sha} is not a single-parent commit; skipping." ;;
      4) die "${sha}: cannot map the commit to a merged pull request." ;;
      *) exit "${rc}" ;;
    esac
    read -r parent pr head <<<"${info}"
    log "${sha} squashes PR ${pr} (head ${head}) onto ${parent}."
    if ! find_serial_builds "${parent}" "${pr}" "${head}"; then
      BUILD_PATHS=(); BUILD_IDS=""; TREE=""
      find_batch_builds "${sha}" "${info}" \
        || skip "${sha}: no shard builds tested its tree (batch member before the last, or unit tests not required); Codecov uses the nearest ancestor report."
    fi
    pr=""
    # Codecov takes one build URL; this is the last shard's build.
    build_url="${PROW_VIEW}/${BUILD_PATHS[${#BUILD_PATHS[@]} - 1]}"
  fi
  log "All shards tested ${sha}'s tree (base/merge ${TREE}): ${BUILD_PATHS[*]}"

  if codecov_has_upload "${sha}"; then
    echo "skip=true"
    return 0
  fi

  check_outputs "${sha}" "${pr}" "${branch}" "${BUILD_IDS}" "${build_url}"
  download_profiles "${outdir}"
  echo "skip=false"
  echo "sha=${sha}"
  echo "pr=${pr}"
  echo "branch=${branch}"
  echo "build_id=${BUILD_IDS}"
  echo "build_url=${build_url}"
  echo "profile=${PROFILE_KIND}"
}

# fetch-prow-unit-coverage_test.sh sources this file.
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
