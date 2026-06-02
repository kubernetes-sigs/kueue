---
name: kueue-flake-debugger
description: Debug Kueue CI flakes. Use when a user asks to debug a flake, investigate a test failure, a test timeout, or a CI flake, or to compare a failing run with a passing run. Analyzes Prow build logs, kueue/lws/scheduler controller logs, kubelet logs, and test code; for flaky tests, also compares with a recent successful run.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

You are an expert in Kueue which is the project for Workload orchestration.

## Flake debugging

### Two strategies for debugging a failure

There are two ways to investigate a CI failure. Pick deliberately — most of the time, **strategy 1 is your starting point**, and you only escalate to strategy 2 if strategy 1 doesn't yield a clear answer.

**Strategy 1 — Read the test and the code under test.**
Look at what the test asserts and what the production code does. This is the foundation; you cannot reason about a failure without understanding what the test expected and what code path it exercised.

**Strategy 2 — Compare the failed run against a recent successful run of the same job.**
Download the build-log (and controller logs, for E2E) for both runs and diff them along three dimensions: STEP timings, controller events, and resource lifecycle. Use this when strategy 1 hits a wall and the failure has the signatures of a flake.

#### When to use each

| Signal in the failure | Primary strategy | Why |
|---|---|---|
| Assertion shows specific expected vs. actual values (`Expected 5, got 3`) | **Strategy 1** | The failure tells you the gap directly — go to the code that produces the value. |
| `panic`, type error, compile failure, traceback into production code | **Strategy 1** | The failure mode itself is the bug. |
| Test was newly introduced in the failing PR | **Strategy 1** | No historical successful run to compare against. |
| Test fails *every* time (deterministic, reproduces locally) | **Strategy 1** | The success run is the same code path; comparison adds nothing. |
| Unit test with minimal environment dependencies | **Strategy 1** | One process, one log file — no race to reveal. |
| `Timed out after Xs` with no concrete actual value | **Strategy 2** | The failure says the test gave up waiting; you need history to see what it should have observed. |
| Same test passes most CI runs, fails intermittently | **Strategy 2** | By definition a race — code works *most* of the time, contrast reveals when it doesn't. |
| `gomega.Eventually` / `Consistently` near the failing line | **Strategy 2** | These poll over time; the bug is "polling missed the right window" — comparison reveals the timing. |
| Failure involves multiple controllers (Kueue + LWS + scheduler + kubelet) | **Strategy 2** | Too many moving parts to reason about from code alone. |
| Strategy 1 made you suspicious of code but you can't see the bug | **Strategy 2** | A passing run proves the path is workable; the difference is timing or environment, not logic. |

Without strategy 2, flakes often mislead you into "fixing" code that wasn't broken. With *only* strategy 2, you'll find a divergence but not understand its meaning — strategy 1 is still required to close the loop.

#### How the strategies relate

```
Failure observed
    │
    ▼
Strategy 1 (steps 1–5): read failure, identify locations, read logs, read test
    │
    ├── Clear bug in code or test? ──→ go to step 7 (reason) and step 8 (fix)
    │
    └── No clear bug + flake signature (Timed out, intermittent, Eventually)
            │
            ▼
        Strategy 2 (step 6): compare against a successful run
            │
            ├── Divergence isolated ──→ go BACK to strategy 1 with new hypothesis
            │                          ──→ step 7 (reason), step 8 (fix)
            │
            └── No divergence ──→ widen scope: BeforeSuite, full window, environmental
```

Step 6 (comparison) is an escalation built on top of steps 1–5, not the spine of the framework.

### One universal precaution

When grepping any build-log or controller log, **strip ANSI color codes first** or grep will silently miss matches:

```sh
sed 's/\x1b\[[0-9;]*[a-zA-Z]//g' build-logs/failed/build-log.txt | grep "your-term"
```

### Workflow

The mechanical commands for each step live in the reference docs — read the relevant one
and follow it exactly, then return here to reason about and write up the failure.

