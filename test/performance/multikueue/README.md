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

## Sharing the scheduler test framework

This runner and `scheduler/minimalkueue` use the same core controller and
scheduler setup in `test/performance/framework/controllers`. The scheduler
benchmark also uses that setup for its TAS controllers. Each caller retains its
client settings, configuration defaulting, and lifecycle management.

This is the first step toward reusing `minimalkueue` as the controller process
for MultiKueue tests. The next steps are:

1. Add a MultiKueue manager mode and configurable client limits and controller
   concurrency to `minimalkueue`.
2. Start one controller process per cluster, with readiness checks, unexpected
   exit reporting, and bounded cleanup. This would allow separate manager and
   worker profiles.
3. Recalibrate after changing the process model. Keep the MultiKueue cluster
   connections, Job-backed workload generation, and admission recorder separate
   from the scheduler test's simulated workload execution.

The current benchmark continues to run its controllers in process; the shared
setup preserves each harness's existing configuration and measurement boundary.

## Run the baseline

```bash
make run-performance-multikueue
```

The baseline uses one generator to create 1,000 workloads as fast as the
manager accepts them and then waits for the queue to drain. This workload count reduces the proportion
of work covered by the clients' initial burst allowances. The 10-minute timeout is a safety bound for a hung run,
not a performance threshold.

One generator limits pressure on the freshly started API server's small watch
buffers. On the CI worker, generation takes almost the entire admission
interval, so this baseline includes a significant generation bottleneck.
It does not establish maximum MultiKueue admission capacity. A scenario intended
to measure that capacity must build a sustained backlog, with generation
substantially shorter than total admission time, and be calibrated separately.
`creationWorkers` is part of the scenario checked against the configuration.

The summary is written to
`artifacts/run-performance-multikueue/summary.yaml`.

For a quick local smoke run:

```bash
make run-performance-multikueue MULTIKUEUE_PERFORMANCE_ARGS="--workloads=12 --creationWorkers=3"
```

The scenario is configured in
[`configs/baseline/configuration.yaml`](configs/baseline/configuration.yaml).
It contains only setup and generation settings. The baseline creates one
ClusterQueue and one LocalQueue per cluster, with quota for the full batch.
Queue layout and workload creation intervals are not configurable yet.
Command-line overrides are intended for smoke tests; committed baseline changes
should be made in the configuration file.

[`configs/baseline/expectations.yaml`](configs/baseline/expectations.yaml)
contains only performance bounds. The checker reads the configuration separately
and compares it with the scenario recorded in the report before applying those
bounds. A smoke-test override therefore cannot be checked against the baseline
configuration accidentally. Both files are decoded strictly: setup fields are
rejected in expectations, and assertion fields are rejected in configuration.

`remoteClientQPS` and `remoteClientBurst` must both be positive and explicitly
configured. The baseline sets them to 1,000 each. The runner passes these values
through MultiKueue's `WithClientConnection` option and records the same values
and the `worker-cluster` rate-limit scope in the summary. A comparison against
different limits fails the scenario check.

`workloadCount` must be between 1 and 10,000. The upper bound keeps the
runner's per-Workload observation state and watch handover buffer bounded while
retaining room for a 10k-scale scenario.

Client limits, Workload reconcile concurrency, garbage collection, worker-loss
detection, and remote-event batching are explicit configuration values. The
baseline pins the values used for calibration, so changing production defaults
does not silently change the benchmark. The configuration currently supports
only the all-at-once dispatcher.

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
[`configs/baseline/expectations.yaml`](configs/baseline/expectations.yaml), and
retries once in the same way as the scheduler performance tests:

```bash
make test-performance-multikueue
```

The committed expectations are broad guardrails for large regressions, based on
the CI measurements below. Recalibrate from at least five runs on the dedicated
CI worker when the scenario or CI capacity changes. TestGrid alerting remains
disabled while the baseline's longer-term variance is assessed.

For this target, the summary is written to
`artifacts/test-performance-multikueue/run-performance-multikueue/summary.yaml`.

## Worker-client configuration

