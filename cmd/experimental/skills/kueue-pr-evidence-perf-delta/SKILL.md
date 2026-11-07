---
name: kueue-pr-evidence-perf-delta
description: Compare scheduler performance for a kueue hot-path PR. The author's coding agent reads CI perf artifacts or runs the minimalkueue harness and writes a short median table. Use when the diff touches the scheduler, cache, or TAS/DRA, or the PR claims a performance effect.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Scheduler performance delta for a PR

The PR author's coding agent compares scheduler performance on two sides
and writes a short table. Human reviewers read it. The harness and its
thresholds stay unchanged.

Publishing follows
[kueue-pr-evidence-common](../kueue-pr-evidence-common/SKILL.md).

## When to use

Use this when non-test files changed on the scheduler hot path, or when
the PR claims a performance effect (`perf(...)` in the title, or a claim
that the change is faster or allocates less).

Hot path: `pkg/scheduler/`, `pkg/cache/`, `pkg/resources/`,
`pkg/workload/`, `pkg/controller/core/`, TAS and DRA code
(`pkg/controller/tas/`, `pkg/util/tas/`, `pkg/dra/`, and the same logic
inside generic files such as preemption and flavor assignment), and
`test/performance/scheduler/`.

For a docs-only, API-only, webhook, integration, or test-only PR, stop.
The verdict is `not applicable`.

## Which config

Read the diff and pick one config. One config per block. Confirm the Make
target in `hack/make/test.mk` (these are the names on current main).
Judge TAS and DRA from the changed lines, not only the directory.

| What the diff touches | Make target | Prow presubmit |
|---|---|---|
| TAS and DRA together | `run-tas-dra-performance-scheduler` | none; run locally. Periodic: `periodic-kueue-test-tas-dra-scheduling-perf-main` |
| TAS, including TAS lines in a generic file | `run-tas-performance-scheduler` | `pull-kueue-test-tas-scheduling-perf-main` |
| Other hot-path code | `run-performance-scheduler` | `pull-kueue-test-scheduling-perf-main` |

A large-scale harness change uses `run-large-scale-performance-scheduler`
and `pull-kueue-test-large-scale-scheduling-perf-main`.

## Which source

One table, one source. When both exist, the visible table is the CI
source and the local numbers get their own table in `<details>`. Prefer
CI: it uses no local CPU.

### CI (Prow)

Head side: every successful presubmit for that config whose `prowjob.json`
`spec.refs.pulls[].sha` equals the PR head. Base side: the 3 most recent
successful periodics for the same config
(`periodic-kueue-test-scheduling-perf-main`,
`periodic-kueue-test-tas-scheduling-perf-main`, or
`periodic-kueue-test-large-scale-scheduling-perf-main` for the large-scale
config) with a build id lower than those presubmits. Say in the block
that this base is recent main periodics, not the merge base.

Artifacts are in the public bucket `kubernetes-ci-logs`: presubmits at
`pr-logs/pull/kubernetes-sigs_kueue/<pr>/<job>/<build>/`, periodics at
`logs/<periodic-job>/<build>/`. List with
`https://storage.googleapis.com/storage/v1/b/kubernetes-ci-logs/o?prefix=...&delimiter=/`
and download with `https://storage.googleapis.com/kubernetes-ci-logs/...`.
Read `finished.json` (`result` `SUCCESS`) and `prowjob.json` for the SHA.
The measured files are `summary.yaml`, `minimalkueue.stats.yaml`, and
`metricsDump.tgz` under `artifacts/<test-target>/<run-target>/`.

When no presubmit finished for this head, ask the user to comment
`/test <presubmit>`. Leave that comment for the user. Run the local
source when the user wants numbers before CI finishes.

### Local

Resolve the merge base and the base worktree as in
[kueue-pr-evidence-common](../kueue-pr-evidence-common/SKILL.md). Run the
head side at the PR head. Prebuild both trees (`envtest`,
`performance-scheduler-runner`, `minimalkueue`) before measuring, so build
time stays out of the numbers.