| Step | What you do | Where |
|---|---|---|
| 1 | Locate the failed Prow run, download the build-log, find the failure block, **decide strategy**, determine test type (integration vs e2e), and (strategy 2) find + download a successful run | [references/gathering-artifacts.md](references/gathering-artifacts.md) |
| 2 | Find `FAILED_SUITE` / `BASE_ARTIFACTS` and lay out local `build-logs/failed/` (and `success/`) | [references/gathering-artifacts.md](references/gathering-artifacts.md) |
| 3 | Identify control-plane and controller Pod names (E2E) | [references/gathering-artifacts.md](references/gathering-artifacts.md) |
| 4 | Download and read kube-scheduler and controller-manager logs; find Pod placement (E2E) | [references/gathering-artifacts.md](references/gathering-artifacts.md) |
| 5 | Read kubelet logs for the relevant Pods, then read the failing test code (timeouts, intervals, predicates) | [references/gathering-artifacts.md](references/gathering-artifacts.md) |
| 6 | (Strategy 2 only) Compare failed vs successful runs along STEP timings, controller events, and resource lifecycle | [references/comparing-runs.md](references/comparing-runs.md) |
| 7 | Reason about the failure (below) | this file |
| 8–9 | Recommend a fix and write the analysis (below) | this file |

### Step 7 — Reason about the failure

Combine the findings from all previous steps: the test's assertion (what was expected), the actual sequence of events in the failed run, and — if you used strategy 2 — the sequence in the successful run.

The root cause of a Kueue flake is almost always one of these archetypes:

- **A race between test polling and a transient controller state.** The test expects state X; X exists briefly; the test polls a moment too late and sees state Y (or no state at all).
- **A timing-dependent ordering** of two unrelated reconcilers (e.g., Kueue reconciles before the API server has propagated a status change).
- **An assumption about the steady state** that holds in some scheduler paths but not others (e.g., `BestEffortFIFO` vs `StrictFIFO` requeue semantics; `notNominated`/Generic vs `skipped`/`FailedAfterNomination`).
- **An environmental difference** between runs (cluster speed, image pull time, slower envtest startup).

Reach a conclusion you can defend with citations.

### Step 8 — Recommendations and final summary

State the smallest possible code change that addresses the root cause, with the file:line and a diff. Then list alternatives you considered and rejected, with reasons. The reviewer will ask "why not X?" — answer those questions in advance.

In the final summary, when you are making a statement you must back that statement up with evidence and citations. The citations could be a log file, a source code file, documentation, etc. When citing a file, include it in a human-readable way to make verification of your claims quick and easy (e.g. `filepath:linenumber`). For every point, when evidence is available, provide a link (clickable when rendered in markdown) to the log file supporting the claim.

### Step 9 — Write the analysis using the template

Final deliverable: a markdown analysis file written to the user's `~/Downloads/` (or a path the user specifies). Follow [ANALYSIS_TEMPLATE.md](ANALYSIS_TEMPLATE.md) in this directory — same sections, same level of evidence, same citation discipline.

The template enforces a few things the reviewer will look for:
- A **plain-English** "Why the test was failing" section in addition to the technical narrative — non-experts should be able to read it.
- A **side-by-side comparison table** (when strategy 2 was used) with timestamps from both runs.
- An **original test author's intent** section — read the PR that introduced the test and explain what the author was trying to verify vs. what the assertion actually checks.
- A **PRs and issues inspected** table — every external reference cited, with what you learned from each.
- An **alternatives considered** subsection in the proposed fix, with reasons for rejection.

### Citation discipline

Every claim in the final analysis must be backed by evidence. When citing:
- **Log entries**: include the timestamp and the exact log fragment. Link the build-log or pod log URL directly so the reader can verify.
- **Source code**: use `path/to/file.go:LINE` format (e.g., `pkg/cache/queue/cluster_queue.go:870`) and link to the file on GitHub when rendered as markdown.
- **PRs and issues**: link as `[#NNNN](https://github.com/kubernetes-sigs/kueue/...)`.

Statements without evidence are suspect — the reviewer will not take them on faith.
