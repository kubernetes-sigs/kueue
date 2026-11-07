---
name: kueue-pr-evidence-red-green
description: Show that a kueue PR's new or changed tests fail on the merge base and pass on the head. The author's coding agent picks those tests case by case and writes a short table. Use when asked for red/green or fail-before/pass-after evidence.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Red/green test evidence for a PR

The PR author's coding agent shows whether the PR's new or changed tests fail
on the merge base and pass on the head. Human reviewers read the table. The
PR's own test remains the regression test.

Publishing follows
[kueue-pr-evidence-common](../kueue-pr-evidence-common/SKILL.md).

## When to use

Use this when the author asks for red/green, "fails before, passes after", or
proof that a new or changed test catches the bug. For a before/after table of
Kueue object state, use
[kueue-pr-evidence-state-diff](../kueue-pr-evidence-state-diff/SKILL.md).

For a docs-only change, a refactor, or a PR with no new or changed Go test,
stop. There is no red/green result to publish.

## Choose and prepare

Read the PR diff and select its new or changed Go test functions, table cases,
or Ginkgo `It`/`Entry` specs that assert the bug. Use one row per selected
case. Note kind e2e tests as not run; this skill does not run them. If a unit
or integration test covers the same change, select it too; otherwise report the
coverage gap, not a substitute result.

Resolve the merge base and create a detached base worktree as in
[kueue-pr-evidence-common](../kueue-pr-evidence-common/SKILL.md). Record the
committed head SHA and always create a second detached worktree at that SHA:
uncommitted or untracked files in the user's checkout must not affect either
run. Use these worktrees for all commands and remove them afterward. Store
patches and logs outside the repository, as in the common skill.

The base is the merge base plus only the selected test and testdata changes;
production changes stay on head. Inspect the diff and select hunks carefully
when a file mixes test and production code. If a test needs a new test-only
helper under `pkg/util/testing` or `test/util`, include only that helper and
say `+ test helpers` on the runner line. Never copy production code.

For files containing only the intended test changes, for example:

```bash
git diff --binary "$MERGE_BASE" "$HEAD_SHA" -- path/to/foo_test.go path/to/testdata > "$PATCH"
git -C "$BASE_WT" apply --check "$PATCH"
git -C "$BASE_WT" apply "$PATCH"
```

Choose absolute paths for `BASE_WT`, `HEAD_WT`, and `PATCH`; set `HEAD_SHA` to
the recorded commit. Check each command's exit status before continuing: do
not treat a failed diff, empty or incomplete patch, or failed apply as a valid
base. Inspect the resulting diff against the PR to confirm the intended hunks
and binary testdata are present. Record the paths and any selected hunks so
another person can reconstruct the patched base. Run diff and apply separately
so a failed `git diff` is visible.

If the hunks cannot apply, or the patched base cannot compile because the
tests need head-only code, report `not applicable: tests depend on new code`.
Include the first apply or compiler error. Run existing tests of the same
functions on the unpatched base and report their results separately; they are
not a result for the new tests. Do not call an unrelated build or environment
failure a dependency on new code.

## Run and classify

Run the same selected cases and settings on the patched base and clean head.
Record commands, environment, feature gates, prerequisites, and testdata so the
runs can be reproduced. Use fresh logs for each run. Disable Go test caching.
For example, replace the package and pattern with the selected unit case:

```bash
go test -json -count=1 -run '^TestReconcile$/^gated_workload$' ./pkg/controller/core
```

Inspect the JSON events for each named test and subtest, including `run` and
`pass` or `fail`; a successful command with no matching tests, a skip, or a
parent pass without the selected subtest is not a pass. For integration,
replace the target and focus with the selected spec:

```bash
INTEGRATION_TARGET=./test/integration/singlecluster/scheduler GINKGO_ARGS="--fail-on-empty --focus='preempt within ClusterQueue'" INTEGRATION_NPROCS=1 make test-integration
```

MultiKueue uses `make test-multikueue-integration`,
`INTEGRATION_TARGET_MULTIKUEUE`, and `INTEGRATION_NPROCS_MULTIKUEUE=1` with
the same `GINKGO_ARGS`. The make targets produce Ginkgo JSON reports; inspect
the report and output to confirm that each selected spec actually ran and
passed or failed. `--fail-on-empty` alone cannot prove every chosen spec ran.
Use the same runner and settings on both sides.

Run each selected case on the patched base three times. Report red only if
all three runs execute the case and fail the relevant assertion; report a mix
of assertion failures and passes as flaky, with counts. Three passes mean no
red on base. Incomplete, skipped, or errored runs do not count as either pass
or assertion failure; fix the cause and repeat them before deciding. A head
assertion failure or incomplete head run means publish nothing until resolved.

Only an assertion in the selected test's path is red. A Gomega `Eventually`
timeout there is an assertion failure, but setup or teardown failures, suite
or `go test` timeouts, compilation failures, and disk, memory, or network
errors are not. For example, a webhook readiness timeout during suite setup is
not a spec failure. Report `run errored, not a test result` with the cause;
fix the environment and rerun. Preserve the first relevant assertion line.

## Head CI evidence

Find the Prow job and build for the recorded head SHA, and link its JUnit
results when available. Check that the matching testcase actually passed;
name the job and build and report the matching result. Do not attribute results
from an older SHA to head or rerun CI. If CI is unfinished, say so. If the
head job or matching JUnit result cannot be found or verified, say it is
unavailable and why; do not imply it passed.

## Block shape

One block under `#### Useful notes for your reviewer`, inside the 25 visible-line
budget from the common skill. Columns are test, base, and head. In order show:

1. One factual verdict sentence, including flaky or no-red cases.
2. `Runner: ... · base = merge base + test hunks` (and helpers if used).
3. The table, with base counts and the head result for each case.
4. `Prow:` job and build link and matching result, or CI status.
5. `Repro:` command and enough information to reconstruct the base patch.

Put the first assertion line, setup details, and extra reproducibility notes
in `<details>`. Include not-run kind e2e cases and any coverage gap. For a
not-applicable case, use `n/a` in the new test's base cell and show the first
error and unpatched existing-test result separately. Keep infrastructure
errors separate from pass/fail cells.

## Example

Shape only. Choose the tests and actual Prow result for the PR in front of you.

```markdown
<!-- pr-evidence:kueue-pr-evidence-red-green:start -->
**kueue-pr-evidence-red-green** base 602de8f1b432 -> head e691e542aa87

red→green: 1/1 changed tests fail on base and pass on head.

Runner: envtest · base = merge base + test hunks

| test | base | head |
|---|---|---|
| preempt within ClusterQueue | fail 3/3 | pass |

Prow: CI has not finished for head e691e542aa87.
Repro: `INTEGRATION_TARGET=./test/integration/singlecluster/scheduler GINKGO_ARGS="--fail-on-empty --focus='preempt within ClusterQueue'" INTEGRATION_NPROCS=1 make test-integration`

<details><summary>Base patch and failure</summary>

Apply the selected test and testdata hunks from this PR to the merge base.
Expected admitted Workload, got pending.

</details>
<!-- pr-evidence:kueue-pr-evidence-red-green:end -->
```

## Publish

Follow [kueue-pr-evidence-common](../kueue-pr-evidence-common/SKILL.md). The
skill name in the markers is `kueue-pr-evidence-red-green`. Fetch the PR
body, show the diff, and run `gh pr edit` only after the user explicitly
approves.
