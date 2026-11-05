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

# Merges the coverage of the unit shards into one profile. Runs as two Prow jobs:
#
# - pull-kueue-test-unit-coverage-main (presubmit): starts together with the
#   shards, so it has the same refs. It waits until every shard build on
#   exactly these refs has finished, then merges the newest successful build
#   of each shard. (A batch job type works the same way if Tide ever runs it.)
# - post-kueue-test-unit-coverage-main (postsubmit on main): for the pushed
#   squash commit M it picks the shard builds whose tested tree equals M's
#   tree (the PR's own runs on M^, or a Tide batch build). Tide merges only
#   after the shards passed, so nothing waits. A commit is skipped only when
#   no build tested its tree, for example a batch member whose prefix no
#   batch build tested.
#
# Outputs, for the Spyglass coverage lens, TestGrid and the Codecov workflow:
#   $ARTIFACTS/filtered.cov          merged profile of the shards' filtered.cov
#   $ARTIFACTS/filtered.html         per-line view (best effort)
#   $ARTIFACTS/junit_coverage.xml    gopherage junit, OVERALL % for TestGrid
#   $ARTIFACTS/coverage-sources.json what was merged, or why nothing was
#
# Everything is read anonymously from the public bucket and from public git;
# the pod needs no credentials.

set -o errexit -o nounset -o pipefail

GOPHERAGE=${GOPHERAGE:-gopherage}
JQ=${JQ:-jq}
REPO_DIR=${REPO_DIR:-.}
UNIT_TOTAL_SHARDS=${UNIT_TOTAL_SHARDS:-2}
SHARD_JOB_TEMPLATE=${SHARD_JOB_TEMPLATE:-pull-kueue-test-unit-shard-%d-main}
POLL_SECONDS=${POLL_SECONDS:-30}
# A shard of the same trigger can stay unscheduled for up to its
# pod_unscheduled_timeout (18m) and then run for up to its timeout (1h).
WAIT_SECONDS=${WAIT_SECONDS:-5100}
# Give up early if no shard build with our refs shows up at all (for example
# after `/test pull-kueue-test-unit-coverage-main` alone on a new base).
APPEAR_SECONDS=${APPEAR_SECONDS:-1200}
LOOKBACK=${LOOKBACK:-40}
# Tide sets no batch size limit for kueue; recent batches had up to 8 pulls.
MAX_BATCH=${MAX_BATCH:-25}
BUCKET=kubernetes-ci-logs
GCS="https://storage.googleapis.com/${BUCKET}"
GCS_API="https://storage.googleapis.com/storage/v1/b/${BUCKET}/o"

log() { printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }
die() { log "ERROR: $*"; exit 1; }

shard_job() {
  # shellcheck disable=SC2059
  printf "${SHARD_JOB_TEMPLATE}" "$1"
}

# Newest LOOKBACK build IDs under a job prefix, newest first.
list_builds() {
  local prefix=$1 token="" page ids=""
  while :; do
    page=$(curl -sSf --retry 3 --get "${GCS_API}" --data-urlencode "prefix=${prefix}" \
      --data-urlencode delimiter=/ --data-urlencode fields=prefixes,nextPageToken \
      ${token:+--data-urlencode "pageToken=${token}"}) || return 1
    ids+=$("${JQ}" -r '.prefixes[]? | split("/")[-2] | select(test("^[0-9]+$"))' <<<"${page}")$'\n' \
      || return 1
    token=$("${JQ}" -r '.nextPageToken // empty' <<<"${page}") || return 1
    [[ -n "${token}" ]] || break
  done
  # awk reads all input, so a long listing cannot fail with SIGPIPE.
  sort -rn <<<"${ids}" | awk -v n="${LOOKBACK}" 'NF && ++c <= n'
}

# scan JOB PREFIX: one line per build of JOB under PREFIX, newest first:
#   <state> <prefix><build> <canonical refs JSON>
# Returns 1 if the listing or a prowjob.json read or parse fails. A build without
# prowjob.json (HTTP 404; crier writes it minutes after the build starts) is
# left out, except the newest, which is printed as `unknown <prefix><build>`.
scan() {
  local job=$1 prefix=$2 build builds pj newest=1
  builds=$(list_builds "${prefix}") || return 1
  for build in ${builds}; do
    # -f keeps the body of a failed attempt out of a retried response.
    if ! pj=$(curl -sSf --retry 3 -w '\n%{http_code}' "${GCS}/${prefix}${build}/prowjob.json" 2>/dev/null); then
      [[ "${pj##*$'\n'}" == 404 ]] || return 1
      ((newest)) && echo "unknown ${prefix}${build}"
      newest=0 && continue
    fi
    pj=${pj%$'\n'*} newest=0
    # shellcheck disable=SC2016 # jq variables
    "${JQ}" -r --arg job "${job}" --arg path "${prefix}${build}" '
      select(.spec.job == $job)
      | "\(.status.state) \($path) \({type: .spec.type, base: .spec.refs.base_sha,
          pulls: [.spec.refs.pulls[]? | {number, sha}]} | tojson)"' <<<"${pj}" || return 1
  done
}

