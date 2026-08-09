# KEP-13502: Unscheduled Pods Timeout

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1](#story-1)
- [Design Details](#design-details)
  - [Kueue Configuration API](#kueue-configuration-api)
  - [UnscheduledPodsTracker controller](#unscheduledpodstracker-controller)
  - [Workload PodsReady condition](#workload-podsready-condition)
  - [Timeout interaction](#timeout-interaction)
  - [Eviction and requeue](#eviction-and-requeue)
  - [Test Plan](#test-plan)
  - [Backward compatibility](#backward-compatibility)
- [Alternatives](#alternatives)
- [Implementation History](#implementation-history)
<!-- /toc -->

## Summary

Add `waitForPodsReady.unscheduledTimeout` to detect admitted workloads whose pods remain
unscheduled (`PodScheduled != True`) sooner than the existing `timeout`, which is intended
for post-schedule startup (image pull, init containers, readiness probes).

## Motivation

On on-premises clusters, pods may take a long time to become Ready after scheduling because
of image pulling or initialization. Operators often set `waitForPodsReady.timeout` to 30
minutes to accommodate that.

However, pods that remain unscheduled due to timing or transient state inconsistencies
between Kueue and the scheduler should be detected and requeued sooner. A separate timeout
allows faster recovery without shortening the time allowed for normal pod startup.

### Goals

- Add a configurable timeout for admitted workloads with unscheduled pods.
- Evict and requeue workloads that exceed the unscheduled timeout, reusing the existing
  `waitForPodsReady` eviction and requeue machinery.
- When `unscheduledTimeout` is enabled, start the `timeout` clock when all pods are scheduled,
  so startup time is not consumed by scheduling delays.
- Detect scheduling state centrally so custom in-house job integrations work without
  per-integration code.

### Non-Goals

- Replacing or modifying kube-scheduler scheduling queue timeouts.
- Changing MultiKueue `PodScheduled` status sync behavior.
- A separate eviction reason or requeue strategy; existing `PodsReadyTimeout` and
  `requeuingStrategy` are reused.

## Proposal

Extend `WaitForPodsReady` with an optional `unscheduledTimeout` field. A new
`UnscheduledPodsTracker` controller (following the TopologyUngater pattern) lists pods per
admitted Workload, distinguishes unscheduled pods from scheduled-but-not-ready pods, and sets
a new `PodsReady` condition reason `WaitForScheduling`. The workload controller enforces the
shorter timeout when that reason is active and `unscheduledTimeout` is configured.

### User Stories

#### Story 1

An operator configures `waitForPodsReady.timeout: 30m` and `unscheduledTimeout: 5m`.
A workload is admitted but its pods remain Pending because of a transient scheduler glitch.
After 5 minutes Kueue evicts and requeues the workload. A pod that is scheduled but still
pulling an image is allowed the full 30 minutes from the moment all pods are scheduled.

## Design Details

### Kueue Configuration API

```yaml
waitForPodsReady:
  timeout: 30m
  unscheduledTimeout: 5m
```

```go
type WaitForPodsReady struct {
    // ...
    // UnscheduledTimeout defines the time for an admitted workload to have all
    // required pods reach PodScheduled=True. When exceeded, the workload is evicted
    // and requeued. Must be non-negative and must not exceed timeout.
    // Defaults to disabled when unset or "0s".
    // +optional
    UnscheduledTimeout *metav1.Duration `json:"unscheduledTimeout,omitempty"`
}
```

### UnscheduledPodsTracker controller

A new controller, `UnscheduledPodsTracker`, follows the same architectural pattern as
`TopologyUngater` ([KEP-2724](keps/2724-topology-aware-scheduling/README.md)) and
`ElasticJobUngater`:

- Reconciles per **Workload** (not per Job integration).
- Watches **Pod** create/update/delete events and batches reconcile requests (using
  `UpdatesBatchPeriod`, as TopologyUngater does).
- Lists pods for a workload using the existing pod field index
  (`WorkloadSliceNameKey` / `IndexPodWorkloadSliceName` in
  `pkg/controller/core/indexer`), which indexes by `WorkloadSliceNameAnnotation` or falls
  back to `WorkloadAnnotation` for non-elastic workloads.

**Reconcile flow:**

1. Skip workloads that are not admitted, finished, or evicted.
2. List non-terminated pods for the workload slice via the field index (same lookup pattern
   as `ElasticJobUngater.podsToUngate`).
3. For each admitted `PodSet`, compare existing non-terminated pods (grouped by
   `kueue.x-k8s.io/podset` label) against the granted counts on the Workload admission.
4. For each required pod, check `PodScheduled=True` in the pod status conditions.
5. Update the Workload `PodsReady` condition:
   - If any required pod is not scheduled → `PodsReady=False`, reason `WaitForScheduling`.
   - If all required pods are scheduled but not ready → delegate readiness to the job
     framework (`generatePodsReadyCondition` / `PodsReady()`), which sets reason
     `WaitForStart` or `WaitForRecovery` as today.
6. If a pod list or index query fails, log the error and **do not** transition to
   `WaitForScheduling` or evict; retry on the next reconcile.

**Required-pod semantics:**

| Case | Behavior |
|------|----------|
| Pod not yet created (count below granted) | Treat as not all scheduled; remain in or enter `WaitForScheduling`. |
| Terminated pod (Succeeded/Failed) | Excluded from the scheduling check (same as TopologyUngater). |
| Pod deleted or preempted while workload was running | `WaitForRecovery` takes precedence over `WaitForScheduling`; `recoveryTimeout` applies (see below). |
| Optional PodSets with zero count | No pods required; scheduling check passes for that PodSet. |
| List/index client error | No condition change toward `WaitForScheduling`; no eviction. |

The job framework `generatePodsReadyCondition` continues to evaluate pod **readiness**
via `GenericJob.PodsReady(ctx, client) bool` but does **not** implement per-integration
scheduling probes. Scheduling state is owned by `UnscheduledPodsTracker`.

### Workload PodsReady condition

Add constant `WorkloadWaitForScheduling = "WaitForScheduling"`.

`UnscheduledPodsTracker` sets `PodsReady=False` with reason `WaitForScheduling` when not
all required pods have `PodScheduled=True`. When all required pods are scheduled but not
ready, the job framework sets `WaitForStart` (or `WaitForRecovery` after a failure) via
the existing `generatePodsReadyCondition` path.

**Recovery precedence:** When a workload was `PodsReady=True` and a required pod fails or
is removed, the job framework keeps reason `WaitForRecovery` (not `WaitForScheduling`).
`recoveryTimeout` remains authoritative for that transition. `WaitForScheduling` applies
only on the initial scheduling path after admission, not when recovering from a running
workload failure.

### Timeout interaction

| `PodsReady` reason | `unscheduledTimeout` | Timer start | Duration |
|--------------------|----------------------|-------------|----------|
| `WaitForScheduling` | not set / `0s` | `WorkloadAdmitted` | `timeout` (backward compatible) |
| `WaitForScheduling` | set | `WorkloadAdmitted` | `unscheduledTimeout` |
| `WaitForStart` | set | `PodsReady.LastTransitionTime` | `timeout` |
| `WaitForStart` | not set | `WorkloadAdmitted` | `timeout` (unchanged) |
| `WaitForRecovery` | any | `PodsReady.LastTransitionTime` | `recoveryTimeout` (unchanged) |

For the `WaitForStart` row when `unscheduledTimeout` is set, `PodsReady.LastTransitionTime`
is the **reconciliation observation time** when the reason transitions from
`WaitForScheduling` to `WaitForStart` (consistent with today's
`generatePodsReadyCondition` clock). Delayed reconciliation can therefore extend the
startup window slightly beyond the latest pod `PodScheduled=True` transition.

When `unscheduledTimeout` is not configured, `WaitForScheduling` is still set for
observability but timeout math is unchanged from today.

### Eviction and requeue

Eviction uses `WorkloadEvictedByPodsReadyTimeout` with underlying cause
`WorkloadWaitForScheduling`. Requeue backoff uses the existing `requeuingStrategy`.

### Test Plan

- Unit tests for `UnscheduledPodsTracker`: pod listing, required-pod counting per PodSet,
  `PodScheduled` evaluation, list/index errors, terminated-pod exclusion, and condition
  transitions (`WaitForScheduling` ↔ `WaitForStart`).
- Unit tests for workload-controller timeout math with `WaitForScheduling` reason and
  `unscheduledTimeout` boundary values (unset, `0s`, equal to `timeout`, greater than
  `timeout` rejected at validation).
- Integration tests: unschedulable Job evicted on `unscheduledTimeout`; scheduled-but-not-ready
  Job uses full `timeout` from the `WaitForStart` transition; one representative operator
  integration (e.g. RayJob or MPIJob) for index-based pod discovery.
- Integration test: running workload whose pod is deleted keeps `WaitForRecovery` and
  `recoveryTimeout` precedence.
- Regression: existing `waitForPodsReady` tests pass with `unscheduledTimeout` omitted.

### Backward compatibility

- `unscheduledTimeout` unset or `0s`: **eviction timing and duration** are identical to
  current behavior (timeout measured from admission).
- `WaitForScheduling` is a new `PodsReady` reason for unscheduled pods; consumers that
  inspect `PodsReady` reason must tolerate it even when `unscheduledTimeout` is disabled.
- `blockAdmission`, `recoveryTimeout`, deactivation, and feature gate behavior unchanged.

## Alternatives

### Per-integration `PodsScheduled` on `GenericJob`

Each job integration (Job, Pod, JobSet, MPIJob, Kubeflow jobs, AppWrapper, Spark,
RayJob, RayCluster, RayService, TrainJob) implements
`PodsScheduled(ctx, client) (bool, error)` on the `GenericJob` interface.
`generatePodsReadyCondition` calls it to set `WaitForScheduling`.

**Reasons for discarding as primary approach**

- High maintenance cost across many integrations; every new job type must implement
  scheduling probes.
- Does not work out-of-the-box for custom in-house integrations that use the job
  framework without upstream changes.
- Duplicates pod-listing logic already centralized for TopologyUngater and
  ElasticJobUngater.

This approach was prototyped in the implementation branch for
[#13614](https://github.com/kubernetes-sigs/kueue/pull/13614) and may be reconsidered
only if the central controller proves too invasive during implementation.

## Implementation History

- 2026-07-27: Initial KEP for issue #13502.
- 2026-08-12: Adopt TopologyUngater-style `UnscheduledPodsTracker` as primary mechanism;
  move per-integration `PodsScheduled` to Alternatives.
