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
  - [UnschedulablePodsTracker controller](#unschedulablepodstracker-controller)
  - [Workload PodsScheduled condition](#workload-podsscheduled-condition)
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

Add `waitForPodsReady.unschedulableTimeout` to detect admitted workloads whose required pods
remain unschedulable (`PodScheduled != True`) sooner than the existing `timeout`, which is
intended for post-schedule startup (image pull, init containers, readiness probes).

## Motivation

On on-premises clusters, pods may take a long time to become Ready after scheduling because
of image pulling or initialization. Operators often set `waitForPodsReady.timeout` to 30
minutes to accommodate that.

However, pods that are not yet scheduled due to timing or transient state inconsistencies
between Kueue and the scheduler should be detected and requeued sooner. A separate timeout
allows faster recovery without shortening the time allowed for normal pod startup.

### Goals

- Add a configurable timeout for admitted workloads with unschedulable pods.
- Evict and requeue workloads that exceed the unschedulable timeout, reusing the existing
  `waitForPodsReady` eviction and requeue machinery with a dedicated scheduling underlying
  cause.
- When `unschedulableTimeout` is specified, start the `timeout` clock when all required pods
  are scheduled, so startup time is not consumed by scheduling delays.
- Detect scheduling state centrally so custom in-house job integrations work without
  per-integration code.

### Non-Goals

- Replacing or modifying kube-scheduler scheduling queue timeouts.
- Changing MultiKueue `PodScheduled` status sync behavior.
- A separate top-level eviction reason or requeue strategy; existing `PodsReadyTimeout` and
  `requeuingStrategy` are reused, with a dedicated underlying cause `WaitForSchedule` for
  scheduling timeouts (parallel to `WaitForStart` and `WaitForRecovery`).

## Proposal

Extend `WaitForPodsReady` with an optional `unschedulableTimeout` field. A new
`UnschedulablePodsTracker` controller (following the TopologyUngater pattern) lists pods per
admitted Workload, checks whether required pods have `PodScheduled=True`, and sets a
`PodsScheduled` workload condition. The job framework reconciler sets `PodsReady` only after
`PodsScheduled=True`. The workload controller enforces `unschedulableTimeout` while
`PodsScheduled=False` with reason `WaitForScheduling`.

### User Stories (Optional)

#### Story 1

An operator configures Kueue with a shorter scheduling window and a longer startup window:

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
waitForPodsReady:
  timeout: 30m
  unschedulableTimeout: 5m
```

A workload is admitted but its pods remain Pending because of a transient scheduler glitch.
After 5 minutes Kueue evicts and requeues the workload with underlying cause `WaitForSchedule`.
A pod that is scheduled but still pulling an image is allowed the full 30 minutes from the
moment all required pods are scheduled (`PodsScheduled=True`).

### Notes/Constraints/Caveats (Optional)

- `unschedulableTimeout` does not change kube-scheduler behavior or MultiKueue `PodScheduled`
  status sync.
- For `WaitForStart` when `unschedulableTimeout` is specified, the startup timer uses
  reconciliation observation time (`PodsReady.LastTransitionTime` when the reason changes),
  not the latest pod `PodScheduled=True` transition.
- When a running workload loses a scheduled pod, `WaitForRecovery` and `recoveryTimeout` take
  precedence over `PodsScheduled` / `WaitForScheduling` (workload controller evaluation order;
  see recovery predicate below).
- `UnschedulablePodsTracker` always reports scheduling observability on `PodsScheduled`; the
  job framework sets `PodsReady` per the scheduling vs recovery paths below.
- The pod `kueue.x-k8s.io/workload` annotation ([`WorkloadAnnotation`](apis/kueue/v1beta2/topology_types.go))
  is added today when the **TopologyAwareScheduling** feature gate is enabled. Clusters
  without that gate may require a broader workload-index path or follow-up design work before
  the tracker can identify workloads from pods everywhere.

### Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Evict a healthy workload on a transient list/index error | Return error (requeue); do not patch `PodsScheduled` from failed data; workload controller applies `unschedulableTimeout` only when tracker set `PodsScheduled=False` / `WaitForScheduling` from a successful reconcile |
| Stale `PodsScheduled=False` after a later list/index failure | No `SchedulingObserved` condition (per maintainer feedback); a prior successful observation may remain until the next successful reconcile; accepted trade-off documented here |
| Increased requeue churn when `unschedulableTimeout` is too low | Operator tuning; existing `requeuingStrategy` backoff |
| Consumers break on the new `PodsScheduled` condition | Document in backward compatibility; `PodsScheduled` is set for observability even when `unschedulableTimeout` is disabled |
| Tracker and job framework both update workload status | Separate condition types (`PodsScheduled` vs `PodsReady`) with distinct ownership; existing `SetConditionAndUpdate` / SSA patch path in `pkg/workload/workload.go` |

## Design Details

### Kueue Configuration API

```yaml
waitForPodsReady:
  timeout: 30m
  unschedulableTimeout: 5m
```

```go
type WaitForPodsReady struct {
    // ...
    // UnschedulableTimeout defines the time for an admitted workload to have all
    // required pods reach PodScheduled=True. When exceeded, the workload is evicted
    // and requeued. Must be non-negative and must not exceed the effective timeout
    // after defaulting. Defaults to disabled when unset or "0s".
    // +optional
    UnschedulableTimeout *metav1.Duration `json:"unschedulableTimeout,omitempty"`
}
```

**Validation:**

- `unschedulableTimeout` must be non-negative; negative values are rejected.
- `unschedulableTimeout` must not exceed the **effective** `timeout` after defaulting
  ([`apis/config/v1beta2/defaults.go`](apis/config/v1beta2/defaults.go) replaces a zero
  `WaitForPodsReady.Timeout` with `DefaultWaitForPodsReadyTimeout`).
- Validation cases: omitted `timeout` (defaults apply), explicit `0s` `timeout` (defaults
  apply), omitted or `0s` `unschedulableTimeout` (disabled), equal to effective `timeout`,
  greater than effective `timeout` (rejected).

### UnschedulablePodsTracker controller

A new controller, `UnschedulablePodsTracker`, follows the same architectural pattern as
`TopologyUngater` ([KEP-2724](keps/2724-topology-aware-scheduling/README.md)) and
`ElasticJobUngater`:

- Reconciles **Workload** resources that are **Admitted** and **not Finished** (not per Job
  integration).
- Watches **Pod** create/update/delete events and batches reconcile requests (using
  `UpdatesBatchPeriod`, as TopologyUngater does).
- Identifies the owning Workload from the pod `kueue.x-k8s.io/workload` annotation when
  present; may also list pods via the existing pod field index (`WorkloadSliceNameKey` /
  `IndexPodWorkloadSliceName` in `pkg/controller/core/indexer`) where applicable.
- **TopologyAwareScheduling dependency:** the workload annotation on pods is added when the
  TopologyAwareScheduling feature gate is enabled; document this constraint for operators and
  implementation.

**Reconcile flow:**

1. Skip workloads that are not admitted, finished, or evicted.
2. List non-terminated pods for the workload (annotation and/or field index, same patterns as
   TopologyUngater / `ElasticJobUngater`).
3. For each admitted `PodSet`, count **non-terminated** pods (grouped by
   `kueue.x-k8s.io/podset` label) against the granted counts on the Workload admission
   ([`utilpod.IsTerminated`](pkg/util/pod/pod.go), same as TopologyUngater). Terminated
   pods (`Succeeded` / `Failed`) are excluded from the active set and do **not** satisfy a
   granted slot; a replacement non-terminated pod is required to meet the grant.
4. For each required non-terminated pod, check `PodScheduled=True` in the pod status conditions.
5. On a successful pod list/index query, update the Workload **`PodsScheduled`** condition:
   - If any required pod is not scheduled → `PodsScheduled=False`, reason `WaitForScheduling`,
     message with scheduled counts (for example `2 of 3 required pods are scheduled`).
   - If all required pods are scheduled → `PodsScheduled=True`, reason
     `AllRequiredPodsScheduled`, message with counts (for example `3 of 3 required pods are
     scheduled`).
   During recovery, the tracker continues to report scheduling truth from observed pod state
   (may set `PodsScheduled=False` / `WaitForScheduling` when a pod is missing or unscheduled).
   The tracker does **not** participate in recovery timeout selection; workload controller
   precedence applies when `PodsReady` reason is `WaitForRecovery` (see below).
6. If a pod list or index query fails, return an error (requeue) and **do not patch**
   `PodsScheduled` from failed data.

**Required-pod semantics:**

| Case | Behavior |
|------|----------|
| Pod not yet created (count below granted) | Treat as not all scheduled; `PodsScheduled=False`, `WaitForScheduling`. |
| Terminated pod (`Succeeded`) | Excluded from active count; does not satisfy the grant. Without a replacement non-terminated pod, `PodsScheduled=False` until a replacement is scheduled (or the workload finishes per job semantics). |
| Terminated pod (`Failed`) | Excluded from active count; does not satisfy the grant. A replacement non-terminated pod is required; `PodsScheduled=False` until replaced and scheduled. |
| Pod deleted or preempted while workload was running | Tracker may report `PodsScheduled=False` / `WaitForScheduling`; `PodsReady` / `WaitForRecovery` and `recoveryTimeout` take precedence for eviction (workload controller skips `unschedulableTimeout`). |
| Optional PodSets with zero count | No pods required; scheduling check passes for that PodSet. |
| List/index client error | No `PodsScheduled` patch from failed data; requeue. |
| Successful observe, pods unschedulable | `PodsScheduled=False`, `WaitForScheduling`; eviction per timeout table. |
| Successful observe, all scheduled | `PodsScheduled=True`, `AllRequiredPodsScheduled`; hand off to job framework for `PodsReady`. |

### Workload PodsScheduled condition

Add constants:

```go
WorkloadPodsScheduled = "PodsScheduled"
WorkloadWaitForScheduling = "WaitForScheduling"
WorkloadAllRequiredPodsScheduled = "AllRequiredPodsScheduled"
```

`UnschedulablePodsTracker` owns updates to the `PodsScheduled` condition. Only one
`PodsScheduled` entry exists at a time (`SetStatusCondition` replaces by `type`). Example
status alternatives:

`PodsScheduled=False` (scheduling in progress):

```yaml
- type: PodsScheduled
  status: "False"
  reason: WaitForScheduling
  message: "2 of 3 required pods are scheduled"
```

`PodsScheduled=True` (all required pods scheduled):

```yaml
- type: PodsScheduled
  status: "True"
  reason: AllRequiredPodsScheduled
  message: "3 of 3 required pods are scheduled"
```

### Workload PodsReady condition

Existing constants `WorkloadWaitForStart` and `WorkloadWaitForRecovery` apply to `PodsReady`.

The job framework reconciler ([`generatePodsReadyCondition`](pkg/controller/jobframework/reconciler.go))
updates `PodsReady` via `GenericJob.PodsReady(ctx, client)`. It does **not** call
per-integration `PodsScheduled()` probes in the primary design. It does **not** select
eviction underlying causes (the workload controller owns timeout and cause selection,
similar to today's `WaitForStart` / `WaitForRecovery` path).

**Scheduling vs recovery paths:**

**Recovery predicate** (when recovery begins), aligned with
[`generatePodsReadyCondition`](pkg/controller/jobframework/reconciler.go):

- **Recovery path:** `PodsReady.Reason == WaitForRecovery`, **or** `PodsReady.Status` was
  `True` and `PodsReady()` is now false.
- **Initial scheduling path:** workload has never reached `PodsReady=True` (`PodsReady`
  condition is nil, reason `WaitForStart`, or legacy `PodsReady` reason).

1. **Initial scheduling path:** evaluate `PodsReady()` only when `PodsScheduled=True`; set
   `WaitForStart` when not ready. A pod failure before first `PodsReady=True` stays on this
   path (`WaitForStart` and `timeout` from admission), not `WaitForRecovery`.
2. **Recovery path:** evaluate `PodsReady()` regardless of `PodsScheduled`; set or retain
   `WaitForRecovery` per `generatePodsReadyCondition`. `PodsScheduled` may be `False` /
   `WaitForScheduling` concurrently; eviction uses `recoveryTimeout`, not
   `unschedulableTimeout`.

**Recovery precedence:** On the recovery path, `WaitForRecovery` and `recoveryTimeout` remain
authoritative over `PodsScheduled` / `WaitForScheduling`. `WaitForScheduling` on `PodsScheduled`
applies only on the initial scheduling path after admission, not when recovering from a running
workload failure.

### Timeout interaction

| Condition | `unschedulableTimeout` | Timer start | Duration |
|-----------|------------------------|-------------|----------|
| `PodsScheduled=False`, `WaitForScheduling` | not set / `0s` | `WorkloadAdmitted` | `timeout` (backward compatible) |
| `PodsScheduled=False`, `WaitForScheduling` | specified | `WorkloadAdmitted` | `unschedulableTimeout` |
| `PodsReady=False`, `WaitForStart` | specified | `PodsReady.LastTransitionTime` | `timeout` |
| `PodsReady=False`, `WaitForStart` | not set / `0s` | `WorkloadAdmitted` | `timeout` (unchanged) |
| `PodsReady=False`, `WaitForRecovery` | any | `PodsReady.LastTransitionTime` | `recoveryTimeout` (unchanged) |

For the `WaitForStart` row when `unschedulableTimeout` is specified, `PodsReady.LastTransitionTime`
is the **reconciliation observation time** when the job framework first sets `WaitForStart`
after `PodsScheduled=True` (consistent with today's `generatePodsReadyCondition` clock).
Delayed reconciliation can therefore extend the startup window slightly beyond the latest pod
`PodScheduled=True` transition.

When `unschedulableTimeout` is not configured, `PodsScheduled` is still set for observability
but timeout math is unchanged from today.

### Eviction and requeue

Eviction uses `WorkloadEvictedByPodsReadyTimeout`. The **workload controller** owns timeout
evaluation and underlying-cause selection in `admittedNotReadyWorkload`
([`pkg/controller/core/workload_controller.go`](pkg/controller/core/workload_controller.go)),
similar to today's `WaitForStart` / `WaitForRecovery` handling.

**Evaluation order** (first match wins):

1. `PodsReady=False` / `WaitForRecovery` → `recoveryTimeout`; underlying cause `WaitForRecovery`.
2. `PodsScheduled=False` / `WaitForScheduling` (and not in recovery) → `unschedulableTimeout`
   from `WorkloadAdmitted`; underlying cause **`WaitForSchedule`**.
3. `PodsReady=False` / `WaitForStart` → `timeout`; underlying cause `WaitForStart`.

The job framework reconciler updates `PodsReady` only; it does not select eviction causes.
Requeue backoff uses the existing `requeuingStrategy`.

Apply `unschedulableTimeout` only when `PodsScheduled=False` with reason `WaitForScheduling`
set by a **successful** tracker reconcile (tracker patches `PodsScheduled` only after a
successful pod list/index query). Do not evict on scheduling timeout while the tracker has
not successfully observed pod state from a failed list/index query. A stale
`PodsScheduled=False` from a prior successful observation may still allow eviction until the
next successful reconcile updates the condition (see Risks).

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Prerequisite testing updates

Existing integration coverage for `waitForPodsReady` in job controller integration tests
and workload controller unit tests in `pkg/controller/core/workload_controller_test.go`
provide the foundation for this enhancement.

#### Unit tests

- `UnschedulablePodsTracker`: `PodsScheduled` transitions, pod annotation / index listing,
  required-pod counting per PodSet, `PodScheduled` evaluation, list/index errors (no condition
  change on error), terminated-pod exclusion (`Succeeded` without replacement vs `Failed`
  with replacement), TopologyAwareScheduling annotation paths.
- Workload controller: timeout math with `PodsScheduled=False` / `WaitForScheduling` and
  `unschedulableTimeout` boundary values (unset, `0s`, **negative** rejected, equal to
  effective `timeout` after defaulting, greater than effective `timeout` rejected); underlying
  cause `WaitForSchedule` for scheduling timeouts; evaluation-order precedence
  (`WaitForRecovery` over `WaitForScheduling` over `WaitForStart`).
- Job framework: `PodsReady` after `PodsScheduled=True` on initial scheduling path;
  recovery predicate gates `PodsReady()` evaluation on recovery path (regardless of
  `PodsScheduled`); pre-first-readiness pod failure stays `WaitForStart`, not `WaitForRecovery`.

#### Integration tests

- Unschedulable Job evicted on `unschedulableTimeout` with underlying cause `WaitForSchedule`.
- Scheduled-but-not-ready Job uses full `timeout` from the `WaitForStart` transition.
- One representative operator integration (e.g. RayJob or MPIJob) for pod discovery.
- Running workload whose pod is deleted: `PodsScheduled=False` and `PodsReady=False` /
  `WaitForRecovery` coexist; `recoveryTimeout` wins over `unschedulableTimeout`.
- Race pod deletion with tracker and job reconciler; assert coexistence model —
  `PodsScheduled=False` with `WaitForRecovery` → `recoveryTimeout`, not `WaitForSchedule`.
- Regression: existing `waitForPodsReady` tests pass with `unschedulableTimeout` omitted.

#### e2e tests

Extend existing `waitforpodsready` e2e coverage in the implementation PR
([#13614](https://github.com/kubernetes-sigs/kueue/pull/13614)) after this KEP merges.

### Graduation Criteria

- **Beta** in Kueue **v0.20** (see `kep.yaml`).
- No feature gate; behavior is enabled when `unschedulableTimeout` is set to a positive value
  on the Kueue `waitForPodsReady` configuration.
- **Stable** when the feature is implemented, tested, documented in the user guide, and has
  run in at least one release without critical issues.

### Backward compatibility

- `unschedulableTimeout` unset or `0s`: **eviction timing and duration** are identical to
  current behavior (timeout measured from admission).
- `PodsScheduled` is a new workload condition; `WaitForScheduling` on that type is observable
  even when `unschedulableTimeout` is disabled. Consumers inspecting workload conditions must
  tolerate it.
- `blockAdmission`, `recoveryTimeout`, deactivation, and feature gate behavior unchanged.

## Implementation History

- 2026-07-27: Initial KEP for issue #13502.

## Drawbacks

- Adds a cluster-scoped controller with pod watches (additional operational surface).
- Central tracking couples scheduling detection with the job framework's readiness path.
- `PodsScheduled` / `WaitForScheduling` is visible in workload status even when the short
  timeout is disabled.
- TopologyAwareScheduling feature gate dependency for workload pod annotation today.

## Alternatives

### Per-integration `PodsScheduled` on `GenericJob`

Each job integration (Job, Pod, JobSet, MPIJob, Kubeflow jobs, AppWrapper, Spark,
RayJob, RayCluster, RayService, TrainJob) implements
`PodsScheduled(ctx, client) (bool, error)` on the `GenericJob` interface.
`generatePodsReadyCondition` calls it to detect scheduling state.

**Reasons for discarding as primary approach**

- High maintenance cost across many integrations; every new job type must implement
  scheduling probes.
- Does not work out-of-the-box for custom in-house integrations that use the job
  framework without upstream changes.
- Duplicates pod-listing logic already centralized for TopologyUngater and
  ElasticJobUngater.

This approach may be reconsidered only if the central controller proves too invasive
during implementation.
