---
name: kueue-pr-evidence-common
description: Shared scripts and conventions for the kueue-pr-evidence-* skills. Resolves the merge base and head SHAs, creates a throwaway worktree at the merge base, and writes one marked evidence block into the "Useful notes for your reviewer" section of the user's own PR, dry-run first. Use when an evidence skill publishes its block, or when a user asks to preview or refresh one.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Publish PR reviewer evidence

Library skill for the `kueue-pr-evidence-*` family
([#18141](https://github.com/kubernetes-sigs/kueue/issues/18141)). Each
evidence skill generates its own content and calls the scripts here to resolve
SHAs, build the base side, and publish the block. This skill adds no evidence
itself.

## When to use

- An evidence skill has a block ready to publish.
- A user asks to preview, refresh, or repair an evidence block in their PR
  description.

## Conventions

1. **One block per skill.** Each skill owns one block under
   `#### Useful notes for your reviewer`, between
   `<!-- pr-evidence:<skill>:start -->` and `<!-- pr-evidence:<skill>:end -->`.
   A re-run replaces the block in place and keeps all other text.
2. **About 25 visible lines.** The injector rejects a block with more than 25
   visible lines, header included. Lines inside `<details>` do not count; the
   `<details>` line does. Start with a one-line verdict, then a small table.
   Use bold text instead of `####` headings inside a block.
3. **Generated base.** Build the "before" side from the merge base, never
   from memory. The header line pins the base and head SHAs.
4. **envtest by default.** Use kind only when the behavior needs real Pods
   (PodsReady), DRA, TAS node-fit, or MultiKueue, and name the runner in the
   block.
5. **Own PRs, dry-run first.** Publish only to a PR the `gh` user opened.
   Always run `--dry-run`, show the diff, and wait for the user's approval.

## Steps

`PR_WORKTREE` is a checkout of the PR head. Run the scripts from the kueue
checkout that contains this skill; it can be the same tree.

1. Resolve the SHAs and an output directory outside the repository:

   ```bash
   C="$(git rev-parse --show-toplevel)/cmd/experimental/skills/kueue-pr-evidence-common/scripts"
   eval "$("$C/merge-base.sh" --worktree "$PR_WORKTREE")"  # UPSTREAM_REMOTE UPSTREAM_REF MERGE_BASE HEAD_SHA
   OUT="${XDG_CACHE_HOME:-$HOME/.cache}/kueue-pr-evidence/pr-<number>"
   mkdir -p "$OUT"
   ```

2. When the skill runs code on the base side, create a throwaway tree:

   ```bash
   BASE_WT="$("$C/kueue-worktree.sh" add "$MERGE_BASE" --repo "$PR_WORKTREE")"
   trap '"$C/kueue-worktree.sh" remove "$BASE_WT"' EXIT
   ```

   Run the base side in `$BASE_WT` and the head side in `$PR_WORKTREE`. Leave
   the user's trees as they are: use `kueue-worktree.sh` instead of checkout,
   reset, stash, or a branch switch. The base tree keeps its own `bin/`, so
   tool versions can differ from head.

3. Write the block content to `$OUT/<skill>.md`. The injector adds the markers
   and the header line, so leave both out of the file.

4. Preview, show the diff to the user, and wait for approval:

   ```bash
   "$C/inject-pr-notes" --pr <number|url> --skill <skill> --file "$OUT/<skill>.md" \
     --base "$MERGE_BASE" --head "$HEAD_SHA" --dry-run
   ```

5. Publish with the same command without `--dry-run`, then reply with the PR
   URL.

## inject-pr-notes

- Writes the header `**<skill>** base <sha12> -> head <sha12>`.
- `--skill` must match `kueue-pr-evidence-<name>`. `--base` and `--head` are
  required.
- Refuses when the PR author is not `gh api user`, or when `--head` is not the
  PR head commit (push first). Ask the user before passing
  `--allow-other-author` or `--allow-stale-head`. With `--dry-run`, refusals
  print as warnings and the diff is still shown.
- Creates the section before `#### Release note` when it is missing. New
  blocks go after existing blocks. CRLF bodies stay CRLF.
- `--body-in <file>` reads the body from a file and never calls `gh`. It
  prints the diff with `--dry-run`, otherwise the new body. Use it in tests
  and evals.

| Exit code | Meaning |
|---|---|
| 0 | Updated, previewed, or no change |
| 1 | `gh` failed or is missing |
| 2 | Usage error, malformed markers, or block over budget |
| 3 | Refused: other author or stale head |

## Captures

`kueue-pr-evidence-state-diff` writes one JSON capture per side, and
`kueue-pr-evidence-timeline` reads them. The format is in
[capture-schema.md](capture-schema.md), with an example in
[examples/capture.example.json](examples/capture.example.json).

## Tests

Run from the repository root:

```bash
python3 -B -m unittest discover -s cmd/experimental/skills/kueue-pr-evidence-common/tests -p '*_test.py'
```

The tests run offline with a stub `gh`. `make shell-lint` covers the `.sh`
scripts.
