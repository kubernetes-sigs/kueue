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

# Self-test for merge-unit-coverage.sh, without network access. A local bare
# repository stands in for GitHub (main and refs/pull/N/head), so git fetch
# and git merge-tree run for real. curl, sleep, go and gopherage are replaced
# by functions; curl serves the GCS listing and objects from a fixture tree.
# Usage: merge-unit-coverage_test.sh

set -o errexit
set -o nounset
set -o pipefail

tmp=$(mktemp -d)
trap 'rm -rf "${tmp}"' EXIT
# shellcheck source=hack/testing/merge-unit-coverage.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/merge-unit-coverage.sh"

# fail inside out=$(run ...) runs in a subshell, so a counter variable would
# be lost and the suite could print FAIL and still exit 0. A file survives.
failures=${tmp}/failures
: >"${failures}"
fail() { echo "FAIL: $*" >&2; echo x >>"${failures}"; }

# Keep the user's git config (signing, hooks) out of the test.
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.com
export GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.com

FX=${tmp}/gcs
ORIGIN=${tmp}/origin.git
GOPHERAGE=fake_gopherage
JOB_NAME=post-kueue-test-unit-coverage-main
POLL_SECONDS=0
SHARD0=pull-kueue-test-unit-shard-0-main
SHARD1=pull-kueue-test-unit-shard-1-main
PR_LOGS="pr-logs/pull/kubernetes-sigs_kueue"
BATCH="pr-logs/pull/batch"

# curl serves https://storage.googleapis.com/kubernetes-ci-logs/PATH from
# ${FX}/PATH. A --get on the JSON API lists the build directories under the
# prefix. With -f a missing object exits 22, still printing -w like real curl.
curl() {
  local url="" out="" w="" get=0 prefix="" f
  while (($#)); do
    case "$1" in
      --retry) shift ;;
      -o) out=$2 && shift ;;
      -w) w=$2 && shift ;;
      --get) get=1 ;;
      --data-urlencode) [[ "$2" != prefix=* ]] || prefix=${2#prefix=}; shift ;;
      -*) ;;
      *) url=$1 ;;
    esac
    shift
  done
  if ((get)); then
    printf '{"prefixes":['
    f=""
    for d in "${FX}/${prefix}"*/; do
      [[ -d "${d}" ]] || continue
      printf '%s"%s"' "${f}" "${d#"${FX}/"}" && f=,
    done
    printf ']}'
    return
  fi
  f=${FX}/${url#https://storage.googleapis.com/kubernetes-ci-logs/}
  if [[ ! -f "${f}" ]]; then
    [[ -z "${w}" ]] || printf '\n404'
    return 22
  fi
  if [[ -n "${out}" ]]; then cp "${f}" "${out}"; else cat "${f}"; fi
  [[ -z "${w}" ]] || printf '\n200'
}

# sleep runs SLEEP_HOOK, if set, and stops a run that would wait forever.
sleeps=0
SLEEP_HOOK=""
sleep() {
  sleeps=$((sleeps + 1))
  ((sleeps < 20)) || { echo "sleep called too often" >&2; exit 99; }
  [[ -z "${SLEEP_HOOK}" ]] || "${SLEEP_HOOK}"
}

# No Go toolchain needed: filtered.html is best effort.
go() { return 1; }

fake_gopherage() {
  local out
  case "$1" in
    merge)
      shift
      out=${*: -1}
      cat "${@:1:$#-2}" >"${out}"
      ;;
    junit)
      printf '<testsuite>\n<testcase name="OVERALL">\n<property name="coverage" value="50.0"/>\n</testcase>\n</testsuite>\n'
      ;;
    *) echo "unexpected gopherage $*" >&2; return 1 ;;
  esac
}

# build PREFIX ID STATE TYPE BASE PULLS [TOTAL]: a shard build with prowjob.json
# and filtered.cov. PREFIX ends with the job name; PULLS is "N:SHA ...".
# TOTAL is the shard job's UNIT_TOTAL_SHARDS (default 2). "-" omits it.
build() {
  local prefix=$1 id=$2 state=$3 type=$4 base=$5 pulls="" n total=${7:-2} envjson=""
  for n in ${6:-}; do pulls+="${pulls:+,}{\"number\":${n%%:*},\"sha\":\"${n#*:}\"}"; done
  if [[ "${total}" != "-" ]]; then
    envjson="{\"name\":\"UNIT_TOTAL_SHARDS\",\"value\":\"${total}\"}"
  fi
  mkdir -p "${FX}/${prefix}/${id}/artifacts"
  printf '{"spec":{"job":"%s","type":"%s","pod_spec":{"containers":[{"env":[%s]}]},"refs":{"base_sha":"%s","pulls":[%s]}},"status":{"state":"%s"}}\n' \
    "${prefix##*/}" "${type}" "${envjson}" "${base}" "${pulls}" "${state}" >"${FX}/${prefix}/${id}/prowjob.json"
  printf 'mode: atomic\nsigs.k8s.io/kueue/pkg/%s.go:1.1,2.2 1 1\n' "${id}" >"${FX}/${prefix}/${id}/artifacts/filtered.cov"
}

