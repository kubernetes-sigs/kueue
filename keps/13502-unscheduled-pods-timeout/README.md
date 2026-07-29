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
  - [Workload PodsReady condition](#workload-podsready-condition)
  - [Timeout interaction](#timeout-interaction)
  - [Eviction and requeue](#eviction-and-requeue)
  - [Test Plan](#test-plan)
  - [Backward compatibility](#backward-compatibility)
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

### Non-Goals

- Replacing or modifying kube-scheduler scheduling queue timeouts.
- Changing MultiKueue `PodScheduled` status sync behavior.
- A separate eviction reason or requeue strategy; existing `PodsReadyTimeout` and
  `requeuingStrategy` are reused.

## Proposal

Extend `WaitForPodsReady` with an optional `unscheduledTimeout` field. Job controllers
distinguish unscheduled pods from scheduled-but-not-ready pods via a new `PodsReady`
condition reason `WaitForScheduling`. The workload controller enforces the shorter timeout
when that reason is active and `unscheduledTimeout` is configured.

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

### Workload PodsReady condition

Add constant `WorkloadWaitForScheduling = "WaitForScheduling"`.

Job controllers implement `PodsScheduled(ctx, client) (bool, error)` on the `GenericJob` interface.
`generatePodsReadyCondition` sets `PodsReady=False` with reason `WaitForScheduling` when
not all required pods have `PodScheduled=True`.

### Timeout interaction

| `PodsReady` reason | `unscheduledTimeout` | Timer start | Duration |
|--------------------|----------------------|-------------|----------|
| `WaitForScheduling` | not set / `0s` | `WorkloadAdmitted` | `timeout` (backward compatible) |
| `WaitForScheduling` | set | `WorkloadAdmitted` | `unscheduledTimeout` |
| `WaitForStart` | set | `PodsReady.LastTransitionTime` | `timeout` |
| `WaitForStart` | not set | `WorkloadAdmitted` | `timeout` (unchanged) |
| `WaitForRecovery` | any | `PodsReady.LastTransitionTime` | `recoveryTimeout` (unchanged) |

When `unscheduledTimeout` is not configured, `WaitForScheduling` is still set for
observability but timeout math is unchanged from today.

### Eviction and requeue

Eviction uses `WorkloadEvictedByPodsReadyTimeout` with underlying cause
`WorkloadWaitForScheduling`. Requeue backoff uses the existing `requeuingStrategy`.

### Test Plan

- Unit tests for timeout math, condition generation, and `PodsScheduled()` per integration.
- Integration test: unschedulable job evicted on `unscheduledTimeout`; scheduled-but-not-ready
  job uses full `timeout` from scheduling transition.
- Regression: existing `waitForPodsReady` tests pass with `unscheduledTimeout` omitted.

### Backward compatibility

- `unscheduledTimeout` unset or `0s`: identical eviction timing to current behavior.
- `blockAdmission`, `recoveryTimeout`, deactivation, and feature gate behavior unchanged.

## Implementation History

- 2026-07-27: Initial KEP for issue #13502.
