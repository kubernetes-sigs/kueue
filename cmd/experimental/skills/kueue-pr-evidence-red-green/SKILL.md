---
name: kueue-pr-evidence-red-green
description: Show that a kueue PR's new or changed tests fail on the merge base and pass on the head. The author's coding agent picks those tests case by case and writes a short table. Use when asked for red/green or fail-before/pass-after evidence.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Red/green test evidence for a PR

The PR author's coding agent shows that the PR's new or changed tests fail on
the merge base and pass on the head. Human reviewers read the table. The PR's
own test remains the regression test.

Publishing follows
[kueue-pr-evidence-common](../kueue-pr-evidence-common/SKILL.md).

## When to use

Use this when the author asks for red/green, "fails before, passes after", or
proof that a new or changed test catches the bug.

For a before/after table of Kueue object state, use
[kueue-pr-evidence-state-diff](../kueue-pr-evidence-state-diff/SKILL.md).

For a docs-only change, a refactor, or a PR with no new or changed Go test,
stop. There is no red/green result to publish.

## What to run

Pick the tests from this PR. Read the diff and choose the new or changed
`*_test.go` cases that assert the bug. One table row per changed test or
Ginkgo spec. Run those cases on both sides.

The base side is the merge base plus the PR's test and testdata hunks. The
production change stays on the head. A failure on that base is the bug. A
pass on that base means the test does not catch it, so report the pass.

Resolve the merge base and the base worktree as in
[kueue-pr-evidence-common](../kueue-pr-evidence-common/SKILL.md). Apply the
test hunks there, and run the head side at the PR head:

```bash
git diff "$MERGE_BASE" HEAD -- path/to/foo_test.go path/to/testdata | git -C "$BASE_WT" apply
```

Use a second detached worktree at the head when the run would write into the
user's checkout, and remove it afterwards.

When the hunks do not apply, or the patched base does not compile because the
test calls code that exists only on the head, the verdict is
`not applicable: tests depend on new code`. Run the unpatched tests that
already exist at the merge base and put that result in the base cell. Record
a red only for a test that compiled and failed an assertion.

A disk-full, out-of-memory, or network failure is an error. Report
`run errored, not a test result` and the cause, then run again once the
environment is fixed. Keep that run out of the pass/fail cells.

A head assertion failure is incomplete: publish nothing, and fix the test or
the PR. A base assertion failure is the red evidence.

When the new test needs a helper the PR adds under `pkg/util/testing` or
`test/util`, apply that helper onto the base as well and write
`+ test helpers` on the runner line.

## Commands

Run the same command on the patched base and on the head.

Unit tests:

```bash
GO_TEST_TARGET=./pkg/controller/core GO_TEST_FLAGS='-race -count=1 -run ^(TestReconcile)$' make test
```

Single-cluster integration tests:

```bash
INTEGRATION_TARGET=test/integration/singlecluster/scheduler GINKGO_ARGS="--focus='preempt within ClusterQueue'" INTEGRATION_NPROCS=1 make test-integration
```

MultiKueue integration uses `make test-multikueue-integration` with
`INTEGRATION_TARGET_MULTIKUEUE` and the same `GINKGO_ARGS`.
`INTEGRATION_NPROCS=1` for single-cluster, and
`INTEGRATION_NPROCS_MULTIKUEUE=1` for MultiKueue, keep the failure excerpt
on the spec you focused.

Kind e2e tests need a cluster the user asked for. Otherwise name them as not
run, and run the unit or integration test that covers the same change.

When the base run fails without an assertion failure, run it again. A
Gomega Eventually timeout is an assertion failure. Run the base again
when the failure is a suite timeout, a `go test` timeout, or a setup
error. Only an assertion failure counts as red. One assertion failure
on the patched base, and a pass on the head, is the red/green pair.

## Block shape

One block, inside the 25 visible-line budget from the common skill, under
`#### Useful notes for your reviewer`. Columns are test, base, and head.

Visible part, in this order:

1. One factual verdict sentence.
2. One `Runner: ... · base = merge base + test hunks` line.
3. The table of the tests you ran.
4. One `Repro:` line with the command.

Put the first assertion line from the base failure in `<details>`.

- `red→green: N/N changed tests fail on the merge base and pass on head`
- `partial:` or `no red on base:` when a chosen test also passes on the
  patched base. Keep that row in the table.
- `not applicable: tests depend on new code` when the hunks do not apply or
  the patched base does not compile. Base cells say `n/a` plus the unpatched
  result.
- `run errored, not a test result (<cause>)` for disk, memory, or network
  failures. Publish after a completed pass/fail run replaces the error.

## Example

Shape only. Choose the tests for the PR in front of you.

```markdown
<!-- pr-evidence:kueue-pr-evidence-red-green:start -->
**kueue-pr-evidence-red-green** base 602de8f1b432 -> head e691e542aa87

red→green: 1/1 changed tests fail on the merge base and pass on head.

Runner: envtest · base = merge base + test hunks

| test | base | head |
|---|---|---|
| preempt within ClusterQueue | fail | pass |

Repro: `INTEGRATION_TARGET=test/integration/singlecluster/scheduler GINKGO_ARGS="--focus='preempt within ClusterQueue'" INTEGRATION_NPROCS=1 make test-integration`

<details><summary>Base failure</summary>

Expected admitted Workload, got pending.

</details>
<!-- pr-evidence:kueue-pr-evidence-red-green:end -->
```

## Publish

Follow [kueue-pr-evidence-common](../kueue-pr-evidence-common/SKILL.md). The
skill name in the markers is `kueue-pr-evidence-red-green`. Fetch the PR
body, show the diff, and run `gh pr edit` only after the user explicitly
approves.
