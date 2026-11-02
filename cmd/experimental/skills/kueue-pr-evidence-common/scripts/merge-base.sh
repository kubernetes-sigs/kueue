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

# Print the upstream merge base and HEAD SHAs of a kueue worktree.
#
# Usage: merge-base.sh [--worktree DIR] [--branch main] [--no-fetch]
#
# Prints eval-able KEY=VALUE lines on stdout:
#   UPSTREAM_REMOTE=upstream
#   UPSTREAM_REF=refs/remotes/upstream/main
#   MERGE_BASE=<40-hex>
#   HEAD_SHA=<40-hex>
#
# The remote is $PR_EVIDENCE_UPSTREAM_REMOTE if set, else `upstream` when it
# points at kubernetes-sigs/kueue, else the first remote that does. The script
# only fetches; it never pushes or changes the worktree.

set -o errexit
set -o nounset
set -o pipefail

worktree=.
branch=main
fetch=1
while [[ $# -gt 0 ]]; do
  case "$1" in
    --worktree) worktree="$2"; shift 2 ;;
    --branch) branch="$2"; shift 2 ;;
    --no-fetch) fetch=0; shift ;;
    -h | --help) sed -n '17,29p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "merge-base.sh: unknown arg: $1" >&2; exit 2 ;;
  esac
done

g() { git -C "$worktree" "$@"; }

g rev-parse --is-inside-work-tree >/dev/null 2>&1 || {
  echo "merge-base.sh: not a git worktree: $worktree" >&2
  exit 2
}

is_kueue_url() { [[ "$1" =~ github\.com[:/]kubernetes-sigs/kueue(\.git)?/?$ ]]; }

remote="${PR_EVIDENCE_UPSTREAM_REMOTE:-}"
if [[ -z "$remote" ]]; then
  if is_kueue_url "$(g remote get-url upstream 2>/dev/null || true)"; then
    remote=upstream
  else
    while read -r r; do
      if is_kueue_url "$(g remote get-url "$r")"; then remote="$r"; break; fi
    done < <(g remote)
  fi
fi
if [[ -z "$remote" ]]; then
  echo "merge-base.sh: no remote points at kubernetes-sigs/kueue; add one:" >&2
  echo "  git remote add upstream https://github.com/kubernetes-sigs/kueue.git" >&2
  exit 2
fi

if [[ "$fetch" == 1 ]]; then
  g fetch --quiet "$remote" "$branch" >&2
fi
ref="refs/remotes/$remote/$branch"
if ! g rev-parse --verify --quiet "$ref^{commit}" >/dev/null; then
  ref=FETCH_HEAD
fi

merge_base="$(g merge-base HEAD "$ref")"
head_sha="$(g rev-parse HEAD)"

if [[ -n "$(g status --porcelain --untracked-files=no)" ]]; then
  echo "merge-base.sh: warning: uncommitted changes are not part of HEAD_SHA" >&2
fi

printf 'UPSTREAM_REMOTE=%s\nUPSTREAM_REF=%s\nMERGE_BASE=%s\nHEAD_SHA=%s\n' \
  "$remote" "$ref" "$merge_base" "$head_sha"
