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

### Step 1 — Initial build-log analysis

#### 1.1 Identify the failed Prow run

When asked to debug a flake with a GitHub issue link, identify the Prow link to the failure.

Report the list of all Prow links. Try `gh` first; otherwise fall back to `curl`:

```sh
# Note: If the issue context is already provided or visible, skip this and read the Prow link directly from the issue description.
curl -Lv https://github.com/kubernetes-sigs/kueue/issues/ABCD 2>/dev/null \
  | grep -e "href=\"https://prow\.k8s\.io/view/gs/kubernetes-ci-logs/pr-logs/pull"
```

Extract the links. Pick the first; call it `BASE_PROW_LOG`. Translate `prow.k8s.io/view/...` to `gcsweb.k8s.io/gcs/...` for raw artifact access:

```sh
BASE_PROW_LOG=https://gcsweb.k8s.io/gcs/kubernetes-ci-logs/pr-logs/pull/kubernetes-sigs_kueue/9617/pull-kueue-test-e2e-main-1-33/2028383367677874176
```

From here on, put downloaded artifacts under `build-logs/failed/` (and `build-logs/success/` if you use strategy 2, see 1.5).

#### 1.2 Download the failed build-log

```sh
mkdir -p build-logs/failed
curl -sL ${BASE_PROW_LOG}/build-log.txt -o build-logs/failed/build-log.txt
```

#### 1.3 Locate the failure block and report it

Grep around the failing lines (remembering to strip ANSI codes):

```sh
sed 's/\x1b\[[0-9;]*[a-zA-Z]//g' build-logs/failed/build-log.txt | grep -nE "FAILED|Timed out after" | head -20
```

Output the lines and report: the failure message, the failing test name, the file:line in the test code, the namespace name used by that test, and the timestamp of failure.

**Decide strategy here.** Re-read the "When to use each" table above against the failure you just located:
- If the failure has the signatures of a deterministic bug, plan on **strategy 1 only** — you can skip 1.5 and 1.6 below, and skip step 6 later.
- If it has flake signatures (`Timed out`, intermittent, `Eventually`-based), plan on **strategy 1 + strategy 2** — do 1.5 and 1.6 now so the comparison data is ready when you reach step 6.

State your chosen strategy explicitly in your analysis.

#### 1.4 Determine test type (integration vs e2e)

This shapes which logs you need. Read the job name embedded in the Prow URL:

| Job name pattern | Type | Where the controller logs live |
|---|---|---|
| `pull-kueue-test-integration-*` | **Integration** | All in `build-log.txt` (Kueue + envtest run in-process inside the test binary) |
| `pull-kueue-test-e2e-*` | **E2E** | Per-pod logs under `${BASE_PROW_LOG}/artifacts/.../kind-worker[2]/pods/<ns>_<pod>/<container>/0.log` |

**For integration tests, you can skip the per-pod artifact downloads in steps 3–4** — `build-log.txt` already contains the kueue scheduler cycles, workload reconciler events, requeue worker activity, etc. The `artifacts/` directory only contains per-test-case JUnit XMLs whose `<system-err>` is the same content sliced per test.

**For E2E tests, `build-log.txt` only contains Ginkgo test output** — the actual controller events are in pods on the kind cluster.

State the test type up front in your analysis; it changes everything downstream.

#### 1.5 (Strategy 2 only) Find a recent successful run for comparison

Query the Prow job history JSON and pick the most recent SUCCESS for the same job. Walk down up to 3 attempts in case the most recent successful run doesn't include the failing test (e.g., the test was new in the failing PR).

```sh
JOB_NAME=$(echo "$BASE_PROW_LOG" | grep -oE 'pull-kueue-test-[a-zA-Z0-9-]+')
curl -sL "https://prow.k8s.io/job-history/gs/kubernetes-ci-logs/pr-logs/directory/${JOB_NAME}" \
  | grep -oE 'allBuilds = \[.*\];' \
  | sed 's/^allBuilds = //; s/;$//' \
  | python3 -c "
import json, sys
builds = json.load(sys.stdin)
successes = [b for b in builds if b['Result'] == 'SUCCESS']
for b in successes[:3]:
    print(b['SpyglassLink'])
"
```

The `SpyglassLink` is of the form `/view/gs/kubernetes-ci-logs/pr-logs/pull/.../<jobname>/<buildid>`. Convert to a `gcsweb.k8s.io/gcs/...` URL for downloading; call it `BASE_PROW_LOG_SUCCESS`.

