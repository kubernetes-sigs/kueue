# Gathering artifacts (Steps 1–5)

The mechanical commands for locating a Prow run, downloading logs, and reading the
failing test. Driven by [SKILL.md](../SKILL.md); start there for strategy selection.

**Universal precaution:** when grepping any build-log or controller log, **strip ANSI
color codes first** or grep will silently miss matches:

```sh
perl -pe 's/\e\[[0-9;]*[a-zA-Z]//g' build-logs/failed/build-log.txt | grep "your-term"
```

## Step 1 — Initial build-log analysis

### 1.1 Identify the failed Prow run

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

### 1.2 Download the failed build-log

```sh
mkdir -p build-logs/failed
curl -sL ${BASE_PROW_LOG}/build-log.txt -o build-logs/failed/build-log.txt
```

### 1.3 Locate the failure block and report it

Grep around the failing lines (remembering to strip ANSI codes):

```sh
perl -pe 's/\e\[[0-9;]*[a-zA-Z]//g' build-logs/failed/build-log.txt | grep -nE "FAILED|Timed out after" | head -20
```

Output the lines and report: the failure message, the failing test name, the file:line in the test code, the namespace name used by that test, and the timestamp of failure.

**Decide strategy here.** Re-read the "When to use each" table in [SKILL.md](../SKILL.md) against the failure you just located:
- If the failure has the signatures of a deterministic bug, plan on **strategy 1 only** — you can skip 1.5 and 1.6 below, and skip step 6 later.
- If it has flake signatures (`Timed out`, intermittent, `Eventually`-based), plan on **strategy 1 + strategy 2** — do 1.5 and 1.6 now so the comparison data is ready when you reach step 6.

State your chosen strategy explicitly in your analysis.

### 1.4 Determine test type (integration vs e2e)

This shapes which logs you need. Read the job name embedded in the Prow URL:

| Job name pattern | Type | Where the controller logs live |
|---|---|---|
| `pull-kueue-test-integration-*` | **Integration** | All in `build-log.txt` (Kueue + envtest run in-process inside the test binary) |
| `pull-kueue-test-e2e-*` | **E2E** | Per-pod logs under `${BASE_PROW_LOG}/artifacts/.../kind-worker[2]/pods/<ns>_<pod>/<container>/0.log` |

**For integration tests, you can skip the per-pod artifact downloads in steps 3–4** — `build-log.txt` already contains the kueue scheduler cycles, workload reconciler events, requeue worker activity, etc. The `artifacts/` directory only contains per-test-case JUnit XMLs whose `<system-err>` is the same content sliced per test.

**For E2E tests, `build-log.txt` only contains Ginkgo test output** — the actual controller events are in pods on the kind cluster.

State the test type up front in your analysis; it changes everything downstream.

### 1.5 (Strategy 2 only) Find a recent successful run for comparison

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

### 1.6 (Strategy 2 only) Download the success build-log

```sh
mkdir -p build-logs/success
curl -sL ${BASE_PROW_LOG_SUCCESS}/build-log.txt -o build-logs/success/build-log.txt
```

Confirm the same failing test ran in the success build:

```sh
perl -pe 's/\e\[[0-9;]*[a-zA-Z]//g' build-logs/success/build-log.txt | grep "<exact failing test name>" | head -5
```

If the test isn't present in the most recent SUCCESS, try the next one. If after 3 attempts the test isn't present in any successful run, say so explicitly — the test may have been introduced in the failing PR, in which case strategy 2 is not available and you must rely on strategy 1 alone.

## Step 2 — Identify the artifacts location with worker and control-plane Pods

### 2.1 Find FAILED_SUITE and BASE_ARTIFACTS

Fetch `${BASE_PROW_LOG}/artifacts/`. You will find a URL with a suffix like `FAILED_SUITE=run-test-e2e-singlecluster-1.33.7`. Identify the `BASE_ARTIFACTS` directory:

```sh
BASE_ARTIFACTS=${BASE_PROW_LOG}/artifacts/${FAILED_SUITE}
```

Example:
`BASE_ARTIFACTS=https://gcsweb.k8s.io/gcs/kubernetes-ci-logs/pr-logs/pull/kubernetes-sigs_kueue/9617/pull-kueue-test-e2e-main-1-33/2028383367677874176/artifacts/run-test-e2e-singlecluster-1.33.7`

