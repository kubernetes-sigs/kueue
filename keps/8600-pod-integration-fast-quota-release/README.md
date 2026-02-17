# KEP-8600: Pod Integration Fast Quota Release

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Preemption with long-running Pod workloads](#story-1-preemption-with-long-running-pod-workloads)
    - [Story 2: Rapid workload cycling](#story-2-rapid-workload-cycling)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

This KEP introduces the `PodIntegrationFastQuotaRelease` feature gate to align
the Pod integration's quota release behavior with other integrations (e.g.,
batch/v1 Job). When enabled, quota is released as soon as the pod is terminating 
(i.e. have a `deletionTimestamp`), rather than waiting for Pods to
fully terminate.

This addresses a consistency gap where the Job integration releases quota when
`IsActive()` returns false (i.e., `status.active == 0`, which Kubernetes sets
as soon as all Pods have a `deletionTimestamp`), while the Pod integration
waits for Pods to reach a terminal phase (`Succeeded` or `Failed`).

## Motivation

When Kueue preempts a Pod-based workload, the current Pod integration holds
onto its quota reservation until all Pods have fully terminated (reached
`Succeeded` or `Failed` phase). This means that during the termination grace
period (which can be 30 seconds or more), quota remains occupied even though
the Pods are being deleted and will not complete their work.

This creates a bottleneck for preemption: higher-priority workloads cannot be
admitted until the preempted Pods are fully gone. In contrast, the batch/v1 Job
integration releases quota as soon as `status.active == 0`, which happens when
all Pods have a `deletionTimestamp` — before the Pods actually terminate.

This behavioral inconsistency between integrations can lead to:
- Delayed admission of higher-priority workloads during preemption
- Serialized preemption, where the scheduler must wait for one preemption to
  fully complete before starting the next
- Underutilization of cluster resources during termination grace periods

### Goals

- Align the Pod integration's quota release behavior with the Job integration
  and other integrations that release quota when `IsActive()` returns false.
- Release quota for Pod workloads as soon as all Pods have a
  `deletionTimestamp`, without waiting for Pods to reach a terminal phase.
- Provide a feature gate (`PodIntegrationFastQuotaRelease`) to control this
  behavior.
- Target Beta in v0.17, with backports as Alpha to v0.15 and v0.16.

### Non-Goals

- Changing quota release behavior for any integration other than Pod.
- Modifying the preemption algorithm itself (e.g., how Kueue decides which
  workloads to preempt).
- Handling the case where Pods are stuck terminating past their grace period
  (this is already handled by the existing `IsActive()` stuck-terminating
  logic).

## Proposal

Modify the Pod integration's `IsActive()` method to treat Pods with a
`deletionTimestamp` as inactive when the `PodIntegrationFastQuotaRelease`
feature gate is enabled. This means:

- A Pod with a `deletionTimestamp` is considered inactive, regardless of its
  current phase (including `Running`).
- Once all Pods in a workload are inactive (i.e., all have a
  `deletionTimestamp`), `IsActive()` returns false.
- The generic reconciler already uses `IsActive()` to determine when to release
  quota for evicted workloads, so no changes to the reconciler are needed.

This aligns with how the Kubernetes Job controller handles its `status.active`
field: a Pod is no longer counted as active as soon as it has a
`deletionTimestamp`, even if it is still running.

### User Stories

#### Story 1: Preemption with long-running Pod workloads

As a cluster administrator, I run long-running Pod workloads managed by Kueue
with a termination grace period of 60 seconds. When a higher-priority workload
arrives and Kueue preempts my running workload, I want the quota to be released
as soon as the Pods begin terminating, so the higher-priority workload can be
admitted without waiting up to 60 seconds.

#### Story 2: Rapid workload cycling

As a data scientist, I submit many short-lived Pod-based workloads to a shared
queue. When workloads are preempted and requeued, I want the quota churn to be
as fast as possible so my requeued workloads can be readmitted quickly.

### Notes/Constraints/Caveats

- The Kubernetes Job controller already behaves this way: it decrements
  `status.active` as soon as a Pod has a `deletionTimestamp`. The Pod
  integration is the outlier.
- This change only affects when quota is released for evicted/preempted
  workloads. The `Finished()` method (which determines when a workload is
  considered complete) is not changed — workloads still require Pods to reach
  a terminal phase to be marked as finished.
- Serving Pods (those marked as serving) are excluded from this behavior, as
  they are already excluded from the `Finished()` check.

### Risks and Mitigations

**Risk**: Releasing quota before Pods have fully terminated means the cluster
could temporarily be overcommitted (the terminating Pod's resources plus the
newly admitted workload's resources).

**Mitigation**: This is the same behavior that already exists for the Job
integration and other integrations. The Kubernetes scheduler handles this
gracefully — terminating Pods' resources are accounted for by the scheduler
until the Pods are fully deleted. This is a well-understood tradeoff that
enables faster preemption cycles.

**Risk**: Users relying on the current behavior (quota held until Pod
termination) might see different scheduling patterns.

**Mitigation**: The feature is gated behind `PodIntegrationFastQuotaRelease`
and follows the standard Alpha/Beta graduation process, giving users time to
test and adapt.

## Design Details

The change is scoped to the Pod controller in
`pkg/controller/jobs/pod/pod_controller.go`.

### `IsActive()` modification

When the `PodIntegrationFastQuotaRelease` feature gate is enabled, the
`IsActive()` method is modified to treat any Pod with a `deletionTimestamp` as
inactive:

```go
func (p *Pod) IsActive() bool {
    for i := range p.list.Items {
        pod := p.list.Items[i]

        // Pods that are not in the Running phase are never considered Active.
        if pod.Status.Phase != corev1.PodRunning {
            continue
        }

        // When PodIntegrationFastQuotaRelease is enabled, any pod with a
        // deletionTimestamp is considered inactive, aligning with how the
        // Job controller handles status.active.
        if features.Enabled(features.PodIntegrationFastQuotaRelease) {
            if pod.DeletionTimestamp != nil {
                continue
            }
        } else {
            // Legacy behavior: only skip pods stuck past their grace period.
            if pod.DeletionTimestamp != nil && pod.DeletionGracePeriodSeconds != nil {
                now := p.clock.Now()
                gracePeriod := time.Duration(*pod.DeletionGracePeriodSeconds) * time.Second
                if now.After(pod.DeletionTimestamp.Add(gracePeriod)) {
                    continue
                }
            }
        }

        return true
    }
    return false
}
```

### Reconciliation flow

No changes are needed to the generic reconciler. The existing flow already
handles this:

1. When a workload is evicted (e.g., by preemption), the reconciler calls
   `job.IsActive()` to check if the job is still active.
2. If `IsActive()` returns false, the reconciler clears the workload's
   admission, releasing quota.
3. The released quota becomes available for the next scheduling cycle.

### Feature gate registration

Add the feature gate constant and versioned spec to
`pkg/features/kube_features.go`:

```go
const (
    // owner: @tkillian
    // kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/8600-pod-integration-fast-quota-release
    //
    // Releases quota for Pod workloads as soon as all Pods are terminating
    // (have deletionTimestamp), aligning with Job and other integrations.
    PodIntegrationFastQuotaRelease featuregate.Feature = "PodIntegrationFastQuotaRelease"
)

var defaultVersionedFeatureGates = map[featuregate.Feature]featuregate.VersionedSpecs{
    // ...existing gates...
    PodIntegrationFastQuotaRelease: {
        {Version: version.MustParse("0.15"), Default: false, PreRelease: featuregate.Alpha},
        {Version: version.MustParse("0.17"), Default: true, PreRelease: featuregate.Beta},
    },
}
```

### Test Plan

[ ] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.

#### Prerequisite testing updates

None.

#### Unit tests

- `pkg/controller/jobs/pod`: Test `IsActive()` with the feature gate enabled
  and disabled, covering:
  - Pod with `deletionTimestamp` and feature gate enabled returns inactive.
  - Pod with `deletionTimestamp` and feature gate disabled returns active
    (unless past grace period).
  - Mixed groups with some terminating and some running Pods.
  - Single Pod (non-group) behavior.

#### Integration tests

- Test that when a Pod workload is evicted with the feature gate enabled, quota
  is released as soon as all Pods have a `deletionTimestamp`.
- Test that preempted workloads are readmitted promptly after the preempted
  Pods begin terminating.
- Test the feature gate disabled path preserves existing behavior.

#### e2e tests

- Test preemption scenario with Pod workloads where a higher-priority workload
  preempts a lower-priority one and is admitted while the preempted Pods are
  still terminating.

### Graduation Criteria

**Alpha (v0.15)**:
- Feature gate `PodIntegrationFastQuotaRelease` added, default disabled.
- Unit and integration tests covering the new `IsActive()` behavior.
- Backported to v0.15 and v0.16 release branches.

**Beta (v0.17)**:
- Feature gate default enabled.
- E2E tests demonstrating the preemption improvement.
- No negative feedback from Alpha usage.

**Stable**:
- Feature gate locked to default.
- Remove the legacy `IsActive()` code path.

## Implementation History

- 2026-02-17: KEP created.

## Drawbacks

- Adds a feature gate for a relatively small behavioral change. However, this
  is warranted because it changes when resources are released, which could
  affect scheduling behavior in unexpected ways for some users.

## Alternatives

### Modify the generic reconciler instead of the Pod controller

Instead of changing `IsActive()`, the reconciler could be modified to release
quota based on `deletionTimestamp` checks directly. This was rejected because:
- It would be a more invasive change affecting all integrations.
- The `IsActive()` interface is the correct abstraction for this behavior.
- Keeping the change in the Pod controller maintains consistency with how other
  integrations already work.

### Use `Finished()` instead of `IsActive()`

Modify `Finished()` to return true when all Pods are terminating. This was
rejected because:
- `Finished()` has different semantics — it means the workload has completed
  (successfully or not), not just that resources can be released.
- Marking a workload as finished when Pods are still terminating would be
  semantically incorrect and could cause issues with workload status reporting.
- The `IsActive()` path for evicted workloads is the correct mechanism for
  early quota release.

### Handle this in the preemption algorithm

Modify the preemption algorithm to account for terminating Pods' resources.
This was rejected because:
- It would be more complex and harder to reason about.
- The simpler fix of releasing quota earlier solves the problem cleanly.
- It would not address the consistency gap between integrations.
