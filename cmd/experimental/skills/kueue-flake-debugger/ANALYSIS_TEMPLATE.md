# Issue #<NNNN> Analysis

**Test:** `<full Ginkgo test name as it appears in build-log.txt>`

**Issue:** [kubernetes-sigs/kueue#<NNNN>](https://github.com/kubernetes-sigs/kueue/issues/<NNNN>)
**Failing job:** `<job-name>` (integration | E2E — *state which, this determines what artifacts you needed*)
**Failing test file:** `path/to/test_file.go`
**Investigation method:** Comparison of failed and successful runs of the same job. Failed-run Prow URL auto-discovered from the issue body; successful-run Prow URL auto-discovered via `prow.k8s.io/job-history/.../<job-name>`. (Or: provided by the user.)

---

## Core Problem

*One or two paragraphs of dense technical narrative. State the root cause and the immediate mechanism. Cite the specific code paths (file:line) involved. This section is for someone who knows the codebase well.*

---

## Why the test was failing (plain version)

**Setup:**

- *Bullet list of the test's setup in everyday language. No code.*
- *What objects are created, what their roles are, what they're trying to do.*

*Then a short paragraph describing what the test does (the sequence of actions) and what it asserts.*

**What goes wrong:**

*A few sentences explaining the bug in plain English. Avoid jargon. If a reviewer who hasn't worked on this area can't follow it, rewrite it.*

*Conclude with a sentence about which of the two runs got lucky and why.*

---

## Failed Run — Simple Events

**PR:** <PR number> | **Build:** [`<build-id>`](https://prow.k8s.io/view/gs/kubernetes-ci-logs/pr-logs/pull/kubernetes-sigs_kueue/<PR>/<job-name>/<build-id>) | **Namespace:** `<test-namespace>` | **Date:** <YYYY-MM-DD>

| # | Time | Event |
|---|------|-------|
| 1 | HH:MM:SS.mmm | *Setup completes (BeforeEach finishes, resources created)* |
| 2 | HH:MM:SS.mmm | *STEP: <first relevant ginkgo.By>* |
| ... | ... | ... |
| N | HH:MM:SS.mmm | **TEST TIMEOUT / FAILURE** — *exact error message* |

*Keep this short: 10-20 rows max. One row per ginkgo.By STEP, plus the key controller events around the failure. The detail goes in the next section.*

---

## Failed Run — Detailed Events

### Event <#> | HH:MM:SS.mmm | <short title>

*Plain-prose paragraph explaining what happened at this moment. Include the relevant variable values (which workload, which CQ, what the metric reported, etc.). Reference the code path that produced this behavior.*

> **Evidence:** *exact log fragment, in a blockquote*
> File: [`path/to/log`](https://full-link-to-the-log-file)

*Repeat for each event that's critical to understanding the failure. Typically 3-5 detailed events. The simple-events table above covers everything chronologically; this section explains the meaningful ones.*

---

## Good Run — Simple Events

**PR:** <PR number> | **Build:** [`<build-id>`](https://prow.k8s.io/view/gs/...) | **Namespace:** `<test-namespace>` | **Date:** <YYYY-MM-DD>

| # | Time | Event |
|---|------|-------|
| 1 | HH:MM:SS.mmm | *...* |
| ... | ... | ... |

*Mirror the structure of the failed-run table. Use the same column meanings so the reader can compare row-by-row.*

---

## Good Run — Detailed Events

### Event <#> | HH:MM:SS.mmm | <short title>

*Same pattern as the failed run's detailed events. Focus on the events that *differed* from the failed run.*

---

## Good Run — Key Differences from Failed Run

| Aspect | Failed Run | Good Run |
|--------|-----------|----------|
| *Gap between two key STEPs* | *X ms* | *Y ms* |
| *Scheduler cycle behavior at critical moment* | *...* | *...* |
| *Metric state during the assertion window* | *...* | *...* |
| *Lifecycle of the disputed object* | *created → deleted (Xs alive)* | *created → persisted (Ys alive)* |
| Test outcome | **FAILED** at HH:MM:SS.mmm | **PASSED** in <time> |

*One row per meaningful dimension of comparison. Aim for 4-6 rows. End the section with a sentence pinpointing the single most meaningful difference.*

---

## Why the failed run was different

*Brief section explaining whether you could pinpoint the underlying cause of the divergence (e.g., "cluster ran X% slower") or whether it's intrinsically racy (e.g., "the test relies on a Y ms window which is shorter than the polling interval").*

*If you cannot pinpoint exactly why this particular run lost the race, say so explicitly and explain why the race itself is the bug — not the run's bad luck.*

---

## Original Test Author's Intent

*Find the commit that introduced the failing test. Use:*

```sh
git log --all --oneline -S "<distinctive string from the test>" -- <test-file-path>
```

*Then read that commit's PR/issue and explain:*

### Evidence of the author's intent

*What the author actually wrote vs. what they likely intended to verify. Cite the commit SHA and PR number. If a sibling test in the same commit handles a similar case correctly, point that out — it usually proves the author understood the concept and made a localized mistake.*

### What the test name implies vs. what the assertion checks

*Often these diverge — the test name describes a behavior, the assertion checks a transient state. Spell out the gap.*

### What semantic signals confirm the intent

*E.g., the choice of `Eventually` vs. `Consistently`, the choice of `Timeout` vs. `MediumTimeout`, the absence of a sleep/wait. These signals tell you what the author expected to happen.*

---

## PRs and Issues Inspected

| Reference | Purpose | What I learned |
|---|---|---|
| [Issue #<NNNN>](https://github.com/kubernetes-sigs/kueue/issues/<NNNN>) | The flake being fixed | *...* |
| [PR #<MMMM>](https://github.com/kubernetes-sigs/kueue/pull/<MMMM>) — `<SHA>` | Introduced the test | *...* |
| [PR #<...>](https://github.com/kubernetes-sigs/kueue/pull/<...>) | Cherry-pick of <MMMM> to release-X.Y | *... or "No change to assertion logic"* |
| Build log (failed) | Source of failed-run timeline | [<short URL>](https://gcsweb.k8s.io/...) |
| Build log (successful) | Source of success-run timeline (auto-discovered from `prow.k8s.io/job-history/...`) | [<short URL>](https://gcsweb.k8s.io/...) |
| *<controller log path>* | *what you read it for* | *what you learned* |

### Source files inspected

| File | Why |
|---|---|
| [`<path>`](https://github.com/kubernetes-sigs/kueue/blob/main/<path>) | *...* |

---

## Findings Summary

1. **Root cause:** *One sentence pinpointing the bug.*

2. *Important secondary finding (e.g., "the race is not in Kueue, it's in the test").*

3. *What the test should verify vs. what it does verify.*

4. *Investigation lesson (e.g., "for E2E tests, controller logs from `artifacts/.../pods/.../manager/0.log` are essential" or "for integration tests, build-log.txt is sufficient").*

5. *Description of the fix at a high level — one or two sentences.*

---

## Proposed Fix

*One-line description of the change and the file:line where it goes.*

### Rationale for the chosen approach

> *A blockquote explaining why this approach was chosen. If the fix carries a non-obvious design choice, explain it here — this content is what would otherwise be a code comment, but kept out of the source to avoid clutter.*

### Diff

```diff
 // unchanged context line
-// removed line
+// added line
```

### Alternative fixes considered (and rejected)

| Option | Description | Why rejected |
|--------|-------------|--------------|
| **A. (chosen)** *<short name>* | *what it does* | *why it's right* |
| **B.** *<alternative>* | *what it does* | *why not* |
| **C.** *<alternative>* | *what it does* | *why not* |

*Including this table preempts the reviewer's "why didn't you do X?" questions.*

---

## Notes on next steps

*If the fix should be cherry-picked to a release branch, note it here:*

> After merge to `main`, the same fix should be cherry-picked to `release-X.Y` (the branch where the flake reproduces) via `/cherry-pick release-X.Y` comment on the merged PR — the k8s-infra prow bot will open the cherry-pick PR automatically.

*If the bug also exists on older release branches, list them.*
