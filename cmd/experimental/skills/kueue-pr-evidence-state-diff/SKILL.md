---
name: kueue-pr-evidence-state-diff
description: Show reviewers how Kueue state changes with a PR. Runs one declarative scenario on the merge base and on the PR head (envtest by default), diffs Workload conditions and admission, ClusterQueue and LocalQueue counts, flavorsUsage, requeue state and events, and publishes a short table under "Useful notes for your reviewer", dry-run first. Use when a PR changes scheduling, preemption, admission or queue status behavior.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Before/after Kueue state for a PR

Part of the `kueue-pr-evidence-*` family
([#18141](https://github.com/kubernetes-sigs/kueue/issues/18141)). One scenario
runs unchanged on `MERGE_BASE` and on `HEAD_SHA`. The "before" column always
comes from the merge base run, never from memory. Publishing goes through
[kueue-pr-evidence-common](../kueue-pr-evidence-common/SKILL.md).

## When to use

- A PR changes controller or scheduler behavior that shows up in Workload
  conditions, admission, ClusterQueue or LocalQueue status, or events.
- A merged PR needs re-verification: pass `--commit <merge-sha>`.
- Skip API-only, docs-only and refactor PRs; the block would say there is no
  observable state change.

## Steps

Run from the kueue checkout that contains this skill. `PR_WORKTREE` is a
checkout of the PR head.

1. **Pick or write a scenario.** `scripts/state-diff list` shows the built-ins
   in `scenarios/`. For a PR-specific scenario, derive a YAML file from the PR's
   own integration test and keep it outside the repository, for example in
   `$OUT/scenarios/`. The format is in [scenario-format.md](scenario-format.md).
   Gate every capture on an assertion and use waits instead of sleeps. Check the
   file with `scripts/state-diff compile --scenario <file>`.

2. **Resolve the SHAs and the output directory:**

   ```bash
   C=cmd/experimental/skills/kueue-pr-evidence-common/scripts
   S=cmd/experimental/skills/kueue-pr-evidence-state-diff/scripts
   eval "$("$C/merge-base.sh" --worktree "$PR_WORKTREE")"  # MERGE_BASE HEAD_SHA
   OUT="${XDG_CACHE_HOME:-$HOME/.cache}/kueue-pr-evidence/pr-<number>"
   ```

3. **Run both sides:**

   ```bash
   "$S/state-diff" run --scenario <name|file> --repo "$PR_WORKTREE" \
     --base "$MERGE_BASE" --head "$HEAD_SHA" --out "$OUT"
   # merged PR instead: --commit <merge-sha>   (base = merge-sha^1)
   ```

   Each side gets its own `kueue-worktree.sh` tree, so the PR worktree stays
   untouched. The CLI overlays an untracked driver test into the scenario's
   integration suite there, runs it, and removes the tree. It prints
   `runner: envtest|kind <reason>` and writes the captures
   (`pr-evidence.capture/v1`), `run.json`, `diff.json` and the block
   `$OUT/kueue-pr-evidence-state-diff.md`.

4. **Check the verdict.** If the generated verdict misses the point of the PR,
   re-render with one factual line that states the delta:

   ```bash
   "$S/state-diff" render --dir "$OUT/state-diff/<scenario>" --verdict "..."
   ```

5. **Preview, then publish.** Show the dry-run diff to the user and wait for
   approval before running the same command without `--dry-run`:

   ```bash
   "$C/inject-pr-notes" --pr <number> --skill kueue-pr-evidence-state-diff \
     --file "$OUT/kueue-pr-evidence-state-diff.md" \
     --base "$MERGE_BASE" --head "$HEAD_SHA" --dry-run
   ```

   `run` and `render` also accept `--pr <number>` or `--body-in <file>` to print
   the same dry-run. They never write the PR.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Block rendered. A base assertion failure is evidence of the bug. |
| 2 | Usage or scenario error, or the scenario needs the kind runner. |
| 3 | Head failed an assertion, both sides failed, or the run broke. Fix the scenario or the PR and publish nothing. A head failure writes the block to `state-diff/<scenario>/block.head-failed.md`, never to the publish path. |

`RUN BROKEN` with `cache entry not found` or `bad checksum` means something
cleaned the Go build cache during the build. Re-run with a private
`GOCACHE=... GOMODCACHE=...`.

## Block shape

A verdict line, then `Runner: ... · gates: ... · scenario ...`, a table with
only the changed fields (12 rows visible at most), one `Unchanged:` line and a
`Repro:` line. A `<details>` section holds the full diff, failure messages,
capture paths and the scenario YAML. The CLI exits 3 when the block exceeds the
25-line budget.

## Runner selection

- **envtest** (default): an API server plus the Kueue manager from the
  scenario's integration suite package (default
  `test/integration/singlecluster/scheduler`). Envtest has no kubelet and no job
  controller, so Workloads are applied directly and the `finishEviction` and
  `finish` steps simulate the job side.
- **kind**: chosen when the scenario declares
  `requires: [pods-ready|dra|tas-node-fit|multikueue]` or `runner: kind`. The
  CLI prints the choice and stops. Run each side with `E2E_MODE=dev`
  (`make kind-image-build` in each tree, then the e2e harness), write captures
  in the [capture format](../kueue-pr-evidence-common/capture-schema.md) to
  `$OUT/state-diff/<scenario>/capture.{base,head}.json`, and use `render`.
  When `run.json` is missing or disagrees with the captures' `run` fields,
  `render` rebuilds it from them.

## Tests

Run from the repository root:

```bash
python3 -B -m unittest discover -s cmd/experimental/skills/kueue-pr-evidence-state-diff/tests -p '*_test.py'
```

The tests run offline: no cluster, no Go build, no network.
