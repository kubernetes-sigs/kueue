---
name: kueue-pr-evidence-contract-delta
description: Show reviewers a before/after table of user-facing contract changes. The PR author's coding agent compares the merge base and the head and chooses which surfaces matter for this PR. Use when a PR changes an API, CRD, flag, config, metric, or other user-facing contract.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Before/after user-facing contract for a PR

The PR author's coding agent produces a before/after table of user-facing
contract changes for reviewers to read. The comparison is a static read of
the merge base and the head. The PR's tests remain the regression tests.

Publishing follows
[kueue-pr-evidence-common](../kueue-pr-evidence-common/SKILL.md).

## When to use

Start from the PR diff and choose the contract surfaces this change can
affect. The rows depend on the PR. Pick them case by case. Use this skill
when the diff touches a user-facing contract, or when a reviewer asks what
changes for users or whether the release note is ACTION REQUIRED.

For runtime behavior, use
[kueue-pr-evidence-state-diff](../kueue-pr-evidence-state-diff/SKILL.md).
For the release-note text, use
[kueue-release-notes](../kueue-release-notes/SKILL.md). This skill flags
that a note is likely ACTION REQUIRED. The author writes the note. When
every surface you checked is unchanged, publish one line that names those
surfaces.

## What to compare

Read each chosen item at the merge base and at the head. Keep a row only
when the user-facing value differs. Choose from:

- API and CRD. Fields, types, required markers, defaults, enums, bounds,
  validation, CEL, printer columns, and the Configuration API. Read `apis/`
  and `config/components/crd/bases/`.
- Install config. The Helm CRD copy under `charts/kueue/templates/crd/`,
  plus controller flags and config files the PR edits.
- Webhook admission messages.
- Condition reasons, event reasons, and constants that users or other
  controllers match on.
- Metrics. Name, help text, and labels, plus
  `site/content/en/docs/reference/metrics.md`.
- Feature gates. New gates, default flips, and gates that become locked.
- Logs. V-level moves and key renames. A recognized reworded log line
  counts as one change.
- `cmd/kueuectl/`. Flags, output columns, and error text.

Choose a surface only when the diff can affect it.

## How to compare

Resolve the merge base as in
[kueue-pr-evidence-common](../kueue-pr-evidence-common/SKILL.md). Read both
sides with `git show <merge-base>:<path>` and `git show <head>:<path>`.
No worktree is needed. Treat a rename as the same file.

Pair the same field path, metric, flag, gate, reason, or message. A
reworded message you still recognize is one changed row. Otherwise record
one removal and one addition. Keep a format string as written in the
source. When the surrounding webhook or CEL code also changed, say the
behavior changed with the string.

For an API or CRD change, judge compatibility from the files:

- A removed field, a new required field, a narrower enum, a tighter bound,
  or a changed type means existing manifests or clients break.
- A changed default affects requests that omit the field. Manifests that
  set the field explicitly keep that value. Mark the omitted behavior
  unverified unless the code shows it.
- A new optional field keeps the old behavior when omitting it still means
  the same thing. Mark an "If unspecified" sentence unverified unless the
  code shows that omitted behavior.
- Mark CEL rules, webhook branches, and gate-off behavior unverified.
- A gate default flip changes behavior with no opt-in. A locked gate
  rejects the opposite `--feature-gates` value at startup.

When metrics change, compare names and labels with
`site/content/en/docs/reference/metrics.md` and note a mismatch. When CRDs
change, note a Helm copy under `charts/kueue/templates/crd/` that differs
from `config/components/crd/bases/`.

## Block shape

One block, inside the 25 visible-line budget from the common skill. Show at
most 12 changed rows. Columns are surface, change, base, and head. Omit
rows that match.

Visible part, in this order:

1. One factual verdict sentence with the change count.
2. One `Runner: static · surfaces: ...` line. `surfaces` names what you
   compared for this PR.
3. The changed-rows table. When one surface has many items, keep one
   summary row here. For a large log move, that row counts V-level moves,
   reworded lines, additions, removals, and key renames.
4. A **Compatibility** table when an API or CRD change can affect existing
   manifests or clients. Each result is a static finding, or `unverified`.
5. One ACTION REQUIRED line when a metric name, label, annotation, or log
   key is renamed or removed, a default changes, a gate default flips, a
   gate is removed or locked, or a required field, removed field, flag, or
   column forces a user change on upgrade.
6. One `Unchanged:` line listing surfaces you checked and found the same.

Put file locations, and changed rows past the visible table, in
`<details>`.

## Example

Shape only. Choose different rows for the PR in front of you.

```markdown
<!-- pr-evidence:kueue-pr-evidence-contract-delta:start -->
**kueue-pr-evidence-contract-delta** base 602de8f1b432 -> head e691e542aa87

3 user-facing contract changes (CRD 1, Metric 1, Log 1).

Runner: static · surfaces: Workload CRD, admission metric, job reconciler logs

| Surface | Change | Before (merge base) | After (head) |
|---|---|---|---|
| CRD | Workload `spec.mode` default | `Strict` | `Lenient` |
| Metric | `kueue_admission_attempts_total` labels | `result` | `outcome` |
| Log | Reconciling Job | `V(2)` | `V(3)` |

**Compatibility** (static read)

| Check | Result |
|---|---|
| Existing manifests accepted | accepted; nothing removed or newly required |
| Defaults of existing fields | changed: `spec.mode` Strict to Lenient |

Likely **ACTION REQUIRED**: the `spec.mode` default changed, and the metric label was renamed. Draft the note with `kueue-release-notes`.
Unchanged: feature gates, kueuectl flags, webhook messages.

<details><summary>Locations</summary>

| Surface | Change | Where |
|---|---|---|
| CRD | Workload `spec.mode` default | `config/components/crd/bases/kueue.x-k8s.io_workloads.yaml` |

</details>
<!-- pr-evidence:kueue-pr-evidence-contract-delta:end -->
```

## Publish

Follow [kueue-pr-evidence-common](../kueue-pr-evidence-common/SKILL.md). The
skill name in the markers is `kueue-pr-evidence-contract-delta`. Fetch the
PR body, show the diff, and run `gh pr edit` only after the user explicitly
approves.
