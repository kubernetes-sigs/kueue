---
name: kueue-flake-debugger
description: Debug Kueue CI flakes. Use when a user asks to debug a flake, investigate a test failure, a test timeout, or a CI flake. Analyzes Prow build logs, CI Pod resources, Prometheus metrics, envtest lifecycle, kube-scheduler logs, kubelet logs, and test code to identify root causes.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

You are an expert in Kueue, the Kubernetes workload orchestration system.

## Flake debugging

Follow the steps below in order. Keep confirmed facts separate from hypotheses. Do not call a
failure an OOM unless the Pod status or OOM metrics support that conclusion.

### Step 1 - identify the Prow build and download the primary log

When given a GitHub issue, first collect all Prow links from the issue and its comments:

```sh
gh issue view ISSUE --repo kubernetes-sigs/kueue --comments --json comments,url
```

If `gh` is unavailable, use the public issue page:

```sh
curl -fsSL https://github.com/kubernetes-sigs/kueue/issues/ISSUE \
  | rg -o 'https://prow\.k8s\.io/view/gs/kubernetes-ci-logs/(pr-logs/pull|logs)/[^" ]+'
```

List all matching builds. Choose the relevant failed build and preserve its exact artifact base
path from the Prow page; do not reconstruct the PR-number path by hand.

```sh
PROW_BUILD='https://prow.k8s.io/view/gs/kubernetes-ci-logs/pr-logs/pull/.../.../...'
GCS_BUILD="${PROW_BUILD/https:\/\/prow.k8s.io\/view\/gs\//https:\/\/gcsweb.k8s.io\/gcs\/}"
mkdir -p build-logs
curl -fsSL "${GCS_BUILD}/build-log.txt" -o build-logs/build-log.txt
if ! curl -fsSL "${GCS_BUILD}/podinfo.json" -o build-logs/podinfo.json; then
  printf '%s\n' 'podinfo.json is unavailable; skip Pod-specific checks.'
fi
if ! curl -fsSL "${GCS_BUILD}/prowjob.json" -o build-logs/prowjob.json; then
  printf '%s\n' 'prowjob.json is unavailable; continue with the available artifacts.'
fi
```

Search around the failure and report the failing spec, source location, and exact error:

```sh
rg -n -i -C 30 'FAILED|failure|error|timeout|INTERNAL_ERROR|OOM|killed|panic' \
  build-logs/build-log.txt
```

The raw artifact host is usually also available as:

```text
https://storage.googleapis.com/kubernetes-ci-logs/<same-path>/build-log.txt
```

Use the `gcsweb` listing when discovering artifact names and the `storage.googleapis.com` URL when
downloading a raw file. Keep downloaded artifacts under `build-logs/`.

Determine the test tier from the Prow job name, target, and build log before following the later
steps:

- Unit test: for an assertion-only failure, skip Steps 2-6 and continue with Steps 7-9. For a
  process or container failure, run Steps 2-3 first, then continue with Steps 7-9.
- Integration or envtest, including `test-performance-scheduler`,
  `test-tas-performance-scheduler`, `test-tas-dra-performance-scheduler`, and
  `test-large-scale-performance-scheduler`: run Steps 2-5, skip Step 6, then continue with Steps 7-9.
- E2E or Kind: run Steps 2-3, skip Steps 4-5, run Step 6, then continue with Steps 7-9.

### Step 2 - inspect the CI Pod and distinguish OOM from ordinary failure

When `build-logs/podinfo.json` is available, use it as the source of truth for the test container
resource limit and termination reason. If it is unavailable, skip this step and report that the Pod
metadata is missing.

```sh
if test -s build-logs/podinfo.json; then
  jq '.pod.metadata | {name,namespace,creationTimestamp}' build-logs/podinfo.json
  jq '.pod.spec.containers[] | {name,resources}' build-logs/podinfo.json
  jq '.pod.status.containerStatuses[] |
    {name,reason:.state.terminated.reason,exitCode:.state.terminated.exitCode,
     startedAt:.state.terminated.startedAt,finishedAt:.state.terminated.finishedAt}' \
    build-logs/podinfo.json
else
  printf '%s\n' 'podinfo.json is unavailable; skip Pod-specific checks.'
fi
```

Interpret the result as follows:

- `reason: OOMKilled` is direct Pod-level evidence of an OOM kill.
- `container_oom_events_total` increasing is direct cAdvisor evidence of an OOM event.
- `reason: Error` plus no OOM events means the process failed, but does not prove why.
- A memory peak near the limit proves resource pressure, not an OOM kill.
- Use the `test` container value and its limit. Do not compare the aggregate Pod working set with a
  single container limit without accounting for sidecars and pause containers.

### Step 3 - query Prow Prometheus for resource evidence

When `build-logs/podinfo.json` is available, read and follow
[Prometheus resource queries](references/prometheus-resource-queries.md). Discover the datasource
UID and query memory, OOM events, CPU usage, and the CFS throttled-period ratio for the test
container lifetime. Keep query results under `build-logs/`; missing samples are an evidence gap.

### Step 4 - inspect envtest parallelism and lifecycle

For an envtest or apiserver failure, inspect the test command and suite setup:

```sh
rg -n 'INTEGRATION_NPROCS|--procs|BeforeSuite|AfterSuite|SynchronizedBeforeSuite|SynchronizedAfterSuite' \
  hack/make test test/integration
```