Default 3 runs per side, interleaved `B H H B B H`, on one machine. Each
run is `make -C <tree> <target>` with its own `ARTIFACTS=` directory under
`${XDG_CACHE_HOME:-$HOME/.cache}/kueue-pr-evidence/pr-<n>/`. Use envtest
and minimalkueue. Label a kind or in-cluster run, requested by a reviewer,
as that runner in its own table. One run takes 6 to 8 minutes: start the
series in the background and leave the machine idle.

## Metrics

The visible table carries minimalkueue wall, user CPU, sys CPU, and max
RSS, plus the metrics the PR claims to change and the metrics the diff
can move. Put every other row in `<details>`.

| File | Rows |
|---|---|
| `minimalkueue.stats.yaml` | `wallMs`, `userMs`, `sysMs`, `maxrss` (bytes on darwin, KiB on linux; report MiB) |
| `summary.yaml` | CQ usage `cpuAverageUsage / nominalQuota`, `averageTimeToAdmissionMs` per class, sum of `totalEvictions` |
| `metricsDump.tgz` | newest `*.prometheus` scrape: sum of `kueue_admission_attempts_total`, mean `kueue_admission_attempt_duration_seconds` (sum/count), sum of `kueue_preempted_workloads_total` |

Wall, CPU, and time to admission are seconds. Mean attempt duration is milliseconds.

## Statistics

Median per side, written `median (min–max)` when that side has more than
one run. Delta is the head median against the base median, as a percent.

- `~` when the absolute percent is under 5, or when a count (evictions,
  admission attempts, preempted workloads) moves by fewer than 10.
- A trailing `?` marks a weak delta: the min–max ranges overlap, or a
  side has one run. Ranges that do not overlap are a clear delta.
- CQ usage higher is better. Every other metric here lower is better.
  Write the signed percent, then `better` or `worse` from that direction:
  `−6.2% better` for wall, `+3.0% better` for CQ usage, `+5.4% worse`
  for user CPU.

The verdict names only clear changes, largest first. With none, write
`no clear change` and name the largest weak deltas. On a local run, when
minimalkueue wall `(max-min)/median` exceeds 15% within one side, add
`Noisy machine`. Evictions and time to admission are timing-sensitive.
Quote `rangespec.yaml` only in `<details>`: it is the CI check, and a
laptop result uses a different scale.

## Failures

List every failed, timed-out, or incomplete run in the block (build id or
local run name) and leave it out of the medians. Keep its directory. A
failed base run stays in that list. When the head side has no successful
run, publish the failure list and stop, as in
[kueue-pr-evidence-common](../kueue-pr-evidence-common/SKILL.md).

## Block shape

One block, inside the 25 visible-line budget from the common skill:
verdict, then a provenance line (`CI (Prow)` or `Local`, config, runs per
side, the machine on a local run, `median (min–max)`; the CI line says the
base is recent main periodics, not the merge base), then the metric table,
then one `<details>` line whose body holds every metric, the per-run
values, and the build links.

## Example

Shape only. Choose the rows for the PR in front of you.

```markdown
<!-- pr-evidence:kueue-pr-evidence-perf-delta:start -->
**kueue-pr-evidence-perf-delta** base 602de8f1b432 -> head e691e542aa87

Verdict: no clear change (largest weak: minimalkueue wall −6.2% better?). 4 ~ (±5%), 1 weak.

CI (Prow) · tas · head: pull-kueue-test-tas-scheduling-perf-main ×1 · base: periodic-kueue-test-tas-scheduling-perf-main ×3 (recent main periodics, not the merge base) · median (min–max)

| Metric | Base | Head | Δ |
|---|---:|---:|---|
| minimalkueue wall (s) | 412 (405–420) | 386 | −6.2% better? |
| max RSS (MiB) | 740 (730–755) | 738 | ~ |

<details><summary>All metrics, per-run values, build links</summary>

Per-run wall, CPU, RSS, and the build links.

</details>
<!-- pr-evidence:kueue-pr-evidence-perf-delta:end -->
```

## Publish

Follow [kueue-pr-evidence-common](../kueue-pr-evidence-common/SKILL.md). The
skill name in the markers is `kueue-pr-evidence-perf-delta`. Fetch the PR
body, show the diff, and run `gh pr edit` only after the user explicitly
approves.
