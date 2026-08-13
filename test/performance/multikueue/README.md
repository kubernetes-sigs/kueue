# MultiKueue performance benchmark

This benchmark measures the MultiKueue control-plane path across one manager
cluster and a configurable number of worker clusters. The baseline uses three
workers, matching the topology proposed in the tracking issue.

The runner starts isolated `envtest` control planes and runs the real Kueue
core scheduler and MultiKueue controllers in process. It creates a suspended
batch Job and its Workload on the manager for every sample. MultiKueue copies
each pair to the workers, the worker schedulers reserve quota, and the manager
admits the Workload after a worker is selected.

This boundary deliberately excludes Kubernetes Job and Pod execution. It
measures MultiKueue dispatch and admission rather than kube-controller-manager
or container runtime performance.

## Run the baseline

```bash
make run-performance-multikueue
```

The baseline creates all 120 workloads as fast as the manager accepts them and
then waits for the queue to drain, which takes about 95 seconds. The 10-minute
timeout in the configuration is a safety bound for a hung run, not a performance
threshold.

The summary is written to
`artifacts/run-performance-multikueue/summary.yaml`.

For a quick local smoke run:

```bash
make run-performance-multikueue MULTIKUEUE_PERFORMANCE_ARGS="--workloads=12 --creationWorkers=3"
```

The scenario is configured in
[`configs/baseline.yaml`](configs/baseline.yaml). Command-line overrides are
intended for smoke tests; committed baseline changes should be made in the
configuration file.

`workloadCount` must be between 1 and 10,000. The upper bound keeps the
runner's per-Workload observation state and watch handover buffer bounded while
retaining room for a 10k-scale scenario.

The runner uses the production defaults for MultiKueue garbage collection,
worker-loss detection, and remote-event batching. These values are recorded in
the summary so a comparison cannot silently mix controller configurations.

Controller logs are written to `runner.log` next to the summary, at error level
by default. Pass `--zap-log-level=debug` (or `info`) through
`MULTIKUEUE_PERFORMANCE_ARGS` to diagnose a run that fails part way through, at
the cost of perturbing the measurement. Expected reconcile conflicts are logged
as errors, so `runner.log` is rarely empty on a healthy run.

The runner's own unit tests live beside it and are excluded from `make test`
along with everything else under `./test/`, so they have a dedicated target:

```bash
make test-performance-multikueue-runner
```

The entrypoint intended for a dedicated periodic job runs those unit tests,
executes the full baseline, checks the result against
[`configs/baseline/rangespec.yaml`](configs/baseline/rangespec.yaml), and
retries once in the same way as the scheduler performance tests:

```bash
make test-performance-multikueue
```

The committed ranges are initial guardrails for large regressions. They must be
recalibrated from at least five runs on the dedicated CI worker before TestGrid
alerting is enabled.

For this target, the summary is written to
`artifacts/test-performance-multikueue/run-performance-multikueue/summary.yaml`.

## What bounds the measurement

Two client-side rate limits bound this benchmark, and one of them is always the
binding constraint. Neither is an artifact of the harness: both are what a
deployed Kueue runs with.

The manager's own client uses a single shared token bucket configured by
`clientConnection`, defaulting to 300 QPS and burst 500. `cmd/kueue/main.go`
installs that limiter explicitly, because otherwise controller-runtime would give
each API type its own.

MultiKueue's worker-cluster clients get no such treatment. `clustersReconciler`
builds one controller-runtime client per worker from a kubeconfig Secret or a
ClusterProfile, and that `rest.Config` carries no QPS, burst, or shared
`RateLimiter`. controller-runtime lazily creates an underlying REST client per
GVK, and each of those falls back to client-go's defaults of 5 QPS and burst 10.
So the remote side is not one 5 QPS budget per worker; it is 5 QPS per worker per
Kind.

Measured on a 32-core machine with three workers. The rows were taken at 120, 1000
and 500 workloads respectively, each sized to give the regime a run long enough to
reach steady state; once the load saturates a limiter, throughput barely moves with
the workload count, which the first row shows directly at 1.30/s for 1000 against
1.31/s for 120:

| remote QPS per REST client | manager QPS | throughput | what binds |
|---|---|---|---|
| 5 (client-go default) | 300 (Kueue default) | 1.31/s | the remote limiter |
| 300 | 300 (Kueue default) | 38.0/s | the manager's shared limiter |
| 300 | 3000 | 95.3/s | neither limiter |

The middle row is why the baseline leaves the remote clients alone. Lifting the
remote throttle does not expose MultiKueue's processing capacity; it moves the
constraint onto the manager's own limiter. Both regimes measure requests per
Workload against a fixed token rate and differ only in which request count they
respond to. Measuring at the remote default needs no production code and is the
configuration a deployed Kueue actually runs, so that is where the baseline sits.

The arithmetic behind each row says what a movement in the number means. At 300
manager QPS and 38.0 workloads/s, a Workload spends about 7.9 tokens on the
manager's shared bucket. At 5 remote QPS and 1.31 workloads/s, it spends about
3.8 tokens on the busiest remote Kind. Neither figure counts all requests: the
first excludes remote traffic entirely, and the second covers one Kind of it.