Determine how many control planes exist at the same time. Ginkgo runs `BeforeSuite` in every
parallel process unless the suite uses synchronized suite callbacks. Multiply the number of
processes by the number of envtest clusters created in `BeforeSuite`.

For each cluster, inspect the manager stop and envtest teardown path. A typical lifecycle is:

```text
cancel manager context
wait for manager
cancel client context
envtest.Stop()
wait for kube-apiserver and etcd
```

An `[AfterSuite] timeout waiting for process kube-apiserver to stop` is a teardown/lifecycle
symptom. It does not by itself identify whether the server was blocked by memory pressure, an etcd
dependency, a process leak, or another control-plane failure.

### Step 5 - enable and collect apiserver logs when they are missing

Check whether CI disables envtest apiserver output:

```sh
rg -n 'INTEGRATION_API_LOG_LEVEL|API_LOG_LEVEL|GetAPIServer\(\).*Out|GetAPIServer\(\).*Err' \
  hack/make/test.mk test/integration/framework
```

If the log level is `0`, apiserver stdout/stderr is not included in the test log. For a reproducer or
follow-up integration run, set the failing Make target explicitly and use a positive log level:

```sh
FAILING_TEST_TARGET=test-integration
INTEGRATION_API_LOG_LEVEL=2 make "${FAILING_TEST_TARGET}"
# For example: FAILING_TEST_TARGET=test-multikueue-integration
```

Do not treat the absence of server logs as proof of OOM. It is an evidence gap. Capture apiserver
and etcd logs before deciding whether to add retries or change teardown behavior.

### Step 6 - analyze control-plane and node artifacts

For kind or e2e artifacts, list the artifact directory with a trailing slash and locate the control
plane and worker logs:

```sh
curl -fsSL "${GCS_BUILD}/artifacts/" | rg 'href='
```

For example, single-cluster artifacts use `artifacts/run-test-e2e-<suite>-<k8s-version>/`.
Set `BASE_ARTIFACTS` to the actual suite directory URL from the listing. With the default Kind
cluster name, typical log paths relative to that directory are:

```text
kind-control-plane/pods/kube-system_kube-scheduler-*/kube-scheduler/0.log
<worker>/kubelet.log
```

List `${BASE_ARTIFACTS}/kind-control-plane/pods/` to find the exact scheduler Pod directory, then
download its `kube-scheduler/0.log`. Worker names include `kind-worker` and `kind-worker2`.
Use the actual cluster and node names from the listing for custom or MultiKueue clusters.

For scheduler logs, locate the Pod directory and inspect the relevant `0.log` file. Look for the
placement of Kueue controller Pods and Pods in the failed test namespace:

```text
Successfully bound pod to node
Failed to bind pod
Preempted
Insufficient memory
```

For kubelet logs, inspect every worker that hosted a relevant Pod. Search by namespace, Pod name,
Pod UID, `OOM`, `evict`, `pressure`, `failed`, and `cgroup` rather than reading the entire noisy log.

### Step 7 - match the failure to test and framework code

Read the test source around the reported line, including at least 100 lines before the failure to
understand `BeforeEach`, `BeforeSuite`, cleanup callbacks, contexts, and `Eventually` timeouts.

For controller-runtime or envtest failures, also inspect the framework helpers that create managers,
clients, contexts, and control planes. Cite the exact source lines in the final report.

Treat these errors differently:

- A failed assertion is the primary test failure.
- `Eventually` returning an apiserver `INTERNAL_ERROR` is an API availability failure.
- `http2: client connection lost` during suite teardown is often a secondary symptom.
- A normal `context canceled` from watches during teardown is expected and is not sufficient evidence
  of the original failure.

### Step 8 - reason about retries and resource changes

Check whether the failing operation is already inside `Eventually` or another retry loop. Retrying a
read does not repair an apiserver that is stopping or an envtest process that cannot exit.

Use this decision order:

1. If `OOMKilled` or an increasing OOM counter is present, fix the memory limit or workload shape.
2. If memory is near the limit but no OOM is observed, report resource pressure and recommend
   increasing the container limit. Do not label it a confirmed OOM.
3. If CPU usage is near its limit and the CFS throttled-period ratio is high, inspect the CPU quota
   and test parallelism. Evaluate a CPU limit or parallelism change without treating throttling as
   a confirmed cause of the failure.
4. If an apiserver stop timeout is primary and resource pressure is plausible, inspect teardown
   ordering and parallel envtest ownership.
5. If resource usage is normal, prioritize apiserver/etcd logs and process lifecycle evidence.
6. Add retries only when the error is transient and the apiserver remains healthy; do not use retries
   to hide teardown failures.

### Step 9 - recommendations and final report

Separate the final report into three categories:

```text
Confirmed facts:
- exact Prow build and failing log line
- Pod resource limits and termination reason
- observed memory/CPU/OOM metric values
- relevant test and framework code paths

Likely contributors:
- resource pressure near the container limit
- too many envtest control planes per parallel process
- teardown ordering or ownership across Ginkgo processes

Not confirmed:
- an OOM kill without OOMKilled status or OOM metrics
- the exact apiserver-side cause without apiserver/etcd logs
```

Every conclusion must cite its evidence: the Prow build log, `podinfo.json`, Prometheus query
results, and source code paths. Recommend the smallest evidence-backed fix first, then list longer
term lifecycle changes separately.
