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
  | rg -o 'https://prow\.k8s\.io/view/gs/kubernetes-ci-logs/pr-logs/pull[^" ]+'
```

List all matching builds. Choose the relevant failed build and preserve its exact artifact base
path from the Prow page; do not reconstruct the PR-number path by hand.

```sh
PROW_BUILD='https://prow.k8s.io/view/gs/kubernetes-ci-logs/pr-logs/pull/.../.../...'
GCS_BUILD='https://gcsweb.k8s.io/gcs/kubernetes-ci-logs/pr-logs/pull/.../.../...'
mkdir -p build-logs
curl -fsSL "${GCS_BUILD}/build-log.txt" -o build-logs/build-log.txt
curl -fsSL "${GCS_BUILD}/podinfo.json" -o build-logs/podinfo.json
curl -fsSL "${GCS_BUILD}/prowjob.json" -o build-logs/prowjob.json
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

### Step 2 - inspect the CI Pod and distinguish OOM from ordinary failure

`podinfo.json` is the source of truth for the test container resource limit and termination reason:

```sh
jq '.pod.metadata | {name,namespace,creationTimestamp}' build-logs/podinfo.json
jq '.pod.spec.containers[] | {name,resources}' build-logs/podinfo.json
jq '.pod.status.containerStatuses[] |
  {name,reason:.state.terminated.reason,exitCode:.state.terminated.exitCode,
   startedAt:.state.terminated.startedAt,finishedAt:.state.terminated.finishedAt}' \
  build-logs/podinfo.json
```

Interpret the result as follows:

- `reason: OOMKilled` is direct Pod-level evidence of an OOM kill.
- `container_oom_events_total` increasing is direct cAdvisor evidence of an OOM event.
- `reason: Error` plus no OOM events means the process failed, but does not prove why.
- A memory peak near the limit proves resource pressure, not an OOM kill.
- Use the `test` container value and its limit. Do not compare the aggregate Pod working set with a
  single container limit without accounting for sidecars and pause containers.

### Step 3 - query Prow Prometheus for resource evidence

The Grafana UI at `https://monitoring-eks.prow.k8s.io` may require login. The Prow datasource
proxy API can expose the required public metrics without using the UI. The datasource UID used by
the repository's metrics helper is `PA553F4D380FC2FA5`; verify it in
`hack/infra/stats/fetch_prow_metrics.py` if the monitoring setup changes. Do not bypass
authentication if the endpoint changes or becomes private.

Set the Pod name from `podinfo.json` and use a time range covering the test container lifetime:

```sh
PROM_API='https://monitoring-eks.prow.k8s.io/api/datasources/proxy/uid/PA553F4D380FC2FA5/api/v1'
POD=$(jq -r '.pod.metadata.name' build-logs/podinfo.json)
START=START_UNIX_TIME
END=END_UNIX_TIME

curl -fsS --get "${PROM_API}/query_range" \
  --data-urlencode "query=container_memory_working_set_bytes{namespace=\"test-pods\",pod=\"${POD}\",container=\"test\"}" \
  --data-urlencode "start=${START}" \
  --data-urlencode "end=${END}" \
  --data-urlencode 'step=30' \
  -o build-logs/memory.json

curl -fsS --get "${PROM_API}/query_range" \
  --data-urlencode "query=container_oom_events_total{namespace=\"test-pods\",pod=\"${POD}\",container=\"test\"}" \
  --data-urlencode "start=${START}" \
  --data-urlencode "end=${END}" \
  --data-urlencode 'step=30' \
  -o build-logs/oom.json

curl -fsS --get "${PROM_API}/query_range" \
  --data-urlencode "query=rate(container_cpu_usage_seconds_total{namespace=\"test-pods\",pod=\"${POD}\",container=\"test\"}[2m])" \
  --data-urlencode "start=${START}" \
  --data-urlencode "end=${END}" \
  --data-urlencode 'step=30' \
  -o build-logs/cpu.json
```

Extract the relevant values:

```sh
jq '[.data.result[]?.values[]?[1] | tonumber] | {max:(max // 0)}' build-logs/memory.json
jq '[.data.result[]?.values[]?[1] | tonumber] | {max:(max // 0)}' build-logs/oom.json
jq '[.data.result[]?.values[]?[1] | tonumber] | {max:(max // 0)}' build-logs/cpu.json
```

Record the units explicitly. For example, `12,871,102,464` bytes is about `11.987 GiB`, while a
`12 GiB` limit is `12,884,901,888` bytes. A result at 99% of the limit supports a resource-pressure
hypothesis but does not establish an OOM kill.

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
follow-up CI run, use a positive level such as:

```sh
INTEGRATION_API_LOG_LEVEL=2 make test-multikueue-integration
```

Do not treat the absence of server logs as proof of OOM. It is an evidence gap. Capture apiserver
and etcd logs before deciding whether to add retries or change teardown behavior.

### Step 6 - analyze control-plane and node artifacts

For kind or e2e artifacts, list the artifact directory with a trailing slash and locate the control
plane and worker logs:

```sh
curl -fsSL "${GCS_BUILD}/artifacts/" | rg 'href='
```

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
2. If memory is near the limit but no OOM is observed, report resource pressure and consider adding
   headroom. Do not label it a confirmed OOM.
3. If an apiserver stop timeout is primary and resource pressure is plausible, inspect teardown
   ordering and parallel envtest ownership.
4. If resource usage is normal, prioritize apiserver/etcd logs and process lifecycle evidence.
5. Add retries only when the error is transient and the apiserver remains healthy; do not use retries
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