*Tip: When curling GCS bucket directories, ensure you include the trailing slash to receive an HTML directory listing, which you can then parse using `grep href` to find the exact subdirectory.*

### 2.2 Where to save things on local disk

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

## Step 3 — Identify names of control-plane Pods

Identify the location of the relevant Pods. Check what the control-plane Pods are by fetching `${BASE_ARTIFACTS}/kind-control-plane/pods/`.

There you will find names for the control-plane logs. For example, for kube-scheduler you may find `KUBE_SCHEDULER_POD=kube-system_scheduler-kind-control-plane_3d2cfc9019893b83637280170b3fd08f`.

For controller pods (kueue-controller-manager, lws-controller-manager, jobset-controller-manager, etc.), check the worker nodes — they do not typically run on the control-plane node:

```sh
for w in kind-worker kind-worker2; do
  curl -sL "${BASE_ARTIFACTS}/${w}/pods/" \
    | grep -oE 'href="[^"]+(kueue-system|lws-system|jobset-system)[^"]+"'
done
```

## Step 4 — Analyze kube-scheduler logs

### 4.1 Download kube-scheduler logs (E2E only)

For E2E runs only. Fetch the kube-scheduler log:

```sh
KUBE_SCHEDULER_LOGS=${BASE_ARTIFACTS}/kind-control-plane/pods/${KUBE_SCHEDULER_POD}/kube-scheduler/
```

You will find the list of files, for example `0.log`. The final kube-scheduler log might look like `${KUBE_SCHEDULER_LOGS}/0.log`.

### 4.2 Find placement of relevant Pods

Now identify the placement of relevant Pods:

1. Find placement of the kueue-controller-manager Pods around the test failure.
2. Find the placement of Pods by the namespace corresponding to the failed test.

You are looking for text anchors like `"Successfully bound pod to node"`, for example:
`2026-03-02T08:28:59.236232048Z stderr F I0302 08:28:59.235983       1 schedule_one.go:314] "Successfully bound pod to node" pod="kueue-system/kueue-controller-manager-787d6db649-sfhqv" node="kind-worker2" evaluatedNodes=3 feasibleNodes=2` means the Kueue pod was placed on kind-worker2.

### 4.3 Download controller manager logs (E2E only)

Kueue-controller-manager typically runs as an HA Deployment with 2 replicas — download both, the bigger log is usually the leader. Do the same for any other controller relevant to the test (lws-controller-manager for LeaderWorkerSet tests, jobset-controller for JobSet, etc.):

```sh
mkdir -p build-logs/failed/kueue-w1 build-logs/failed/kueue-w2 build-logs/failed/lws-w1   # adjust per controller
curl -sL "${BASE_ARTIFACTS}/kind-worker/pods/<kueue-pod>/manager/0.log" \
  -o build-logs/failed/kueue-w1/0.log
# ...and so on for each replica/controller on each worker
```

If you're using strategy 2, do the same under `build-logs/success/` from `BASE_PROW_LOG_SUCCESS`'s artifacts.

## Step 5 — Analyze kubelet logs

Once you know the placement of all relevant Pods, check the kubelet logs for each. For example, the link for the kubelet on `KIND_WORKER=kind-worker2` might be found at:

`${BASE_ARTIFACTS}/${KIND_WORKER}/kubelet.log`

It is useful to check kubelet logs from all workers, so often also `kind-worker2` etc.

Summarize what you find relevant in the kubelet logs.

*Tip: Kubelet logs are notoriously noisy. To find relevant signals, `grep` explicitly for the namespace, pod name, or pod UID discovered in the previous steps.*

### 5.1 Read the failing test code

Read the test code around the failure. For example, if the failed test indicates `/home/prow/go/src/sigs.k8s.io/kueue/test/e2e/singlecluster/job_test.go:535`, read `test/e2e/singlecluster/job_test.go` around the failed line.

Read the file from the right branch — the failure may be on `release-0.X`, not `main`. Read at least 100 lines before the failed line to capture `BeforeEach`, `BeforeAll`, and the test's setup variables.

For each `gomega.Eventually` near the failure, note:
- The timeout (`util.Timeout` = 10s, `MediumTimeout` = 45s, `LongTimeout` = 90s, `VeryLongTimeout` = 5m)
- The interval (`util.Interval` = 250ms, `ShortInterval` = 10ms)
- The exact predicate (what is being polled — a Get, a metric, a status condition)

This determines what timing of state changes the test would actually catch.

Summarize your findings.