If the user provides a specific successful Prow URL, use that instead of auto-discovery.

#### 1.6 (Strategy 2 only) Download the success build-log

```sh
mkdir -p build-logs/success
curl -sL ${BASE_PROW_LOG_SUCCESS}/build-log.txt -o build-logs/success/build-log.txt
```

Confirm the same failing test ran in the success build:

```sh
sed 's/\x1b\[[0-9;]*[a-zA-Z]//g' build-logs/success/build-log.txt | grep "<exact failing test name>" | head -5
```

If the test isn't present in the most recent SUCCESS, try the next one. If after 3 attempts the test isn't present in any successful run, say so explicitly — the test may have been introduced in the failing PR, in which case strategy 2 is not available and you must rely on strategy 1 alone.

### Step 2 — Identify the artifacts location with worker and control-plane Pods

#### 2.1 Find FAILED_SUITE and BASE_ARTIFACTS

Fetch `${BASE_PROW_LOG}/artifacts/`. You will find a URL with a suffix like `FAILED_SUITE=run-test-e2e-singlecluster-1.33.7`. Identify the `BASE_ARTIFACTS` directory:

```sh
BASE_ARTIFACTS=${BASE_PROW_LOG}/artifacts/${FAILED_SUITE}
```

Example:
`BASE_ARTIFACTS=https://gcsweb.k8s.io/gcs/kubernetes-ci-logs/pr-logs/pull/kubernetes-sigs_kueue/9617/pull-kueue-test-e2e-main-1-33/2028383367677874176/artifacts/run-test-e2e-singlecluster-1.33.7`

*Tip: When curling GCS bucket directories, ensure you include the trailing slash to receive an HTML directory listing, which you can then parse using `grep href` to find the exact subdirectory.*

#### 2.2 Where to save things on local disk

Everything for the failing run lives under `build-logs/failed/`; everything for the successful run (if using strategy 2) lives under `build-logs/success/`. Mirror the subdirectory structure across the two so a side-by-side diff is straightforward later:

```
build-logs/
├── failed/
│   ├── build-log.txt
│   ├── kueue-w1/0.log
│   ├── kueue-w2/0.log
│   ├── lws-w1/0.log
│   └── ...
└── success/
    ├── build-log.txt
    ├── kueue-w1/0.log
    └── ...
```

### Step 3 — Identify names of control-plane Pods

Identify the location of the relevant Pods. Check what the control-plane Pods are by fetching `${BASE_ARTIFACTS}/kind-control-plane/pods/`.

There you will find names for the control-plane logs. For example, for kube-scheduler you may find `KUBE_SCHEDULER_POD=kube-system_scheduler-kind-control-plane_3d2cfc9019893b83637280170b3fd08f`.

For controller pods (kueue-controller-manager, lws-controller-manager, jobset-controller-manager, etc.), check the worker nodes — they do not typically run on the control-plane node:

```sh
for w in kind-worker kind-worker2; do
  curl -sL "${BASE_ARTIFACTS}/${w}/pods/" \
    | grep -oE 'href="[^"]+(kueue-system|lws-system|jobset-system)[^"]+"'
done
```

### Step 4 — Analyze kube-scheduler logs

#### 4.1 Download kube-scheduler logs (E2E only)

For E2E runs only. Fetch the kube-scheduler log:

```sh
KUBE_SCHEDULER_LOGS=${BASE_ARTIFACTS}/kind-control-plane/pods/${KUBE_SCHEDULER_POD}/kube-scheduler/
```

You will find the list of files, for example `0.log`. The final kube-scheduler log might look like `${KUBE_SCHEDULER_LOGS}/0.log`.

#### 4.2 Find placement of relevant Pods

Now identify the placement of relevant Pods:

1. Find placement of the kueue-controller-manager Pods around the test failure.
2. Find the placement of Pods by the namespace corresponding to the failed test.

You are looking for text anchors like `"Successfully bound pod to node"`, for example:
`2026-03-02T08:28:59.236232048Z stderr F I0302 08:28:59.235983       1 schedule_one.go:314] "Successfully bound pod to node" pod="kueue-system/kueue-controller-manager-787d6db649-sfhqv" node="kind-worker2" evaluatedNodes=3 feasibleNodes=2` means the Kueue pod was placed on kind-worker2.

