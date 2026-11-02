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

# Throwaway detached kueue worktrees for generating the "before" side.
#
# Usage:
#   kueue-worktree.sh add <sha> [--repo DIR]   # prints the new worktree path
#   kueue-worktree.sh remove <path>            # only paths under the managed root
#   kueue-worktree.sh list                     # managed worktrees
#   kueue-worktree.sh gc [--repo DIR]          # remove all managed worktrees, prune
#
# Managed root: $PR_EVIDENCE_WORKTREE_ROOT, else
#   ${XDG_CACHE_HOME:-~/.cache}/kueue-pr-evidence/worktrees
#
# `add` runs `git worktree add --detach` with hooks disabled. It never checks
# out, resets, or switches branches in --repo (default: current directory); it
# only adds an entry under the shared .git/worktrees/ that `remove` and `gc`
# delete. The new tree has its own bin/; nothing is shared with the PR tree.

set -o errexit
set -o nounset
set -o pipefail

root="${PR_EVIDENCE_WORKTREE_ROOT:-${XDG_CACHE_HOME:-$HOME/.cache}/kueue-pr-evidence/worktrees}"

die() { echo "kueue-worktree.sh: $*" >&2; exit 2; }
usage() { sed -n '17,31p' "$0" | sed 's/^# \{0,1\}//'; }

abs() { (cd "$1" 2>/dev/null && pwd -P); }

under_root() {
  local p r
  p="$(abs "$1")" || return 1
  r="$(abs "$root")" || return 1
  [[ "$p" == "$r"/* ]]
}

remove_one() {
  local path="$1" common
  under_root "$path" || die "refusing: $path is not under $root"
  common="$(git -C "$path" rev-parse --path-format=absolute --git-common-dir 2>/dev/null || true)"
  if [[ -n "$common" ]]; then
    git --git-dir="$common" worktree remove --force "$path" 2>/dev/null || rm -rf "$path"
    git --git-dir="$common" worktree prune
  else
    rm -rf "$path"
  fi
  echo "removed $path" >&2
}

cmd="${1:-}"
[[ $# -gt 0 ]] && shift
case "$cmd" in
  add)
    [[ $# -ge 1 ]] || die "add needs <sha>"
    sha="$1"; shift
    repo=.
    while [[ $# -gt 0 ]]; do
      case "$1" in
        --repo) repo="$2"; shift 2 ;;
        *) die "unknown arg: $1" ;;
      esac
    done
    full="$(git -C "$repo" rev-parse --verify --quiet "$sha^{commit}")" || die "unknown commit: $sha"
    mkdir -p "$root"
    path="$(mktemp -d "$root/${full:0:12}.XXXXXX")"
    rmdir "$path"
    git -C "$repo" -c core.hooksPath=/dev/null worktree add --quiet --detach "$path" "$full" >&2
    echo "$path"
    ;;
  remove)
    [[ $# -eq 1 ]] || die "remove needs <path>"
    remove_one "$1"
    ;;
  list)
    [[ -d "$root" ]] || exit 0
    find "$root" -mindepth 1 -maxdepth 1 -type d -print
    ;;
  gc)
    repo=.
    [[ "${1:-}" == --repo ]] && repo="$2"
    if [[ -d "$root" ]]; then
      find "$root" -mindepth 1 -maxdepth 1 -type d -print | while read -r p; do remove_one "$p"; done
    fi
    git -C "$repo" worktree prune 2>/dev/null || true
    ;;
  -h | --help | "") usage ;;
  *) die "unknown command: $cmd" ;;
esac
