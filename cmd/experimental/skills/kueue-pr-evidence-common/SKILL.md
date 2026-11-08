---
name: kueue-pr-evidence-common
description: Shared conventions for every kueue-pr-evidence-* skill. The PR author's coding agent publishes one evidence block; human reviewers read it. Use when an evidence skill writes or refreshes its block on the author's own PR.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Publish PR reviewer evidence

Conventions for every `kueue-pr-evidence-*` skill. The PR author's coding
agent produces the evidence. Human reviewers read it in the PR body. This
skill adds no evidence of its own. Use it when an evidence skill publishes
a block, or when the user asks to preview or refresh one.

## Own PR, current head

Publish only to a PR whose author is the current `gh` user (`gh api user`).
The head SHA in the block is `git rev-parse HEAD`, and it must equal the PR's
current head (`gh pr view <n> --json headRefOid -q .headRefOid`). Push first
when they differ.

## Merge base

From the PR checkout, set `UPSTREAM` to the remote that points at
`kubernetes-sigs/kueue`:

```bash
UPSTREAM=upstream
git fetch "$UPSTREAM" main
MERGE_BASE="$(git merge-base HEAD "$UPSTREAM/main")"
```

Run the head side at that HEAD. Run the base side in a throwaway worktree and
remove it afterwards:

```bash
git worktree add --detach "$BASE_WT" "$MERGE_BASE"
# base-side run uses $BASE_WT
git worktree remove --force "$BASE_WT"
```

The base side runs only in that worktree. Leave the user's checkout as it is:
do not checkout, reset, stash, or switch branches there.

Keep outputs outside the repo, for example
`${XDG_CACHE_HOME:-$HOME/.cache}/kueue-pr-evidence/pr-<n>/`.

## The block

Put it in `#### Useful notes for your reviewer`. Create that section before
`#### Release note` when it is missing. When other skills already have blocks
in the section, add the new block after them.

One block per skill. The markers sit alone on their lines. `<skill>` is the
skill `name`. `<sha12>` is the first 12 hex characters of each SHA. The first
line inside the markers is the header:

```html
<!-- pr-evidence:<skill>:start -->
**<skill>** base <sha12> -> head <sha12>
block body from the evidence skill
<!-- pr-evidence:<skill>:end -->
```

On a rerun, replace that skill's block in place. Leave every other line of
the body as it is.

At most 25 visible lines. Count every line of a fenced code block. Lines
inside a `<details>` body do not count; the `<details>` line does. Move extra
detail into `<details>`. Mask tokens, IP addresses, and hostnames.

A failing base run is evidence. A failing head run is incomplete: publish
nothing until the head run succeeds.

## Dry run, then publish

1. Fetch the body: `gh pr view <n> --json body -q .body`.
2. Write the new body to a file outside the repo.
3. Show the user the diff against the current body.
4. After the user explicitly approves, run `gh pr edit <n> --body-file <file>`.
