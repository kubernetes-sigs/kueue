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

# Self-test for fetch-prow-unit-coverage.sh: codecov_has_upload with a stub
# curl that replays canned Codecov API responses, and the build discovery of
# both modes with stub GitHub and GCS data.
# Usage: fetch-prow-unit-coverage_test.sh

set -o errexit
set -o nounset
set -o pipefail

tmp=$(mktemp -d)
trap 'rm -rf "${tmp}"' EXIT
# shellcheck source=hack/testing/fetch-prow-unit-coverage.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/fetch-prow-unit-coverage.sh"
# Keep the real batch listing helper; build discovery below replaces it.
eval "real_$(declare -f gcs_batch_builds)"

failures=0
fail() { echo "FAIL: $*" >&2; failures=$((failures + 1)); }

SHA=0123456789abcdef0123456789abcdef01234567
CODECOV_POLL_TRIES=3
CODECOV_POLL_SECS=0
PULLS_RETRY_SECS=0

# The stub replays ${tmp}/responses, one "HTTP_CODE BODY" line per call, and
# repeats the last line once the others are used up.
curl() {
  local n line
  n=$(( $(cat "${tmp}/calls") + 1 ))
  echo "${n}" > "${tmp}/calls"
  line=$(sed -n "${n}p" "${tmp}/responses")
  [[ -n "${line}" ]] || line=$(tail -n 1 "${tmp}/responses")
  [[ "${line}" != "fail" ]] || return 7
  printf '%s\n%s' "${line#* }" "${line%% *}"
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

# Build discovery. Stub GitHub API paths and GCS objects live under
# ${tmp}/gh and ${tmp}/gcs; statuses are kept as JSON lines. A FILE.lag
# holding N makes the next N reads of FILE return [] (GitHub indexing lag),
# and an OBJECT.fail makes the download or read of OBJECT fail (a missing
# OBJECT is a 404). The default branch has codecov.yml unless
# ${tmp}/main-no-yml exists. Every commit has only codecov.yml with the blob
# ${YML_MAIN}, except those listed in ${tmp}/cfg as "SHA PATH BLOB" lines
# (PATH "-" for no Codecov YAML); git/trees serves them with a .github/ tree.
# compare/BASE...HEAD returns BASE as the merge base, unless ${tmp}/mergebase
# has a "HEAD MERGE_BASE" line.
mkdir -p "${tmp}/gh" "${tmp}/gcs"
: > "${tmp}/cfg"
: > "${tmp}/mergebase"
: > "${tmp}/subtrees"
YML_MAIN=$(printf '%040x' 0xc0dec0de)
key() { local k=${1%%\?*}; echo "${tmp}/gh/${k//\//_}"; }
# cfg_paths REF prints "PATH BLOB" for each Codecov YAML path at REF.
cfg_paths() {
  if grep -q "^$1 " "${tmp}/cfg"; then
    awk -v r="$1" '$1 == r && $2 != "-" { print $2, $3 }' "${tmp}/cfg"
  else
    echo "codecov.yml ${YML_MAIN}"
  fi
}
# tree_json REF [DIR] prints the Git tree of REF's root, or of its DIR.
tree_json() {
  local ref=$1 dir=${2:-} d sub p b
  local entries='{"path":"go.mod","mode":"100644","type":"blob","sha":"0000000000000000000000000000000000000f00"}'
  if [[ -z "${dir}" ]]; then
    for d in .github dev; do
      [[ "${d}" == ".github" ]] || cfg_paths "${ref}" | grep -q "^dev/" || continue
      sub=$(printf '%s %s' "${ref}" "${d}" | shasum | cut -c1-40)
      echo "${sub} ${ref} ${d}" >> "${tmp}/subtrees"
      entries+=',{"path":"'${d}'","mode":"040000","type":"tree","sha":"'${sub}'"}'
    done
  fi
  while read -r p b; do
    if [[ ( -z "${dir}" && "${p}" != */* ) || ( -n "${dir}" && "${p}" == "${dir}"/* ) ]]; then
      entries+=',{"path":"'${p#"${dir}"/}'","mode":"100644","type":"blob","sha":"'${b}'"}'
    fi
  done < <(cfg_paths "${ref}")
  echo '{"sha":"'"${ref}"'","tree":['"${entries}"'],"truncated":false}'
}
gh() {
  local path file lag sub
  for path in "$@"; do :; done
  if [[ "${path}" == */contents/codecov.yml ]]; then
    [[ ! -e "${tmp}/main-no-yml" ]] || { echo "gh: Not Found (HTTP 404)" >&2; return 1; }
    echo "{\"type\":\"file\",\"sha\":\"${YML_MAIN}\"}"
    return
  fi
  if [[ "${path}" == */compare/* ]]; then
    sub=${path#*/compare/}
    sub=${sub%%\?*}
    lag=$(awk -v h="${sub#*...}" '$1 == h { print $2; exit }' "${tmp}/mergebase")
    echo "{\"merge_base_commit\":{\"sha\":\"${lag:-${sub%%...*}}\"}}"
    return
  fi
  if [[ "${path}" == */git/trees/* ]]; then
    sub=$(awk -v x="${path##*/}" '$1 == x { print $2, $3; exit }' "${tmp}/subtrees")
    # shellcheck disable=SC2086 # "REF DIR"
    tree_json ${sub:-${path##*/}}
    return
  fi
  file=$(key "${path}")
  if [[ "${path}" == */statuses* ]]; then jq -s . "${file}" 2>/dev/null || echo "[]"; return; fi
  if [[ -f "${file}.lag" ]] && lag=$(cat "${file}.lag") && ((lag > 0)); then
    echo "$((lag - 1))" > "${file}.lag"
    echo "[]"
    return
  fi
  [[ -f "${file}" ]] || { echo "gh: Not Found (HTTP 404)" >&2; return 1; }
  cat "${file}"
}
gcs_get() {
  [[ ! -e "${tmp}/gcs/$1.fail" ]] || return 1
  [[ -f "${tmp}/gcs/$1" ]] || return 4
  cat "${tmp}/gcs/$1"
}
gcs_exists() { [[ -f "${tmp}/gcs/$1" ]]; }
gcs_download() { [[ ! -e "${tmp}/gcs/$1.fail" ]] && cp "${tmp}/gcs/$1" "$2" 2>/dev/null; }
gcs_batch_builds() { find "${tmp}/gcs/pr-logs/pull/batch/$1" -mindepth 1 -maxdepth 1 -exec basename {} \; | sort -rn; }
codecov_has_upload() { return 1; }

R=repos/kubernetes-sigs/kueue
s() { printf '%040x' "$1"; }
# commit SHA PARENT [PR HEAD]: a squash commit on main titled "Change (#PR)"
# (no PR: a direct push without the suffix).
commit() {
  printf '{"sha":"%s","parents":[{"sha":"%s"}],"commit":{"message":"Change%s\\n\\nBody (#1)"}}\n' \
    "$1" "$2" "${3:+ (#$3)}" > "$(key "${R}/commits/$1")"
  if [[ -n "${3:-}" ]]; then
    echo "[{\"number\":$3,\"merge_commit_sha\":\"$1\",\"base\":{\"ref\":\"main\"},\"head\":{\"sha\":\"$4\"}}]"
  else
    echo "[]"
  fi > "$(key "${R}/commits/$1/pulls")"
}
# unlisted SHA PR HEAD [BASE_REF [MERGE_SHA]]: commits/SHA/pulls stays empty;
# pulls/PR says PR was merged into BASE_REF (main) as MERGE_SHA (SHA).
unlisted() {
  echo "[]" > "$(key "${R}/commits/$1/pulls")"
  echo "{\"number\":$2,\"merge_commit_sha\":\"${5:-$1}\",\"base\":{\"ref\":\"${4:-main}\"},\"head\":{\"sha\":\"$3\"}}" > "$(key "${R}/pulls/$2")"
}
# build PATH TYPE JOB BASE RESULT REVISION PULLS: a finished Prow build with
# a filtered.cov.
build() {
  mkdir -p "${tmp}/gcs/$1/artifacts"
  printf 'mode: atomic\nsigs.k8s.io/kueue/pkg/a.go:1.1,2.2 1 1\n' > "${tmp}/gcs/$1/artifacts/filtered.cov"
  echo "{\"spec\":{\"type\":\"$2\",\"job\":\"$3\",\"refs\":{\"org\":\"kubernetes-sigs\",\"repo\":\"kueue\",\"base_ref\":\"main\",\"base_sha\":\"$4\",\"pulls\":$7}}}" > "${tmp}/gcs/$1/prowjob.json"
  echo "{\"result\":\"$5\",\"revision\":\"$6\"}" > "${tmp}/gcs/$1/finished.json"
  echo "{\"repo-commit\":\"$(s "$((0x${4:32} + 0x7000))")\"}" > "${tmp}/gcs/$1/started.json"
}
# presubmit SHARD ID PR HEAD BASE [STATE]: a shard build of PR at HEAD on
# BASE, and its status on HEAD with crier's BaseSHA suffix.
presubmit() {
  local job="pull-kueue-test-unit-shard-$1-main" path="pr-logs/pull/kubernetes-sigs_kueue/$3/pull-kueue-test-unit-shard-$1-main/$2"
  build "${path}" presubmit "${job}" "$5" SUCCESS "$4" "[{\"number\":$3,\"sha\":\"$4\"}]"
  echo "{\"id\":$2,\"context\":\"${job}\",\"state\":\"${6:-success}\",\"created_at\":\"2026-10-31T00:00:$(printf %02d $(($2 % 60)))Z\",\"description\":\"Job succeeded. BaseSHA:$5\",\"target_url\":\"https://prow.k8s.io/view/gs/kubernetes-ci-logs/${path}\"}" >> "$(key "${R}/commits/$4/statuses")"
}
# batch SHARD ID BASE RESULT PR:HEAD...: a Tide batch build (no statuses).
batch() {
  local shard=$1 id=$2 base=$3 result=$4 pulls="" ph; shift 4
  for ph in "$@"; do pulls+="${pulls:+,}{\"number\":${ph%%:*},\"sha\":\"${ph#*:}\"}"; done
  build "pr-logs/pull/batch/pull-kueue-test-unit-shard-${shard}-main/${id}" batch "pull-kueue-test-unit-shard-${shard}-main" "${base}" "${result}" "${base}" "[${pulls}]"
}

# Serial: PR 201 (head 201) merged as 2 onto 1, tested on base 1, and earlier
# on base 9 (the newer pair must not be mixed with it).
commit "$(s 2)" "$(s 1)" 201 "$(s 201)"
presubmit 0 10 201 "$(s 201)" "$(s 9)"
presubmit 1 11 201 "$(s 201)" "$(s 9)"
presubmit 0 20 201 "$(s 201)" "$(s 1)"
presubmit 1 21 201 "$(s 201)" "$(s 1)"
# Mismatch: PR 202 merged as 3 onto 2, but only tested on base 1.
commit "$(s 3)" "$(s 2)" 202 "$(s 202)"
presubmit 0 30 202 "$(s 202)" "$(s 1)"
presubmit 1 31 202 "$(s 202)" "$(s 1)"
# Batch: PRs 301-303 merged as 5, 6, 7 onto 4. A failed batch build and a
# batch on another base must not match.
commit "$(s 4)" "$(s 3)" 300 "$(s 300)"
commit "$(s 5)" "$(s 4)" 301 "$(s 301)"
commit "$(s 6)" "$(s 5)" 302 "$(s 302)"
commit "$(s 7)" "$(s 6)" 303 "$(s 303)"
batch 0 50 "$(s 4)" FAILURE "301:$(s 301)" "302:$(s 302)" "303:$(s 303)"
batch 1 51 "$(s 4)" SUCCESS "301:$(s 301)" "302:$(s 302)" "303:$(s 303)"
batch 0 60 "$(s 4)" SUCCESS "303:$(s 303)" "301:$(s 301)" "302:$(s 302)"
batch 1 61 "$(s 4)" SUCCESS "301:$(s 301)" "302:$(s 302)" "303:$(s 303)"
batch 0 70 "$(s 3)" SUCCESS "302:$(s 302)" "303:$(s 303)"
# Skip: PR 401 (docs only, no unit statuses) merged as 8 onto 7, and a
# direct push 9 onto 8.
commit "$(s 8)" "$(s 7)" 401 "$(s 401)"
commit "$(s 9)" "$(s 8)"
# Serial status whose build tested another base.
commit "$(s 11)" "$(s 10)" 501 "$(s 501)"
presubmit 0 80 501 "$(s 501)" "$(s 10)"
presubmit 1 81 501 "$(s 501)" "$(s 10)"
sed -i.bak 's/"base_sha":"[0-9a-f]*"/"base_sha":"'"$(s 12)"'"/' "${tmp}/gcs/pr-logs/pull/kubernetes-sigs_kueue/501/pull-kueue-test-unit-shard-1-main/81/prowjob.json"
# PR mode: PR 601 head shard 1 retested on a newer base only; the older
# pair on one base is used.
presubmit 0 90 601 "$(s 601)" "$(s 1)"
presubmit 1 91 601 "$(s 601)" "$(s 1)"
presubmit 1 92 601 "$(s 601)" "$(s 2)"
# PR mode: PR 602 head with shard 1 pending on base 2 and an older shard 1
# success only on base 1.
presubmit 1 88 602 "$(s 602)" "$(s 1)"
presubmit 0 93 602 "$(s 602)" "$(s 2)"
presubmit 1 94 602 "$(s 602)" "$(s 2)" pending
# PR mode: PR 603 passed on base 1, then a shard 1 retest on base 2 failed.
presubmit 0 95 603 "$(s 603)" "$(s 1)"
presubmit 1 96 603 "$(s 603)" "$(s 1)"
presubmit 1 97 603 "$(s 603)" "$(s 2)" failure
# PR mode: PR 604 shards passed only on different bases.
presubmit 0 98 604 "$(s 604)" "$(s 1)"
presubmit 1 99 604 "$(s 604)" "$(s 2)"
# GitHub lists PR 701 (merged as 21 onto 20) only on the third read.
commit "$(s 21)" "$(s 20)" 701 "$(s 701)"
echo 2 > "$(key "${R}/commits/$(s 21)/pulls").lag"
presubmit 0 100 701 "$(s 701)" "$(s 20)"
presubmit 1 101 701 "$(s 701)" "$(s 20)"
# GitHub never lists PR 702 (merged as 23 onto 22); its title maps it.
commit "$(s 23)" "$(s 22)" 702 "$(s 702)"
unlisted "$(s 23)" 702 "$(s 702)"
presubmit 0 102 702 "$(s 702)" "$(s 22)"
presubmit 1 103 702 "$(s 702)" "$(s 22)"
# Titles that point at PRs merged as another commit, or into another branch.
commit "$(s 25)" "$(s 24)" 703 "$(s 703)"
unlisted "$(s 25)" 703 "$(s 703)" main "$(s 26)"
commit "$(s 27)" "$(s 26)" 704 "$(s 704)"
unlisted "$(s 27)" 704 "$(s 704)" release-0.15
# One shard's profile fails to download (PR 801), or is empty (PR 802).
commit "$(s 41)" "$(s 40)" 801 "$(s 801)"
presubmit 0 110 801 "$(s 801)" "$(s 40)"
presubmit 1 111 801 "$(s 801)" "$(s 40)"
touch "${tmp}/gcs/pr-logs/pull/kubernetes-sigs_kueue/801/pull-kueue-test-unit-shard-1-main/111/artifacts/filtered.cov.fail"
commit "$(s 43)" "$(s 42)" 802 "$(s 802)"
presubmit 0 112 802 "$(s 802)" "$(s 42)"
presubmit 1 113 802 "$(s 802)" "$(s 42)"
: > "${tmp}/gcs/pr-logs/pull/kubernetes-sigs_kueue/802/pull-kueue-test-unit-shard-1-main/113/artifacts/filtered.cov"
echo '{"head":{"ref":"topic","label":"someone:topic","repo":{"full_name":"someone/kueue"}}}' > "$(key "${R}/pulls/201")"
for pr in 601 603; do
  echo '{"head":{"ref":"topic","label":"kubernetes-sigs:topic","repo":{"full_name":"kubernetes-sigs/kueue"}}}' > "$(key "${R}/pulls/${pr}")"
done
# PR mode: fork branch labels that would split at commas in the
# codecov-action wrapper (PR 606), or start with a dash (PR 607).
for pr in 606 607; do
  presubmit 0 "$((pr - 486))" "${pr}" "$(s "${pr}")" "$(s 1)"
  presubmit 1 "$((pr - 485))" "${pr}" "$(s "${pr}")" "$(s 1)"
done
echo '{"head":{"ref":"x","label":"attacker:test,--sha=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa,--pr=,--branch=main","repo":{"full_name":"attacker/kueue"}}}' > "$(key "${R}/pulls/606")"
echo '{"head":{"ref":"-n","label":"-n","repo":{"full_name":"kubernetes-sigs/kueue"}}}' > "$(key "${R}/pulls/607")"
# Codecov YAML at a PR head on base 1: codecov.yml edited (608) or removed
# (609), .codecov.yml added (611), .github/codecov.yaml added (615).
for pr in 608 609 611 615; do
  presubmit 0 "$((pr - 486))" "${pr}" "$(s "${pr}")" "$(s 1)"
  presubmit 1 "$((pr - 485))" "${pr}" "$(s "${pr}")" "$(s 1)"
  echo '{"head":{"ref":"topic","label":"kubernetes-sigs:topic","repo":{"full_name":"kubernetes-sigs/kueue"}}}' > "$(key "${R}/pulls/${pr}")"
done
BLOB_BAD=$(printf '%040x' 0xbad)
printf '%s %s %s\n' "$(s 608)" codecov.yml "${BLOB_BAD}" "$(s 609)" - - "$(s 611)" codecov.yml "${YML_MAIN}" "$(s 611)" .codecov.yml "${BLOB_BAD}" \
  "$(s 615)" codecov.yml "${YML_MAIN}" "$(s 615)" .github/codecov.yaml "${BLOB_BAD}" >> "${tmp}/cfg"
# PR 618 branched before the rollout: neither its head nor its merge base 51
# has Codecov YAML, but the tested base 1 has. PR 619 branched at 52, whose
# codecov.yml (an older blob than base 1's) it keeps unchanged.
YML_OLD=$(printf '%040x' 0x01dc0de)
for pr in 618 619; do
  presubmit 0 "$((2 * pr - 1096))" "${pr}" "$(s "${pr}")" "$(s 1)"
  presubmit 1 "$((2 * pr - 1095))" "${pr}" "$(s "${pr}")" "$(s 1)"
  echo '{"head":{"ref":"topic","label":"kubernetes-sigs:topic","repo":{"full_name":"kubernetes-sigs/kueue"}}}' > "$(key "${R}/pulls/${pr}")"
done
printf '%s %s\n' "$(s 618)" "$(s 51)" "$(s 619)" "$(s 52)" >> "${tmp}/mergebase"
printf '%s %s %s\n' "$(s 618)" - - "$(s 51)" - - "$(s 619)" codecov.yml "${YML_OLD}" "$(s 52)" codecov.yml "${YML_OLD}" >> "${tmp}/cfg"
# PR 614 and its base 50 both predate codecov.yml.
presubmit 0 135 614 "$(s 614)" "$(s 50)"
presubmit 1 136 614 "$(s 614)" "$(s 50)"
printf '%s - -\n' "$(s 614)" "$(s 50)" >> "${tmp}/cfg"
# Main commits with Codecov YAML outside codecov.yml (47), or none (48).
printf '%s %s %s\n' "$(s 47)" codecov.yml "${YML_MAIN}" "$(s 47)" dev/.codecov.yml "${BLOB_BAD}" "$(s 48)" - - >> "${tmp}/cfg"
# PRs 612 and 613 share the head 612: PR 612 passed both shards, then PR 613
# shard 1 passed on the same base. Head 616 has only a pair mixed of PRs 616
# and 617.
presubmit 0 130 612 "$(s 612)" "$(s 1)"
presubmit 1 131 612 "$(s 612)" "$(s 1)"
presubmit 1 132 613 "$(s 612)" "$(s 1)"
presubmit 0 133 616 "$(s 616)" "$(s 1)"
presubmit 1 134 617 "$(s 616)" "$(s 1)"
for pr in 612 614; do
  echo '{"head":{"ref":"topic","label":"kubernetes-sigs:topic","repo":{"full_name":"kubernetes-sigs/kueue"}}}' > "$(key "${R}/pulls/${pr}")"
done
# PR 612 (head 612, shared with PR 613) merged as 63 onto 1.
commit "$(s 63)" "$(s 1)" 612 "$(s 612)"
# Batch: PRs 901 and 902 merged as 61 and 62 onto 60. The newest shard 0
# build 220 tested another merge commit than shard 1's only build 211.
commit "$(s 61)" "$(s 60)" 901 "$(s 901)"
commit "$(s 62)" "$(s 61)" 902 "$(s 902)"
batch 0 210 "$(s 60)" SUCCESS "901:$(s 901)" "902:$(s 902)"
batch 1 211 "$(s 60)" SUCCESS "902:$(s 902)" "901:$(s 901)"
batch 0 220 "$(s 60)" SUCCESS "901:$(s 901)" "902:$(s 902)"
echo "{\"repo-commit\":\"$(s 0xdead)\"}" > "${tmp}/gcs/pr-logs/pull/batch/pull-kueue-test-unit-shard-0-main/220/started.json"
# Crier's description without the BaseSHA suffix: PR 610 head, and the
# serial presubmits of PR 806 (merged as 45 onto 44).
presubmit 0 126 610 "$(s 610)" "$(s 1)"
presubmit 1 127 610 "$(s 610)" "$(s 1)"
sed -i.bak '/"id":127,/s/ BaseSHA:[0-9a-f]*//' "$(key "${R}/commits/$(s 610)/statuses")"
commit "$(s 45)" "$(s 44)" 806 "$(s 806)"
presubmit 0 128 806 "$(s 806)" "$(s 44)"
presubmit 1 129 806 "$(s 806)" "$(s 44)"
sed -i.bak '/"id":128,/s/ BaseSHA:[0-9a-f]*/ (new format)/' "$(key "${R}/commits/$(s 806)/statuses")"

# discover NAME MODE SHA WANT: WANT is the build_id, "skip", or "error". An
# upload must come with a non-empty profile per shard; anything else with none.
discover() {
  local name=$1 got profiles
  rm -rf "${tmp}/out"
  if ! got=$( (main "$2" "$3" "${tmp}/out") 2> "${tmp}/err"); then
    got=error
  elif grep -qx "skip=true" <<<"${got}"; then
    got=skip
  else
    got=$(sed -n 's/^build_id=//p' <<<"${got}")
    [[ -s "${tmp}/out/cover-shard-0.out" && -s "${tmp}/out/cover-shard-1.out" ]] || got="${got} without profiles"
  fi
  profiles=$(find "${tmp}/out" -name 'cover-shard-*.out' 2>/dev/null | wc -l | tr -d ' ')
  [[ "${got}" =~ ^[0-9] || "${profiles}" -eq 0 ]] || got="${got} with ${profiles} profile(s)"
  if [[ "${got}" == "$4" ]]; then
    echo "ok: ${name} (${got}: $(tail -n 1 "${tmp}/err"))"
  else
    fail "${name}: got ${got}, want $4: $(cat "${tmp}/err")"
  fi
}

discover "main: single PR, BaseSHA matches the parent" main "$(s 2)" 20-21
discover "main: single PR, BaseSHA of an older base" main "$(s 3)" skip
discover "main: batch, last commit of the run" main "$(s 7)" 60-61
discover "main: batch member before the last" main "$(s 6)" skip
discover "main: pull request without unit statuses" main "$(s 8)" skip
discover "main: commit without a pull request" main "$(s 9)" error
discover "main: PR listed after two empty reads" main "$(s 21)" 100-101
discover "main: PR never listed, mapped by its title" main "$(s 23)" 102-103
discover "main: title PR merged as another commit" main "$(s 25)" error
discover "main: title PR merged into another branch" main "$(s 27)" error
discover "main: one shard's download fails" main "$(s 41)" error
discover "main: one shard's profile is empty" main "$(s 43)" error
discover "main: status points at a build on another base" main "$(s 11)" error
discover "pr: shards on one base" pr "$(s 201)" 20-21
discover "pr: newer retest of one shard on another base" pr "$(s 601)" 90-91
discover "pr: newer failed retest of one shard" pr "$(s 603)" 95-96
discover "pr: shard pending, no pair on one base" pr "$(s 602)" skip
discover "pr: shards only on different bases" pr "$(s 604)" skip
discover "pr: edits codecov.yml" pr "$(s 608)" skip
discover "pr: removes codecov.yml" pr "$(s 609)" 123-124
discover "pr: branched before rollout (head has no config, base has)" pr "$(s 618)" 140-141
discover "pr: head has an older main codecov.yml equal to its merge base" pr "$(s 619)" 142-143
discover "pr: adds .codecov.yml" pr "$(s 611)" skip
discover "pr: adds .github/codecov.yaml" pr "$(s 615)" skip
discover "pr: base and head without codecov.yml" pr "$(s 614)" 135-136
discover "pr: two PRs share the head" pr "$(s 612)" 130-131
discover "main: two PRs share the merged PR's head" main "$(s 63)" 130-131
discover "pr: only a pair mixed of two PRs" pr "$(s 616)" skip
discover "main: Codecov YAML outside codecov.yml" main "$(s 47)" skip
discover "main: no codecov.yml" main "$(s 48)" skip
touch "${tmp}/main-no-yml"
discover "pr: default branch without codecov.yml" pr "$(s 201)" skip
discover "main: default branch without codecov.yml" main "$(s 2)" skip
rm "${tmp}/main-no-yml"
discover "main: batch, older pair of shards on one tree" main "$(s 62)" 210-211
b220="pr-logs/pull/batch/pull-kueue-test-unit-shard-0-main/220"
mv "${tmp}/gcs/${b220}/started.json" "${tmp}/started.json"
discover "main: batch started.json missing skips that build" main "$(s 62)" 210-211
mv "${tmp}/started.json" "${tmp}/gcs/${b220}/started.json"
touch "${tmp}/gcs/${b220}/started.json.fail"
discover "main: batch started.json unreadable" main "$(s 62)" error
rm "${tmp}/gcs/${b220}/started.json.fail"
touch "${tmp}/gcs/pr-logs/pull/batch/pull-kueue-test-unit-shard-0-main/60/prowjob.json.fail"
discover "main: matching batch prowjob.json unreadable" main "$(s 7)" error
rm "${tmp}/gcs/pr-logs/pull/batch/pull-kueue-test-unit-shard-0-main/60/prowjob.json.fail"
touch "${tmp}/gcs/pr-logs/pull/batch/pull-kueue-test-unit-shard-1-main/61/finished.json.fail"
discover "main: matching batch finished.json unreadable" main "$(s 7)" error
rm "${tmp}/gcs/pr-logs/pull/batch/pull-kueue-test-unit-shard-1-main/61/finished.json.fail"
discover "pr: status without BaseSHA" pr "$(s 610)" error
discover "main: status without BaseSHA" main "$(s 45)" error

# The real batch listing helper fails on a page it cannot parse, rather than
# listing no builds; a valid page lists the build IDs newest first.
listing() {
  local name=$1 want=$2 got rc=0; shift 2
  printf '%s\n' "$@" > "${tmp}/listing"
  # shellcheck disable=SC2329 # replaces curl for real_gcs_batch_builds
  got=$( (curl() { cat "${tmp}/listing"; }; real_gcs_batch_builds pull-kueue-test-unit-shard-0-main) 2> "${tmp}/err") || rc=$?
  ((rc == 0)) || got=error
  if [[ "${got//$'\n'/ }" == "${want}" ]]; then
    echo "ok: listing: ${name} (${got//$'\n'/ })"
  else
    fail "listing: ${name}: got '${got//$'\n'/ }', want '${want}': $(cat "${tmp}/err")"
  fi
}
p="pr-logs/pull/batch/pull-kueue-test-unit-shard-0-main"
listing "valid page" "12 11" '{"prefixes":["'"${p}"'/11/","'"${p}"'/12/","'"${p}"'/latest-build.txt/"]}'
listing "truncated JSON" error '{"prefixes":["'"${p}"'/11/","'"${p}"'/1'
listing "not an object" error '"oops"'

# output NAME MODE SHA KEY WANT checks one key=value line of an upload.
output() {
  local got
  got=$( (main "$2" "$3" "${tmp}/out") 2> "${tmp}/err" | sed -n "s/^$4=//p") || true
  if [[ "${got}" == "$5" ]]; then
    echo "ok: ${1} ($4=${got}: $(tail -n 1 "${tmp}/err"))"
  else
    fail "${1}: got $4='${got}', want '$5': $(cat "${tmp}/err")"
  fi
}

output "branch: fork label" pr "$(s 201)" branch someone:topic
output "branch: comma in the fork label" pr "$(s 606)" branch pr-606
output "branch: leading dash" pr "$(s 607)" branch pr-607
output "branch: main commit" main "$(s 2)" branch main
output "Codecov config unchanged" pr "$(s 201)" skip false
output "build_url: pull request" pr "$(s 201)" build_url "https://prow.k8s.io/pr-history/?org=kubernetes-sigs&repo=kueue&pr=201"

# check_outputs rejects anything the wrapper would split or expand.
reject() {
  if (check_outputs "$@") 2> /dev/null; then
    fail "check_outputs accepted: $*"
  else
    echo "ok: check_outputs rejects: $*"
  fi
}
reject "${SHA}" 1 'a,--sha=b' 1 https://prow.k8s.io/x
reject "${SHA}" 1 main 1,2 https://prow.k8s.io/x
reject "${SHA}" 1 main 1 'https://prow.k8s.io/x,--pr=2'
reject "${SHA}" 1 main 1 'https://example.com/x'
reject "${SHA}" 1x main 1 https://prow.k8s.io/x
reject "${SHA}" 1 'a b' 1 https://prow.k8s.io/x
reject "${SHA}" 1 'a*' 1 https://prow.k8s.io/x
reject "${SHA}" 1 "$(printf 'a%.0s' {1..201})" 1 https://prow.k8s.io/x

if [[ "${failures}" -gt 0 ]]; then
  echo "${failures} test(s) failed." >&2
  exit 1
fi
echo "All tests passed."
