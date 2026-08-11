#!/usr/bin/env bash

# Copyright The Kubernetes Authors.
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

# collect_stats.sh — one command for the whole pipeline:
#   1. fetch prow metrics for the selected jobs   (fetch_prow_metrics.py)
#   2. render the PNG image set for each job       (plot.py)
#   3. aggregate a recommendation.md table         (aggregate_reco.py)
#
# It cd's to the kueue repo root on its own, so you can run it from anywhere and the
# default output still lands under the checkout. It applies these defaults, each used
# ONLY if you did not pass that flag yourself, so any explicit value overrides it:
#
#   --job-regex '^pull-'   --days 14   --step 30s
#   --out-dir artifacts/infra-stats   --concurrency 2   --sleep 2   --retries 2
#
# A relative --out-dir you pass is therefore resolved against the repo root; pass an
# absolute path if you want the output somewhere else.
#
# All arguments are forwarded to fetch_prow_metrics.py, so any of its flags work
# (--job, --start/--end, --min-duration, --suffix, --force, --list-jobs, ...).
# The work directory (--out-dir) holds every per-job folder, the images, and
# recommendation.md.
#
# Examples:
#   ./hack/infra/stats/collect_stats.sh
#       all defaults: every ^pull- job, last 14d at 30s, into artifacts/infra-stats
#
#   ./hack/infra/stats/collect_stats.sh --list-jobs
#       dry-run: print the jobs the default regex matches and exit (downloads nothing)
#
#   ./hack/infra/stats/collect_stats.sh --job-regex '^pull-.*-main'
#       only presubmit (pull) jobs that run against the main branch
#
#   ./hack/infra/stats/collect_stats.sh --job-regex '^periodic-.*-main'
#       only periodic (scheduled) jobs for the main branch
#
#   ./hack/infra/stats/collect_stats.sh --job-regex '^pull-.*e2e-multikueue' --list-jobs
#       preview just the multikueue e2e jobs before committing to a fetch
#
#   ./hack/infra/stats/collect_stats.sh --job pull-kueue-test-e2e-dra-counter-main
#       a single job by exact name (no regex)
#
#   ./hack/infra/stats/collect_stats.sh --days 30
#       override just the range (30 days); everything else stays default
#
#   ./hack/infra/stats/collect_stats.sh --days 30 --step 1m --concurrency 4
#       wider window at a coarser 1-minute step, 4 jobs fetched in parallel
#
#   ./hack/infra/stats/collect_stats.sh --out-dir /tmp/ci-stats
#       write everything to a work dir outside the repo instead
set -u
here="$(cd "$(dirname "$0")" && pwd)"

# Operate from the repo root regardless of where you invoked this from, so the default
# relative --out-dir (artifacts/infra-stats) always lands under the kueue checkout. The
# script lives at hack/infra/stats/, so the root is three levels up.
root="$(cd "$here/../../.." && pwd)"
cd "$root" || { echo "cannot cd to repo root ($root)" >&2; exit 1; }

# ---- defaults (each applied only when you didn't pass that flag) --------------
user_args=("$@")
contains() {  # is flag $1 present in the user args (as "--flag" or "--flag=...")?
  local a
  for a in ${user_args[@]+"${user_args[@]}"}; do
    [ "$a" = "$1" ] && return 0
    case "$a" in "$1"=*) return 0 ;; esac
  done
  return 1
}

defaults=()
# --job / --job-regex are a mutually-exclusive group: default only if neither was given
if ! contains --job && ! contains --job-regex; then defaults+=(--job-regex '^pull-'); fi
contains --days        || defaults+=(--days 14)
contains --step        || defaults+=(--step 30s)
contains --out-dir     || defaults+=(--out-dir artifacts/infra-stats)
contains --concurrency || defaults+=(--concurrency 2)
contains --sleep       || defaults+=(--sleep 2)
contains --retries     || defaults+=(--retries 2)

# defaults first, user args last: fetch_prow_metrics.py (argparse) keeps the last value,
# so an explicitly-passed flag overrides the matching default.
set -- ${defaults[@]+"${defaults[@]}"} "$@"
# ------------------------------------------------------------------------------

# resolve the effective --out-dir (defaulted or overridden) for the post-fetch steps
out="artifacts/infra-stats"
prev=""
for a in "$@"; do
  case "$a" in --out-dir=*) out="${a#--out-dir=}" ;; esac
  [ "$prev" = "--out-dir" ] && out="$a"
  prev="$a"
done

echo "==> fetching   ($*)"
"$here/fetch_prow_metrics.py" "$@" || exit 1

# a dry-run (--list-jobs) fetches nothing, so there is nothing to plot or aggregate
case " $* " in
  *" --list-jobs "*|*" -n "*|*" --dry-run "*) exit 0 ;;
esac

echo "==> rendering images"
for d in "$out"/*/; do
  [ -f "$d/raw_series.json" ] || continue
  "$here/plot.py" "$d" --samples 3 --seed 7 >/dev/null 2>&1 || echo "  plot failed: $d"
done

echo "==> aggregating recommendation table"
"$here/aggregate_reco.py" "$out"