# Downloads and merges the shard builds' filtered.cov (filtered by
# hack/make/test.mk), given as GCS prefixes, then writes all outputs. $1 is
# the commit the profile describes; $2 the refs JSON.
merge_builds() {
  local commit=$1 refs=$2 work i p inputs=()
  shift 2
  work=$(mktemp -d)
  # shellcheck disable=SC2064 # expand now
  trap "rm -rf '${work}'" EXIT
  i=0
  for p in "$@"; do
    curl -sSf --retry 3 -o "${work}/s${i}.cov" "${GCS}/${p}/artifacts/filtered.cov" 2>/dev/null \
      || die "no filtered.cov in ${p}"
    inputs+=("${work}/s${i}.cov")
    i=$((i + 1))
  done
  "${GOPHERAGE}" merge "${inputs[@]}" -o "${ARTIFACTS}/filtered.cov"
  "${GOPHERAGE}" junit --threshold 0 "${ARTIFACTS}/filtered.cov" >"${ARTIFACTS}/junit_coverage.xml"
  # Prow cloned the tested tree into REPO_DIR; main-module packages resolve
  # without downloading dependencies.
  (cd "${REPO_DIR}" && GOPROXY=off GOFLAGS=-mod=mod go tool cover \
    -html="${ARTIFACTS}/filtered.cov" -o "${ARTIFACTS}/filtered.html") \
    || log "WARNING: filtered.html not written"
  # shellcheck disable=SC2016 # jq variables
  printf '%s\n' "$@" | "${JQ}" -R . | "${JQ}" -s --arg commit "${commit}" --argjson refs "${refs}" \
    --arg job "${JOB_NAME:-}" '{job: $job, commit: $commit, refs: $refs, shards: .}' \
    >"${ARTIFACTS}/coverage-sources.json"
  log "Merged statement coverage for ${commit}: $(awk '/name="OVERALL"/ { f = 1 }
    f && /name="coverage"/ { match($0, /value="[^"]*"/); print substr($0, RSTART + 7, RLENGTH - 8); exit }' \
    "${ARTIFACTS}/junit_coverage.xml")%"
}

# Exits 0 without a profile; coverage-sources.json says why. The Codecov
# workflow skips such builds.
skip() {
  log "Skipping: $1"
  # shellcheck disable=SC2016 # jq variables
  "${JQ}" -n --arg commit "$2" --arg reason "$1" --arg job "${JOB_NAME:-}" \
    '{job: $job, commit: $commit, skipped: $reason}' >"${ARTIFACTS}/coverage-sources.json"
  exit 0
}

# Presubmit and batch: wait for the shard builds that have exactly our refs.
merge_same_refs() {
  local type=$1 want pr prefix i job scanned builds ok running elapsed start picked=() retest
  local unknown seen since
  # shellcheck disable=SC2016 # jq expressions
  want=$("${JQ}" -c '{type: .type, base: .refs.base_sha,
    pulls: [.refs.pulls[] | {number, sha}]}' <<<"${JOB_SPEC}")
  pr=$("${JQ}" -r '.refs.pulls[0].number' <<<"${JOB_SPEC}")
  # One comment with every /test line is one trigger, so one base for all.
  retest=$(for ((i = 0; i < UNIT_TOTAL_SHARDS; i++)); do printf '/test %s\n' "$(shard_job "${i}")"; done
    printf '/test %s' "${JOB_NAME:-pull-kueue-test-unit-coverage-main}")
  log "Waiting for the unit shards on ${want}"
  start=$(date +%s)
  for ((i = 0; i < UNIT_TOTAL_SHARDS; i++)); do
    job=$(shard_job "${i}")
    if [[ "${type}" == presubmit ]]; then prefix="pr-logs/pull/${ORG_REPO}/${pr}/${job}/"
    else prefix="pr-logs/pull/batch/${job}/"; fi
    seen=""
    while :; do
      scanned=$(scan "${job}" "${prefix}") || die "cannot list ${job}"
      builds=$(awk -v w="${want}" '$3 == w' <<<"${scanned}")
      ok=$(awk '$1 == "success" { print $2; exit }' <<<"${builds}")
      running=$(awk '$1 == "triggered" || $1 == "pending" { print $2; exit }' <<<"${builds}")
      elapsed=$(($(date +%s) - start))
      # The newest build may be a retry whose prowjob.json is not written yet.
      # Count it as running until APPEAR_SECONDS after this job first sees it.
      unknown=$(awk '$1 == "unknown" { print $2 }' <<<"${scanned}")
      [[ "${unknown}" == "${seen}" ]] || seen=${unknown} since=$(date +%s)
      [[ -n "${running}" || -z "${unknown}" ]] || (($(date +%s) - since >= APPEAR_SECONDS)) \
        || running=${unknown}
      # Wait for a running retry first. Then any success will do: a newer
      # failed attempt ran the same tree, so it does not void an older success
      # (pick_exact does the same for the postsubmit).
      if [[ -n "${running}" ]]; then
        ((elapsed < WAIT_SECONDS)) || die "${job} ${running} still running after ${elapsed}s"
      elif [[ -n "${ok}" ]]; then
        picked+=("${ok}") && log "${job}: ${ok}" && break
      elif [[ -n "${builds}" ]]; then
        die "${job} did not succeed on these refs; merged coverage needs every shard"
      else
        ((elapsed < APPEAR_SECONDS)) \
          || die "no ${job} build on these refs. To run the shards and this job on one base, comment:"$'\n'"${retest}"
      fi
      sleep "${POLL_SECONDS}"
    done
  done
  merge_builds "$("${JQ}" -r '.refs.pulls[0].sha' <<<"${JOB_SPEC}")" "${want}" "${picked[@]}"
}

# tested_tree REFS_JSON: the tree clonerefs builds for these refs (git merge
# --no-ff of each pull onto the base, in order), or nothing if the pulls
# conflict. Returns 1 if a commit cannot be fetched after three tries.
# clonerefs adds no remote, so fetch from the repository URL.
tested_tree() {
  local refs=$1 c n sha tree try
  c=$("${JQ}" -r .base <<<"${refs}")
  while read -r n sha; do
    for try in 1 2 3; do
      git -C "${REPO_DIR}" cat-file -e "${sha}^{commit}" 2>/dev/null && break
      git -C "${REPO_DIR}" fetch -q "${REPO_URL}" "${sha}" 2>/dev/null \
        || git -C "${REPO_DIR}" fetch -q "${REPO_URL}" "refs/pull/${n}/head" 2>/dev/null \
        || { ((try == 3)) || sleep 10; }
    done
    git -C "${REPO_DIR}" cat-file -e "${sha}^{commit}" 2>/dev/null || { log "cannot fetch ${sha}"; return 1; }
    tree=$(git -C "${REPO_DIR}" merge-tree --write-tree "${c}" "${sha}" 2>/dev/null) || return 0
    c=$(GIT_AUTHOR_NAME=x GIT_AUTHOR_EMAIL=x@x GIT_COMMITTER_NAME=x GIT_COMMITTER_EMAIL=x@x \
      git -C "${REPO_DIR}" commit-tree "${tree}" -p "${c}" -p "${sha}" -m merge) || return 1
  done < <("${JQ}" -r '.pulls[] | "\(.number) \(.sha)"' <<<"${refs}")
  git -C "${REPO_DIR}" rev-parse "${c}^{tree}"
}

# pick_exact TREE LINES: LINES are "<shard> <state> <path> <refs>". For the
# first refs (newest first) on which every shard succeeded and whose tested
# tree is TREE, prints the refs and then one build path per shard. Returns 1
# if none matches, and 2 if none matches but a candidate could not be fetched.
pick_exact() {
  local tree=$1 lines=$2 refs i t failed=0
  while read -r refs; do
    [[ -n "${refs}" ]] || continue
    t=$(tested_tree "${refs}") || { failed=1; continue; }
    [[ "${t}" == "${tree}" ]] || continue
    echo "${refs}"
    for ((i = 0; i < UNIT_TOTAL_SHARDS; i++)); do
      awk -v i="${i}" -v r="${refs}" '$1 == i && $2 == "success" && $4 == r { print $3; exit }' <<<"${lines}"
    done
    return 0
  done < <(awk -v n="${UNIT_TOTAL_SHARDS}" '$2 == "success" && !seen[$4 " " $1]++ {
      c[$4]++; if (!($4 in order)) { order[$4] = ++k; refs[k] = $4 } }
    END { for (j = 1; j <= k; j++) if (c[refs[j]] == n) print refs[j] }' <<<"${lines}")
  return $((failed ? 2 : 1))
}

# merge_picked COMMIT PICKED: PICKED is pick_exact's output.
merge_picked() {
  local paths=()
  mapfile -t paths < <(tail -n +2 <<<"$2")
  merge_builds "$1" "$(head -n 1 <<<"$2")" "${paths[@]}"
}

# Postsubmit on main: find the shard builds whose tested tree is M's tree.
# First the pull request's own presubmits on M^ (serial merges, PR number from
# the "(#N)" squash title), then Tide batches on a base up to MAX_BATCH
# first-parent steps back (a batch member matches only if some batch build
# tested exactly its prefix). The tree comparison is what decides; refs alone
# are only a filter.
merge_postsubmit() {
  local m p1 tree pr i job lines bases picked rc unfetched=0
  m=$("${JQ}" -r .refs.base_sha <<<"${JOB_SPEC}")
  [[ "$(git -C "${REPO_DIR}" rev-parse HEAD)" == "${m}" ]] || die "REPO_DIR is not at ${m}"
  [[ "$(git -C "${REPO_DIR}" rev-list --parents -n 1 "${m}" | wc -w)" -eq 2 ]] \
    || skip "${m} is not a single-parent (squash) commit" "${m}"
  p1=$(git -C "${REPO_DIR}" rev-parse "${m}^")
  tree=$(git -C "${REPO_DIR}" rev-parse "${m}^{tree}")
  pr=$(git -C "${REPO_DIR}" log -1 --format=%s "${m}" | sed -nE 's/.*\(#([0-9]+)\)$/\1/p')

  if [[ -n "${pr}" ]]; then
    lines=""
    for ((i = 0; i < UNIT_TOTAL_SHARDS; i++)); do
      job=$(shard_job "${i}")
      lines+=$(scan "${job}" "pr-logs/pull/${ORG_REPO}/${pr}/${job}/" \
        | awk -v i="${i}" -v b="${p1}" 'index($3, "\"type\":\"presubmit\"") && index($3, "\"base\":\"" b "\"") { print i, $0 }')$'\n' \
        || die "cannot list ${job}"
    done
    rc=0 && picked=$(pick_exact "${tree}" "${lines}") || rc=$?
    if ((rc == 0)); then
      log "PR ${pr} tested the tree of ${m} on ${p1}"
      merge_picked "${m}" "${picked}"
      return
    fi
    ((rc == 1)) || unfetched=1
  fi

  bases=$(git -C "${REPO_DIR}" rev-list --first-parent -n "$((MAX_BATCH + 1))" "${m}" | tail -n +2)
  lines=""
  for ((i = 0; i < UNIT_TOTAL_SHARDS; i++)); do
    job=$(shard_job "${i}")
    lines+=$(scan "${job}" "pr-logs/pull/batch/${job}/" \
      | awk -v i="${i}" -v bases="${bases//$'\n'/ }" 'BEGIN { split(bases, a, " "); for (k in a) ok[a[k]] = 1 }
          { match($3, /"base":"[0-9a-f]+"/); if (substr($3, RSTART + 8, 40) in ok) print i, $0 }')$'\n' \
      || die "cannot list ${job}"
  done
  rc=0 && picked=$(pick_exact "${tree}" "${lines}") || rc=$?
  if ((rc == 0)); then
    log "A Tide batch tested the tree of ${m}"
    merge_picked "${m}" "${picked}"
    return
  fi
  # A commit that could not be fetched may be the match; fail so that a rerun
  # can find it, instead of skipping the commit for good.
  ((rc == 1 && !unfetched)) || die "no match for ${m}, and a candidate's commits could not be fetched"
  skip "no unit shard build tested the tree of ${m} (for example a batch member whose prefix no batch build tested, or unit jobs skipped)" "${m}"
}

main() {
  : "${JOB_SPEC:?JOB_SPEC is set by Prow}"
  : "${ARTIFACTS:?ARTIFACTS is set by Prow}"
  # shellcheck disable=SC2016 # jq expressions
  ORG_REPO=$("${JQ}" -r '.refs.org + "_" + .refs.repo' <<<"${JOB_SPEC}")
  # shellcheck disable=SC2016 # jq expressions
  REPO_URL=$("${JQ}" -r '.refs.clone_uri // "https://github.com/\(.refs.org)/\(.refs.repo).git"' <<<"${JOB_SPEC}")
  mkdir -p "${ARTIFACTS}"
  case "$("${JQ}" -r .type <<<"${JOB_SPEC}")" in
    presubmit) merge_same_refs presubmit ;;
    batch) merge_same_refs batch ;;
    postsubmit) merge_postsubmit ;;
    *) die "unsupported job type" ;;
  esac
}

# A test can source this file.
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