# History: B0, then squash commits M1..M4 of PRs 1..4, each changing its own
# file. PRs 1 and 2 were tested on B0, PRs 2..4 as one Tide batch on M1, and
# PR 2 alone as an earlier batch on M1. Batch 203 tests M3's tree but fails
# on shard 1.
src=${tmp}/src
git init -q -b main "${src}"
for f in a b c d; do echo "${f}" >"${src}/${f}"; done
git -C "${src}" add . && git -C "${src}" commit -q -m base
B0=$(git -C "${src}" rev-parse HEAD)
git init -q --bare -b main "${ORIGIN}"
pull() { # pull N FILE: a PR branch on B0, pushed as refs/pull/N/head.
  git -C "${src}" checkout -q --detach "${B0}"
  echo "pr$1" >>"${src}/$2"
  git -C "${src}" commit -q -am "pr$1"
  git -C "${src}" push -q "${ORIGIN}" "HEAD:refs/pull/$1/head"
  git -C "${src}" rev-parse HEAD
}
P1=$(pull 1 a) P2=$(pull 2 b) P3=$(pull 3 c) P4=$(pull 4 d)
squash() { # squash PARENT PR_SHA N: the squash commit Tide writes for PR N.
  git -C "${src}" commit-tree "$(git -C "${src}" merge-tree --write-tree "$1" "$2")" -p "$1" -m "Change $3 (#$3)"
}
M1=$(squash "${B0}" "${P1}" 1)
M2=$(squash "${M1}" "${P2}" 2)
M3=$(squash "${M2}" "${P3}" 3)
M4=$(squash "${M3}" "${P4}" 4)
git -C "${src}" push -q "${ORIGIN}" "${M4}:refs/heads/main"

for s in "${SHARD0}" "${SHARD1}"; do
  build "${PR_LOGS}/1/${s}" 100 success presubmit "${B0}" "1:${P1}"
  build "${PR_LOGS}/2/${s}" 110 success presubmit "${B0}" "2:${P2}"
  build "${BATCH}/${s}" 200 success batch "${M1}" "2:${P2} 3:${P3} 4:${P4}"
  build "${BATCH}/${s}" 201 success batch "${M1}" "2:${P2}"
done
# A newer shard-1 retry of PR 1 that crier has not written prowjob.json for.
mkdir -p "${FX}/${PR_LOGS}/1/${SHARD1}/101"

