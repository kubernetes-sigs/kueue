# KEP-13502: Unscheduled Pods Timeout

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
  - [Notes/Constraints/Caveats (Optional)](#notesconstraintscaveats-optional)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Kueue Configuration API](#kueue-configuration-api)
  - [UnscheduledPodsTracker controller](#unscheduledpodstracker-controller)
  - [PodsReady condition ownership](#podsready-condition-ownership)
  - [Workload PodsReady condition](#workload-podsready-condition)
  - [Timeout interaction](#timeout-interaction)
  - [Eviction and requeue](#eviction-and-requeue)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
  - [Backward compatibility](#backward-compatibility)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Per-integration <code>PodsScheduled</code> on <code>GenericJob</code>](#per-integration-podsscheduled-on-genericjob)
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

### User Stories (Optional)

#### Story 1

An operator configures `waitForPodsReady.timeout: 30m` and `unscheduledTimeout: 5m`.
A workload is admitted but its pods remain Pending because of a transient scheduler glitch.
After 5 minutes Kueue evicts and requeues the workload. A pod that is scheduled but still
pulling an image is allowed the full 30 minutes from the moment all pods are scheduled.

### Notes/Constraints/Caveats (Optional)

- `unscheduledTimeout` does not change kube-scheduler behavior or MultiKueue `PodScheduled`
  status sync.
- For `WaitForStart` when `unscheduledTimeout` is set, the startup timer uses reconciliation
  observation time (`PodsReady.LastTransitionTime` when the reason changes), not the latest
  pod `PodScheduled=True` transition.
- When a running workload loses a scheduled pod, `WaitForRecovery` and `recoveryTimeout`
  take precedence over `WaitForScheduling`.
- Single owner for scheduling-related `PodsReady` updates: see
  [PodsReady condition ownership](#podsready-condition-ownership). The job framework handles
  readiness only; it does not call per-integration scheduling probes in the primary design.

### Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Evict a healthy workload on a transient list/index error | Return error (requeue); set `SchedulingObserved=False` with reason `ObservationFailed`; workload controller suppresses `unscheduledTimeout` eviction until observation succeeds |
| Increased requeue churn when `unscheduledTimeout` is too low | Operator tuning; existing `requeuingStrategy` backoff |
| Consumers break on the new `WaitForScheduling` reason | Document in backward compatibility; reason is set even when `unscheduledTimeout` is disabled |
| Controller and job framework both update `PodsReady` | See [PodsReady condition ownership](#podsready-condition-ownership); tracker owns scheduling state and `SchedulingObserved`; job framework owns readiness only |

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
5. On a successful pod list/index query, set `SchedulingObserved=True` and update the
   Workload `PodsReady` condition:
   - If any required pod is not scheduled → `PodsReady=False`, reason `WaitForScheduling`.
   - If all required pods are scheduled but not ready → stop asserting scheduling state;
     delegate readiness to the job framework (`generatePodsReadyCondition` / `PodsReady()`),
     which sets reason `WaitForStart` or `WaitForRecovery` as today.
6. If a pod list or index query fails, return an error (requeue), set
   `SchedulingObserved=False` with reason `ObservationFailed`, and **do not** infer
   `WaitForScheduling` from failed data. The workload controller suppresses
   `unscheduledTimeout` eviction while `SchedulingObserved` is not `True`.

**Required-pod semantics:**

| Case | Behavior |
|------|----------|
| Pod not yet created (count below granted) | Treat as not all scheduled; remain in or enter `WaitForScheduling`. |
| Terminated pod (Succeeded/Failed) | Excluded from the scheduling check (same as TopologyUngater). |
| Pod deleted or preempted while workload was running | `WaitForRecovery` takes precedence over `WaitForScheduling`; `recoveryTimeout` applies (see below). |
| Optional PodSets with zero count | No pods required; scheduling check passes for that PodSet. |
| List/index client error | `SchedulingObserved=False`, reason `ObservationFailed`; no eviction. |
| Successful observe, pods unscheduled | `SchedulingObserved=True`; `WaitForScheduling`; eviction per timeout table. |
| Successful observe, all scheduled | `SchedulingObserved=True`; hand off to job framework for `WaitForStart`. |

### PodsReady condition ownership

| Component | Responsibility |
|-----------|----------------|
| `UnscheduledPodsTracker` | Pod list/index, `PodScheduled` checks, `SchedulingObserved`, set `PodsReady=False` with reason `WaitForScheduling`, hand off when all pods are scheduled |
| Job framework `generatePodsReadyCondition` | **Readiness only** via `GenericJob.PodsReady()` when `SchedulingObserved=True` and all required pods are scheduled |
| Workload controller | Timeout and eviction math only; does not list pods |

**Coordination rules:**

1. The job reconciler does **not** call `PodsScheduled()` on `GenericJob` in the primary
   design (no per-integration scheduling probes).
2. When not all pods are scheduled, **only** `UnscheduledPodsTracker` sets reason
   `WaitForScheduling`.
3. When all pods are scheduled, the tracker stops asserting `WaitForScheduling`; the job
   framework sets `WaitForStart` or `WaitForRecovery` via existing readiness logic.
4. **Recovery precedence:** if `PodsReady` was `True` and a pod fails or is removed, the job
   framework keeps reason `WaitForRecovery`; the tracker does not overwrite with
   `WaitForScheduling` (see `generatePodsReadyCondition` in
   `pkg/controller/jobframework/reconciler.go`).
5. The implementation prototype in [#13614](https://github.com/kubernetes-sigs/kueue/pull/13614)
   that calls `PodsScheduled()` inside `generatePodsReadyCondition` is **superseded** by this
   KEP; [#13614](https://github.com/kubernetes-sigs/kueue/pull/13614) will pivot after the KEP
   merges. Per-integration `PodsScheduled` remains documented under [Alternatives](#alternatives).

### Workload PodsReady condition

Add constants:

```go
WorkloadWaitForScheduling = "WaitForScheduling"
// SchedulingObserved indicates UnscheduledPodsTracker successfully observed
// required pods for scheduling state. False suppresses unscheduledTimeout eviction.
WorkloadSchedulingObserved = "SchedulingObserved"
```

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
| `WaitForScheduling` | set | `WorkloadAdmitted` | `unscheduledTimeout` (only when `SchedulingObserved=True`) |
| `SchedulingObserved=False` | set | — | No eviction deadline; tracker requeue until observation succeeds |
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

In `admittedNotReadyWorkload` (`pkg/controller/core/workload_controller.go`), extend timeout
math so `unscheduledTimeout` eviction for reason `WaitForScheduling` runs only when
`SchedulingObserved=True`. When `SchedulingObserved=False` (for example after a pod
list/index error), return no eviction deadline and rely on the tracker to requeue and
restore observation.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Prerequisite testing updates

Existing integration coverage for `waitForPodsReady` in job controller integration tests
and workload controller unit tests in `pkg/controller/core/workload_controller_test.go`
provide the foundation for this enhancement.

#### Unit tests

- `UnscheduledPodsTracker`: pod index listing, required-pod counting per PodSet,
  `PodScheduled` evaluation, `SchedulingObserved` transitions, list/index errors,
  terminated-pod exclusion, and condition transitions (`WaitForScheduling` ↔ `WaitForStart`).
- Workload controller: timeout math with `WaitForScheduling` reason and
  `unscheduledTimeout` boundary values (unset, `0s`, equal to `timeout`; greater than
  `timeout` rejected at validation).

#### Integration tests

- Unschedulable Job evicted on `unscheduledTimeout`.
- Scheduled-but-not-ready Job uses full `timeout` from the `WaitForStart` transition.
- One representative operator integration (e.g. RayJob or MPIJob) for index-based pod
  discovery.
- Running workload whose pod is deleted keeps `WaitForRecovery` and `recoveryTimeout`
  precedence.
- Regression: existing `waitForPodsReady` tests pass with `unscheduledTimeout` omitted.

#### e2e tests

Extend existing `waitforpodsready` e2e coverage in the implementation PR ([#13614](https://github.com/kubernetes-sigs/kueue/pull/13614)).

### Graduation Criteria

- **Beta** in Kueue **v0.20** (see `kep.yaml`).
- No feature gate; behavior is enabled when `unscheduledTimeout` is set to a positive value
  on a ClusterQueue's `waitForPodsReady` configuration.
- **Stable** when the feature is implemented, tested, documented in the user guide, and has
  run in at least one release without critical issues.

### Backward compatibility

- `unscheduledTimeout` unset or `0s`: **eviction timing and duration** are identical to
  current behavior (timeout measured from admission).
- `WaitForScheduling` is a new `PodsReady` reason for unscheduled pods; consumers that
  inspect `PodsReady` reason must tolerate it even when `unscheduledTimeout` is disabled.
- `blockAdmission`, `recoveryTimeout`, deactivation, and feature gate behavior unchanged.

## Implementation History

- 2026-07-27: Initial KEP for issue #13502.
- 2026-08-12: Adopt TopologyUngater-style `UnscheduledPodsTracker` as primary mechanism;
  move per-integration `PodsScheduled` to Alternatives.
- 2026-08-18: Restore KEP template sections and fix TOC for CI verify.
- 2026-08-18: Document `PodsReady` condition ownership and `SchedulingObserved` eviction
  suppression (CodeRabbit review).

## Drawbacks

- Adds a cluster-scoped controller with pod watches (additional operational surface).
- Central tracking couples scheduling detection with the job framework's readiness path.
- `WaitForScheduling` is visible in workload status even when the short timeout is disabled.

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
[#13614](https://github.com/kubernetes-sigs/kueue/pull/13614). That prototype conflicts with
the [PodsReady condition ownership](#podsready-condition-ownership) model in this KEP and
will not ship as the primary design; [#13614](https://github.com/kubernetes-sigs/kueue/pull/13614)
will pivot after the KEP merges. Per-integration `PodsScheduled` may be reconsidered only if
the central controller proves too invasive during implementation.
