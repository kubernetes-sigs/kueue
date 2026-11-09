---
name: kueue-pr-evidence-state-diff
description: Show reviewers a before/after Kueue state table. The PR author's coding agent chooses the observable state from the change and runs the same steps on the merge base and the head. Use when a PR changes scheduling, preemption, admission, MultiKueue, metrics, or TAS.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Before/after Kueue state for a PR

The PR author's coding agent produces a before/after table of Kueue state.
Human reviewers read it. The PR's integration or e2e test remains the
regression test. This skill only turns that test into a short evidence block.

Publishing follows
[kueue-pr-evidence-common](../kueue-pr-evidence-common/SKILL.md).

## When to use

Start from what the PR changes, and choose the observable state for this PR.
The rows depend on the fix. Pick them case by case:

- Scheduling, preemption, or admission: Workload conditions and admission,
  ClusterQueue and LocalQueue counts, `flavorsUsage`, requeue state, events.
- MultiKueue: manager and worker Workload status, and admission checks.
- Metrics: the metric values the PR changes.
- TAS: topology assignments.

For a docs-only, refactor, or API-only PR, stop. There is no observable state
to publish.

## How to run

Derive the steps from the PR's own integration or e2e test. Run those same
steps, unchanged, on the merge base and on the head. The "before" column
comes from the base run.

Resolve the merge base and the base worktree as in
[kueue-pr-evidence-common](../kueue-pr-evidence-common/SKILL.md). Use a second
detached worktree at the PR head when the head run needs a dump file, so the
user's checkout stays untouched.

Capture state at the test's key assertions. In each throwaway tree, add a
temporary untracked dump: a copied spec, or a temporary file in the test
package. Write the captured state under the evidence output directory outside
the repo. The dump stays in the throwaway tree and is deleted with it.

Use envtest by default. Use kind, or the e2e suite with `E2E_MODE=dev`, when
the behavior needs real Pods, PodsReady, DRA, TAS node fit, or MultiKueue.
Name the runner in the block.

A failing base run is evidence of the bug. A failing head run is incomplete:
publish nothing, and fix the steps or the PR.

## Practical notes

- Set the same feature gates on both sides. When a gate does not exist at the
  base, run the base without it and say so on the `gates:` line.
- A manager started by the suite keeps the feature-gate values from setup
  time. When the PR depends on such a gate, start a dedicated manager after
  the gates are set.
- Wait with `Eventually`. Let ClusterQueue status settle past its 1s batch
  period (`constants.UpdatesBatchPeriod`) before you capture. A window of 2s
  with no further change is enough. A shorter window records stale counts.
- Envtest has no kubelet and no job controller. Apply Workloads directly.
  Finish eviction with `FinishEvictionForWorkloads` in
  `test/util/behavioral/workload.go` (package
  `sigs.k8s.io/kueue/test/util/behavioral`). Finish a Workload with
  `FinishWorkloads` in `test/util/behavioral/integration/workload.go`
  (package `sigs.k8s.io/kueue/test/util/behavioral/integration`).
- A Ready Node created in envtest keeps the `node.kubernetes.io/not-ready`
  taint. Remove that taint, or create the Node with `CreateNodesWithStatus`
  in `test/util/behavioral/integration/objects.go` (package
  `sigs.k8s.io/kueue/test/util/behavioral/integration`), which removes it.

## Block shape

One block, inside the 25 visible-line budget from the common skill. Show at
most 12 changed rows. Columns are object, field, base, and head. Omit rows
that match.

Visible part, in this order:

1. One factual verdict sentence.
2. One `Runner: ... · gates: ... · scenario: ...` line. `scenario` names the
   case you chose for this PR.
3. The changed-rows table.
4. One `Unchanged:` line listing what you checked and found the same.
5. One `Repro:` line with the test command.

Put the steps you ran, and changed rows past the visible table, in
`<details>`.

## Example

Shape only. Choose different rows for the PR in front of you.

```markdown
<!-- pr-evidence:kueue-pr-evidence-state-diff:start -->
**kueue-pr-evidence-state-diff** base 602de8f1b432 -> head e691e542aa87

Admitted Workload is preempted on head, and the pending one reserves quota.

Runner: envtest · gates: defaults · scenario: preempt within one ClusterQueue

| object | field | base | head |
|---|---|---|---|
| wl/low | Evicted | absent | True, Preempted |
| wl/high | admission | absent | reserved |
| cq/cq | pendingWorkloads | 1 | 0 |

Unchanged: LocalQueue counts, flavorsUsage.
Repro: `INTEGRATION_TARGET='test/integration/singlecluster/scheduler' GINKGO_ARGS="--focus='preempt within ClusterQueue'" make test-integration`

<details><summary>Steps and remaining rows</summary>

Same steps on the merge base and on the head:

1. Create a ClusterQueue with within-ClusterQueue preemption and a LocalQueue.
2. Create a low-priority Workload and wait until it reserves quota.
3. Create a high-priority Workload that needs that quota.
4. Finish eviction for the low-priority Workload.
5. Capture Evicted, admission, pendingWorkloads, LocalQueue counts, flavorsUsage, and events.

| object | field | base | head |
|---|---|---|---|
| wl/low | event Normal Preempted | absent | present |
| wl/high | event Normal QuotaReserved | absent | present |

</details>
<!-- pr-evidence:kueue-pr-evidence-state-diff:end -->
```

## Publish

Follow [kueue-pr-evidence-common](../kueue-pr-evidence-common/SKILL.md). The
skill name in the markers is `kueue-pr-evidence-state-diff`. Fetch the PR
body, show the diff, and run `gh pr edit` only after the user explicitly
approves.