#### 4.3 Download controller manager logs (E2E only)

Kueue-controller-manager typically runs as an HA Deployment with 2 replicas — download both, the bigger log is usually the leader. Do the same for any other controller relevant to the test (lws-controller-manager for LeaderWorkerSet tests, jobset-controller for JobSet, etc.):

```sh
mkdir -p build-logs/failed/kueue-w1 build-logs/failed/kueue-w2 build-logs/failed/lws-w1   # adjust per controller
curl -sL "${BASE_ARTIFACTS}/kind-worker/pods/<kueue-pod>/manager/0.log" \
  -o build-logs/failed/kueue-w1/0.log
# ...and so on for each replica/controller on each worker
```

If you're using strategy 2, do the same under `build-logs/success/` from `BASE_PROW_LOG_SUCCESS`'s artifacts.

### Step 5 — Analyze kubelet logs

Once you know the placement of all relevant Pods, check the kubelet logs for each. For example, the link for the kubelet on `KIND_WORKER=kind-worker2` might be found at:

`${BASE_ARTIFACTS}/${KIND_WORKER}/kubelet.log`

It is useful to check kubelet logs from all workers, so often also `kind-worker2` etc.

Summarize what you find relevant in the kubelet logs.

*Tip: Kubelet logs are notoriously noisy. To find relevant signals, `grep` explicitly for the namespace, pod name, or pod UID discovered in the previous steps.*

#### 5.1 Read the failing test code

Read the test code around the failure. For example, if the failed test indicates `/home/prow/go/src/sigs.k8s.io/kueue/test/e2e/singlecluster/job_test.go:535`, read `test/e2e/singlecluster/job_test.go` around the failed line.

Read the file from the right branch — the failure may be on `release-0.X`, not `main`. Read at least 100 lines before the failed line to capture `BeforeEach`, `BeforeAll`, and the test's setup variables.

For each `gomega.Eventually` near the failure, note:
- The timeout (`util.Timeout` = 10s, `MediumTimeout` = 45s, `LongTimeout` = 90s, `VeryLongTimeout` = 5m)
- The interval (`util.Interval` = 250ms, `ShortInterval` = 10ms)
- The exact predicate (what is being polled — a Get, a metric, a status condition)

This determines what timing of state changes the test would actually catch.

Summarize your findings.

### Step 6 — (Strategy 2 only) Compare failed vs successful runs

**Skip this step if you chose strategy 1 only in step 1.3.**

The same test in the success run is your reference: it tells you what *should* have happened. Compare both runs along three dimensions.

#### 6.1 Dimension 1: STEP timings

In Ginkgo build-log output, every `ginkgo.By(...)` produces a `STEP: <description> @ <timestamp>` line. List the STEPs of the failing test from both runs side by side. The step *just before* the failure, and its time delta to the previous step, is usually where the divergence lives.

```sh
sed 's/\x1b\[[0-9;]*[a-zA-Z]//g' build-logs/failed/build-log.txt   | grep -E "STEP:" | grep "<test or namespace>" > /tmp/steps-failed.txt
sed 's/\x1b\[[0-9;]*[a-zA-Z]//g' build-logs/success/build-log.txt  | grep -E "STEP:" | grep "<test or namespace>" > /tmp/steps-success.txt
diff /tmp/steps-failed.txt /tmp/steps-success.txt
```

#### 6.2 Dimension 2: Controller events for the failing test's namespace

Filter both controller logs (or both build-logs, for integration tests) by the test namespace and the relevant object names. Look for differences in:
- Number of events
- Reasons / status values
- Time gaps (a 9-second silence on one side often means a wedged loop)
- Sequence of events (e.g., create → admit → delete vs. create → admit → ... still admitted)

#### 6.3 Dimension 3: Resource lifecycle

Trace the specific objects (workloads, pods, LWS, etc.) named in the failure message through both runs. When was each created, admitted, updated, deleted? A failing run often shows an object that exists briefly (or not at all) where the successful run shows it persisting (or vice versa).

#### 6.4 Produce a side-by-side findings table

One row per dimension; columns for `Failed run`, `Successful run`, `Notable difference`. Anchor every cell with a timestamp and a file path (link the build-log or pod log directly).

If the focused comparison turns up nothing useful, widen the scope: include BeforeSuite/BeforeAll logs, the entire scheduler cycle history, kubelet events. Do this only when the focused view isn't conclusive — the broad view is noisy.

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