# run NAME WANT_RC JOB_SPEC [CHECKOUT [CLONE_ARGS [CLONE_SRC]]]: runs main in a
# subshell with a fresh clone at CHECKOUT as REPO_DIR. CLONE_ARGS are extra
# git-clone arguments, split on spaces. CLONE_SRC defaults to the bare origin.
# Prints the artifacts directory.
run() {
  local name=$1 want=$2 spec=$3 at=${4:-} clone_args=${5:-} clone_src=${6:-${ORIGIN}} rc
  local out=${tmp}/out/${name// /-} repo=${tmp}/repo/${name// /-}
  # shellcheck disable=SC2086 # clone args are separate words
  git clone -q ${clone_args} "${clone_src}" "${repo}"
  [[ -z "${at}" ]] || git -C "${repo}" checkout -q --detach "${at}"
  set +o errexit
  (
    set -o errexit
    JOB_SPEC=${spec} ARTIFACTS=${out} REPO_DIR=${repo}
    main
  ) >"${tmp}/log" 2>&1
  rc=$?
  set -o errexit
  if ((rc != want)); then
    fail "${name}: exit ${rc}, want ${want}"
    sed 's/^/  /' "${tmp}/log" >&2
  fi
  echo "${out}"
}

# check NAME DIR JQ_FILTER: coverage-sources.json in DIR satisfies JQ_FILTER.
check() {
  "${JQ}" -e "$3" "$2/coverage-sources.json" >/dev/null 2>&1 \
    || fail "$1: coverage-sources.json does not match $3: $(cat "$2/coverage-sources.json" 2>&1)"
}

post() {
  printf '{"type":"postsubmit","job":"%s","refs":{"org":"kubernetes-sigs","repo":"kueue","base_ref":"main","base_sha":"%s","clone_uri":"%s"}}' \
    "${JOB_NAME}" "$1" "${ORIGIN}"
}

# presubmit PR SHA BASE
presubmit() {
  printf '{"type":"presubmit","job":"pull-kueue-test-unit-coverage-main","refs":{"org":"kubernetes-sigs","repo":"kueue","base_ref":"main","base_sha":"%s","pulls":[{"number":%s,"sha":"%s"}]}}' \
    "$3" "$1" "$2"
}

name="PR commit matches its presubmits on M^"
out=$(run "${name}" 0 "$(post "${M1}")" "${M1}")
check "${name}" "${out}" "
  .commit == \"${M1}\" and .refs.type == \"presubmit\"
  and .shards == [\"${PR_LOGS}/1/${SHARD0}/100\", \"${PR_LOGS}/1/${SHARD1}/100\"]"
[[ -s "${out}/filtered.cov" ]] || fail "${name}: no filtered.cov"

name="batch tip matches its batch"
out=$(run "${name}" 0 "$(post "${M4}")" "${M4}")
check "${name}" "${out}" "
  .commit == \"${M4}\" and .refs.type == \"batch\" and .refs.base == \"${M1}\"
  and .shards == [\"${BATCH}/${SHARD0}/200\", \"${BATCH}/${SHARD1}/200\"]"

name="earlier batch member matches an earlier batch on the same base"
out=$(run "${name}" 0 "$(post "${M2}")" "${M2}")
check "${name}" "${out}" "
  .commit == \"${M2}\" and .shards == [\"${BATCH}/${SHARD0}/201\", \"${BATCH}/${SHARD1}/201\"]"

name="batch member that no build tested is skipped"
out=$(run "${name}" 0 "$(post "${M3}")" "${M3}")
check "${name}" "${out}" ".commit == \"${M3}\" and (.skipped | test(\"no unit shard build tested the tree\"))"
[[ ! -e "${out}/filtered.cov" ]] || fail "${name}: filtered.cov written"

# Batch 203 tests M3's tree but fails on shard 1; 204 reruns shard 0.
build "${BATCH}/${SHARD0}" 203 success batch "${M1}" "2:${P2} 3:${P3}"
build "${BATCH}/${SHARD1}" 203 failure batch "${M1}" "2:${P2} 3:${P3}"
build "${BATCH}/${SHARD0}" 204 success batch "${M1}" "2:${P2} 3:${P3}"
name="batch with a failed shard is not a match"
out=$(run "${name}" 0 "$(post "${M3}")" "${M3}")
check "${name}" "${out}" ".commit == \"${M3}\" and (.skipped | test(\"no unit shard build tested the tree\"))"
[[ ! -e "${out}/filtered.cov" ]] || fail "${name}: filtered.cov written"

# PR 8 sits on an unrelated root. merge-tree exits 128, which must fail the
# postsubmit instead of looking like "no match". The batch is removed
# afterwards so later cases do not see it. Placed before batch 202, which
# already makes this commit fail.
unrelated=${tmp}/unrelated
git init -q -b main "${unrelated}"
echo u >"${unrelated}/u"
git -C "${unrelated}" add .
git -C "${unrelated}" commit -q -m unrelated
U=$(git -C "${unrelated}" rev-parse HEAD)
git -C "${unrelated}" push -q "${ORIGIN}" "HEAD:refs/pull/8/head"
for s in "${SHARD0}" "${SHARD1}"; do
  build "${BATCH}/${s}" 210 success batch "${M1}" "2:${P2} 8:${U}"
done
name="merge-tree error fails instead of skipping"
out=$(run "${name}" 1 "$(post "${M3}")" "${M3}")
grep -q "git merge-tree failed" "${tmp}/log" || fail "${name}: no 'git merge-tree failed' in the log"
rm -rf "${FX}/${BATCH}/${SHARD0}/210" "${FX}/${BATCH}/${SHARD1}/210"

# PR 6 edits a from B0, so it conflicts with M1. A conflict is still "no tree",
# and the postsubmit skips. Removed afterwards.
P6=$(pull 6 a)
for s in "${SHARD0}" "${SHARD1}"; do
  build "${BATCH}/${s}" 211 success batch "${M1}" "6:${P6}"
done
name="conflicting candidate still skips"
out=$(run "${name}" 0 "$(post "${M3}")" "${M3}")
check "${name}" "${out}" ".commit == \"${M3}\" and (.skipped | test(\"no unit shard build tested the tree\"))"
rm -rf "${FX}/${BATCH}/${SHARD0}/211" "${FX}/${BATCH}/${SHARD1}/211"

# The pull commit exists, so only the missing base decides the result.
name="missing base fails"
rc=0
(
  set -o errexit
  REPO_DIR=${src}
  REPO_URL=${ORIGIN}
  JQ=${JQ:-jq}
  tested_tree '{"base":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","pulls":[{"number":1,"sha":"'"${P1}"'"}]}'
) >"${tmp}/tt.out" 2>"${tmp}/tt.err" || rc=$?
((rc == 1)) || fail "${name}: exit ${rc}, want 1"
grep -q "cannot find base" "${tmp}/tt.err" || fail "${name}: $(cat "${tmp}/tt.err" 2>&1)"

# Depth 1 has no parent, so without the shallow guard this skips as a merge.
name="shallow clone fails"
out=$(run "${name}" 1 "$(post "${M4}")" "" "--depth 1 --no-checkout" "file://${ORIGIN}")
grep -q "shallow" "${tmp}/log" || fail "${name}: no 'shallow' in the log"

# A batch whose PR 9 head is not on the remote may be M3's match.
for s in "${SHARD0}" "${SHARD1}"; do
  build "${BATCH}/${s}" 202 success batch "${M1}" "2:${P2} 9:0123456789abcdef0123456789abcdef01234567"
done
name="unfetchable candidate fails instead of skipping"
out=$(run "${name}" 1 "$(post "${M3}")" "${M3}")
[[ ! -e "${out}/coverage-sources.json" ]] || fail "${name}: coverage-sources.json written"
grep -q "could not be fetched" "${tmp}/log" || fail "${name}: no 'could not be fetched' in the log"

# The first poll sees the shard-1 retry without prowjob.json and waits; the
# hook then lets crier write it, and the retry is merged.
crier() { build "${PR_LOGS}/1/${SHARD1}" 101 success presubmit "${B0}" "1:${P1}"; }
SLEEP_HOOK=crier
APPEAR_SECONDS=600
name="presubmit waits for a build without prowjob.json"
out=$(run "${name}" 0 "{\"type\":\"presubmit\",\"job\":\"pull-kueue-test-unit-coverage-main\",\"refs\":{\"org\":\"kubernetes-sigs\",\"repo\":\"kueue\",\"base_ref\":\"main\",\"base_sha\":\"${B0}\",\"pulls\":[{\"number\":1,\"sha\":\"${P1}\"}]}}")
check "${name}" "${out}" "
  .commit == \"${P1}\" and .shards == [\"${PR_LOGS}/1/${SHARD0}/100\", \"${PR_LOGS}/1/${SHARD1}/101\"]"

# Each presubmit below uses its own PR, so PR 1's builds stay at
# UNIT_TOTAL_SHARDS=2. Each sets its own wait bounds.
name="presubmit with no shard build on its refs is skipped"
out=$(JOB_NAME=pull-kueue-test-unit-coverage-main APPEAR_SECONDS=0 SLEEP_HOOK='' \
  run "${name}" 0 "$(presubmit 15 "${P1}" "${B0}")")
check "${name}" "${out}" '(.skipped | test("no pull-kueue-test-unit-shard-0-main build on these refs")) and (.skipped | test("/test pull-kueue-test-unit-coverage-main"))'
[[ ! -e "${out}/filtered.cov" ]] || fail "${name}: filtered.cov written"

name="presubmit fails when one shard failed on its refs"
APPEAR_SECONDS=0
SLEEP_HOOK=""
build "${PR_LOGS}/16/${SHARD1}" 160 failure presubmit "${B0}" "16:${P1}"
out=$(JOB_NAME=pull-kueue-test-unit-coverage-main APPEAR_SECONDS=0 SLEEP_HOOK='' \
  run "${name}" 1 "$(presubmit 16 "${P1}" "${B0}")")

name="presubmit fails when a shard build omits the shard count"
build "${PR_LOGS}/17/${SHARD0}" 170 success presubmit "${B0}" "17:${P1}"
build "${PR_LOGS}/17/${SHARD1}" 171 success presubmit "${B0}" "17:${P1}" -
out=$(JOB_NAME=pull-kueue-test-unit-coverage-main APPEAR_SECONDS=0 SLEEP_HOOK='' \
  run "${name}" 1 "$(presubmit 17 "${P1}" "${B0}")")
grep -q "unset" "${tmp}/log" || fail "${name}: no 'unset' in the log"

name="presubmit fails a high shard count instead of skipping"
build "${PR_LOGS}/18/${SHARD1}" 180 success presubmit "${B0}" "18:${P1}" 3
out=$(JOB_NAME=pull-kueue-test-unit-coverage-main APPEAR_SECONDS=0 SLEEP_HOOK='' \
  run "${name}" 1 "$(presubmit 18 "${P1}" "${B0}")")
grep -q "UNIT_COVERAGE_SHARDS" "${tmp}/log" || fail "${name}: no UNIT_COVERAGE_SHARDS in the log"

name="presubmit skips when one shard never ran"
build "${PR_LOGS}/19/${SHARD0}" 190 success presubmit "${B0}" "19:${P1}"
out=$(JOB_NAME=pull-kueue-test-unit-coverage-main APPEAR_SECONDS=0 SLEEP_HOOK='' \
  run "${name}" 0 "$(presubmit 19 "${P1}" "${B0}")")
check "${name}" "${out}" '.skipped | type == "string"'
[[ ! -e "${out}/filtered.cov" ]] || fail "${name}: filtered.cov written"

# PR 10 adds a new file, so it merges cleanly onto M4. The matching batch ran
# with UNIT_TOTAL_SHARDS=3. Removed afterwards.
git -C "${src}" checkout -q --detach "${B0}"
echo pr10 >"${src}/e"
git -C "${src}" add e
git -C "${src}" commit -q -m pr10
git -C "${src}" push -q "${ORIGIN}" "HEAD:refs/pull/10/head"
P10=$(git -C "${src}" rev-parse HEAD)
M5=$(squash "${M4}" "${P10}" 10)
git -C "${src}" push -q "${ORIGIN}" "${M5}:refs/heads/main"
for s in "${SHARD0}" "${SHARD1}"; do
  build "${BATCH}/${s}" 300 success batch "${M4}" "10:${P10}" 3
done
name="batch shard count above the config fails"
out=$(run "${name}" 1 "$(post "${M5}")" "${M5}")
grep -q "UNIT_COVERAGE_SHARDS" "${tmp}/log" || fail "${name}: no UNIT_COVERAGE_SHARDS in the log"
rm -rf "${FX}/${BATCH}/${SHARD0}/300" "${FX}/${BATCH}/${SHARD1}/300"

name="postsubmit old shard count fails red"
out=$(UNIT_COVERAGE_SHARDS=3 run "${name}" 1 "$(post "${M1}")" "${M1}")
grep -q "UNIT_COVERAGE_SHARDS" "${tmp}/log" || fail "${name}: no UNIT_COVERAGE_SHARDS in the log"
[[ ! -e "${out}/coverage-sources.json" ]] || fail "${name}: coverage-sources.json written"
[[ ! -e "${out}/filtered.cov" ]] || fail "${name}: filtered.cov written"

# No PR-path success. The batch shards succeeded with UNIT_TOTAL_SHARDS=2,
# the config is 3, and two shards are not a complete set.
git -C "${src}" checkout -q --detach "${B0}"
echo pr12 >"${src}/f"
git -C "${src}" add f
git -C "${src}" commit -q -m pr12
git -C "${src}" push -q "${ORIGIN}" "HEAD:refs/pull/12/head"
P12=$(git -C "${src}" rev-parse HEAD)
M6=$(squash "${M5}" "${P12}" 12)
git -C "${src}" push -q "${ORIGIN}" "${M6}:refs/heads/main"
for s in "${SHARD0}" "${SHARD1}"; do
  build "${BATCH}/${s}" 301 success batch "${M5}" "12:${P12}"
done
name="postsubmit batch-only old shard count fails red"
out=$(UNIT_COVERAGE_SHARDS=3 run "${name}" 1 "$(post "${M6}")" "${M6}")
grep -q "UNIT_COVERAGE_SHARDS" "${tmp}/log" || fail "${name}: no UNIT_COVERAGE_SHARDS in the log"
rm -rf "${FX}/${BATCH}/${SHARD0}/301" "${FX}/${BATCH}/${SHARD1}/301"

if [[ -s "${failures}" ]]; then
  echo "$(($(wc -l <"${failures}"))) failure(s)" >&2
  exit 1
fi
echo "All tests passed."
