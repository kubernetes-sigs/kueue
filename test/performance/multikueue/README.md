# MultiKueue performance benchmark

This benchmark measures the MultiKueue control-plane path across one manager
cluster and a configurable number of worker clusters. The baseline uses three
workers, matching the topology proposed in the tracking issue.

The runner starts isolated `envtest` control planes and runs the real Kueue
core scheduler and MultiKueue controllers in process. It creates a suspended
batch Job and its Workload on the manager for every sample. MultiKueue copies
the Workload to the workers, whose schedulers reserve quota. It then copies the
Job to the selected worker, and the manager admits the local Workload.

This boundary deliberately excludes Kubernetes Job and Pod execution. It
measures MultiKueue dispatch and admission rather than kube-controller-manager
or container runtime performance.

## Run the baseline

```bash
make run-performance-multikueue
```

The baseline uses one generator to create 1,000 workloads as fast as the
manager accepts them and then waits for the queue to drain. This workload count reduces the proportion
of work covered by the clients' initial burst allowances. The 10-minute timeout is a safety bound for a hung run,
not a performance threshold.

One generator avoids overflowing the freshly started API server's small watch
buffers with parallel creates. Check that generation remains substantially
shorter than total admission time when calibrating on the CI worker, so the
generator does not become the throughput bottleneck. `creationWorkers` is part
of the scenario and must match the range spec.

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

`remoteClientQPS` and `remoteClientBurst` must both be positive and explicitly
configured. The baseline sets them to 1,000 each. The runner passes these values
through MultiKueue's worker-client configuration and records the same values in
the summary. A comparison against different limits fails the scenario check.

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

## Worker-client configuration

The runner uses MultiKueue's worker-client configuration API. The explicit
1,000 QPS and 1,000 burst baseline follows the increased limits discussed in
[issue 14973](https://github.com/kubernetes-sigs/kueue/issues/14973).
These are scenario inputs, not a claim that every Kueue deployment uses them.

Results from client-go's implicit 5 QPS and burst 10 measure the bottleneck
addressed by that issue. Their throughput floor and latency ceilings are not
comparable with this scenario and must not be reused.

Five consecutive local runs on a macOS arm64 host produced:

| Measurement | Observed range |
|---|---|
| Throughput | 27.81–27.89 workloads/s |
| Admission P95 | 30.13–30.59 s |
| Quota-reservation P95 | 2.78–2.87 s |
| Generation time | 1.90–2.19 s |
| Total admission time | 35.86–35.96 s |
| Watch gaps | 0 in every run |

The provisional floor of 22 workloads/s leaves about 21% headroom below the
slowest local run. The 45-second admission P95 and 5-second quota-reservation
P95 ceilings allow additional variance. These are initial regression guards,
not CI calibration; tighten or revise them from measurements on the dedicated
worker. No CPU or memory capacity claim follows from these results.

## What bounds the measurement

The manager's local client uses Kueue's default QPS and burst with a single
shared token bucket, matching the production entrypoint. The generator has its
own client so its requests do not consume that bucket.

Worker-client QPS and burst are passed through MultiKueue to each worker's
`rest.Config`. controller-runtime creates REST clients per GVK, so these limits
apply per worker per Kind, rather than as one shared budget for a worker.

Raising the remote limits can move the bottleneck onto the manager's shared
limiter or the host. The baseline measures end-to-end control-plane throughput;
it does not measure unconstrained processing capacity or count API requests.
CPU work, lock contention, or traffic outside the binding limiter can regress
without reducing observed throughput. Compare results only at identical
scenario settings and on stable CI capacity.

The runner retains production garbage collection, worker-loss, and event-batch
defaults. Garbage collection can add traffic during sufficiently long runs.
Worker-loss detection is not exercised: its 15-minute timeout exceeds the
baseline's 10-minute safety bound. Failure and recovery scenarios are deferred.

## Reconcile concurrency

The runner sets `groupKindConcurrency` to match the MultiKueue e2e configuration,
including `Workload.kueue.x-k8s.io: 10`. Without that setting, controller-runtime
would run only one reconcile at a time. `workloadConcurrency` is recorded in the
summary and matched exactly by the checker.

## Reading the summary

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
  min/avg/P50/P95/P99/max. Quota reservation measures manager-side scheduling
  before admission checks complete; dispatch traffic can also affect it through
  the manager's shared local rate limiter. Admission
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
recovers. The committed tolerance is provisional and must also be checked on
the CI worker.

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

1. merge the worker-client configuration dependency, then the runner and
   regression-check target in this repository;
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