The runner uses the existing `clientConnection` support for worker clusters and
requires the default-enabled `MultiKueueReuseClientConnectionConfigForWorkers`
feature gate. The explicit 1,000 QPS and 1,000 burst baseline follows the
increased limits discussed in
[issue 14973](https://github.com/kubernetes-sigs/kueue/issues/14973).
These are scenario inputs, not a claim that every Kueue deployment uses them.

Results from client-go's implicit 5 QPS and burst 10 measure the bottleneck
addressed by that issue. Their throughput floor and latency ceilings are not
comparable with this scenario and must not be reused.

## CI calibration

Seven scheduled runs of `periodic-kueue-test-multikueue-perf-main` (14 attempts
including retries) admitted all 1,000 workloads with zero watch gaps. Only the
original 75 workloads/s throughput floor failed, as reported in
[issue 15891](https://github.com/kubernetes-sigs/kueue/issues/15891).

The observed ranges cover the
[first job](https://prow.k8s.io/view/gs/kubernetes-ci-logs/logs/periodic-kueue-test-multikueue-perf-main/2101010794903769088)
through the
[seventh job](https://prow.k8s.io/view/gs/kubernetes-ci-logs/logs/periodic-kueue-test-multikueue-perf-main/2102097974023688192):

| Measurement | Observed range |
|---|---|
| Throughput | 44.82–46.75 workloads/s |
| Admission P95 | 0.60–1.38 s |
| Quota-reservation P95 | 0.063–0.070 s |
| Generation time | 20.59–22.05 s |
| Total admission time | 21.39–22.31 s |
| Watch gaps | 0 in all attempts |

The 35 workloads/s floor leaves about 22% headroom below the slowest CI attempt.
The original 75 workloads/s floor came from local macOS measurements. The
15-second admission P95 and 1-second quota-reservation P95 ceilings are unchanged.

Generation occupies 96–99% of the measured interval, so this floor covers both
workload creation and admission. The generator can mask controller regressions;
changing generation concurrency requires new CI calibration.

## What bounds the measurement

The manager's local client uses the configured QPS and burst (1,000 each in the
baseline) with a single shared token bucket, matching the production entrypoint.
The generator has its own client so its requests do not consume that bucket.

Worker-client QPS and burst form one shared budget per worker cluster across
the direct clients and remote cache. Earlier benchmark revisions used separate
per-Kind budgets; their reports lack `remoteClientRateLimitScope` and are rejected
by the current scenario check.

Raising the remote limits can move the bottleneck onto the manager's shared
limiter or the host. The baseline measures end-to-end control-plane throughput;
it does not measure unconstrained processing capacity or count API requests.
CPU work, lock contention, or traffic outside the binding limiter can regress
without reducing observed throughput. Compare results only at identical
scenario settings and on stable CI capacity.

The baseline pins the production garbage collection, worker-loss, and event-batch
values used for calibration. Garbage collection can add traffic during sufficiently long runs.
Worker-loss detection is not exercised: its 15-minute timeout exceeds the
baseline's 10-minute safety bound. Failure and recovery scenarios are deferred.

## Reconcile concurrency

The runner takes Workload reconcile concurrency from the configuration. The
baseline sets it to 10, matching the MultiKueue e2e configuration. Without that setting, controller-runtime
would run only one reconcile at a time. `workloadConcurrency` is recorded in the
summary and matched against the configuration by the checker.

## Reading the summary

The runner submits workloads as fast as it can rather than at a fixed arrival
rate, so once the load saturates the dispatch path the latency percentiles
describe a workload's position in the drain queue rather than the cost of
dispatching it. They are still useful as a distribution shape and as a
same-scale comparison between revisions, but they must not be read as
per-workload service time, and they are only comparable across runs with an
identical `workloadCount`. When generation limits throughput, as in the CI
baseline above, the latency distribution does not demonstrate a sustained drain
queue. Interpret `maxAdmissionP95Ms` alongside generation time, total admission
time, and throughput.

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
admission observation. The expectations file tolerates one gap only while bounded
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

The raw runner remains observational. The dedicated periodic job in
[`kubernetes/test-infra`](https://github.com/kubernetes/test-infra/blob/master/config/jobs/kubernetes-sigs/kueue/kueue-periodics-main.yaml)
invokes `make test-performance-multikueue` every 12 hours and publishes
`ARTIFACTS`. It applies the broad guardrails above with notifications disabled.
Enable TestGrid alerting only after longer-term variance is shown to be stable.
Worker disconnect/reconnect and dispatcher-specific scenarios remain follow-up
work.

CPU and memory profiles are also deferred, and the current structure is what
defers them: all four controller managers run in one process, so a process
profile cannot attribute cost to the manager versus an individual worker.
Splitting them into separate processes, as the scheduler benchmark does with
`minimalkueue`, is what would make resource measurement possible.