Because the remote limiter releases tokens at a fixed rate regardless of how fast
the host is, the baseline number is more portable across machines than a
processing measurement would be, and the throughput floor can be set tightly
enough to catch a single added remote request per Workload. What it cannot see is
a regression that does not change the busiest remote Kind's request count: CPU
work, lock contention, or extra traffic to some other Kind.

Nothing here asserts anything about the garbage collection or worker-loss paths.
Worker-loss detection cannot trigger at all, since its timeout is 15 minutes. The
garbage collector does run: its interval is one minute against a drain of about 95
seconds, so it fires once inside the measured window, listing Workloads on each
worker and reading the local copy of every one it finds. That is a small cost
against the run's token budget, but it is not zero, and where it lands relative to
the run's progress varies, so it contributes to the run-to-run spread.

## Reconcile concurrency

The runner sets `groupKindConcurrency` to match the MultiKueue e2e configuration,
including `Workload.kueue.x-k8s.io: 10`. This has to be set explicitly:
controller-runtime resolves each controller's concurrency from that map and
otherwise runs one reconcile at a time, an order of magnitude below any deployed
MultiKueue.

At the committed baseline this does not move the throughput, because the remote
limiter binds either way. It matters for fidelity, and it matters for any run
that lifts the limiter: with the remote clients at 300 QPS, raising concurrency
from one to ten took throughput from 24.8/s to 38.0/s. `workloadConcurrency` is
recorded in the summary and matched exactly by the checker.

## Reading the summary

Throughput is the headline number. On a 32-core machine the baseline reports
about 1.31 workloads/s; five consecutive runs spanned 1.289 to 1.335.

The runner submits workloads as fast as it can rather than at a fixed arrival
rate, so once the load saturates the dispatch path the latency percentiles
describe a workload's position in the drain queue rather than the cost of
dispatching it. They are still useful as a distribution shape and as a
same-scale comparison between revisions, but they must not be read as
per-workload service time, and they are only comparable across runs with an
identical `workloadCount`. In particular, `maxAdmissionP95Ms` in the range spec
tracks throughput rather than adding an independent signal; it is kept looser
than the throughput floor so that throughput stays the binding guard.

The summary also contains:

- manager quota-reservation and end-to-end admission latency, each as
  min/avg/P50/P95/P99/max. Quota reservation is manager-side scheduling and is
  the one measurement here that does not track dispatch throughput. Admission
  includes MultiKueue dispatch and the subsequent manager reconcile that admits
  the local Workload, so the gap is an end-to-end control-plane interval rather
  than pure MultiKueue service time;
- total and post-generation drain time;
- `watchGaps`, described below; and
- Go/platform metadata, plus authoritative source revision fields when the
  runner is built through the Make target that passes the build flags.

The loop receiving Workload events does nothing but timestamp them and hand them
to a buffer, because the API server terminates a watcher it cannot deliver events
to, and because a timestamp taken after the processing loop had queued an event
would inflate measured control-plane latency.

If the watch still ends before every workload is admitted, the runner resumes it
from the last resource version it saw and counts the gap in `watchGaps`. A
resumed watch replays what it missed, but those transitions are then timestamped
on arrival rather than when they happened. A gap can inflate latencies and
total/drain times, and can lower measured throughput when it delays the final
admission observation. The range spec tolerates one gap only while bounded
latency and throughput remain in range; total and drain timings are
observational. More than one gap fails. A watch error ends the run instead: it
usually means the resource version to resume from has expired, which no retry
recovers. No gap occurred in any calibration run.

`workerDistribution` records which worker won each workload. Under the
all-at-once dispatcher that is decided by whichever worker reserves quota
first, so on a single host it mostly reflects which `envtest` API server is
momentarily faster, and it is routinely lopsided. It is a sanity check that
every worker participates, not a fairness metric.

Latency starts immediately before the manager Workload API request and ends
when the corresponding state is first observed on the Workload watch. This
avoids the one-second serialization granularity of Kubernetes condition
timestamps while retaining the API visibility delay an external user sees.
Percentiles use the nearest-rank method.

## Rollout

The raw runner remains observational. The dedicated test target applies broad,
provisional guardrails, but there is no presubmit, periodic, or alert until a
job is added and calibrated on stable CI capacity. The intended rollout is:

1. merge the runner and regression-check target in this repository;
2. add a dedicated non-alerting periodic job in
   [`kubernetes/test-infra`](https://github.com/kubernetes/test-infra/blob/master/config/jobs/kubernetes-sigs/kueue/kueue-periodics-main.yaml)
   that invokes `make test-performance-multikueue` and publishes `ARTIFACTS`;
3. collect at least five runs on that worker and recalibrate the committed
   throughput and latency ranges;
4. enable TestGrid alerting only after the variance is shown to be stable;
5. add worker disconnect/reconnect and dispatcher-specific scenarios.

CPU and memory profiles are also deferred, and the current structure is what
defers them: all four controller managers run in one process, so a process
profile cannot attribute cost to the manager versus an individual worker.
Splitting them into separate processes, as the scheduler benchmark does with
`minimalkueue`, is what would make resource measurement possible.
