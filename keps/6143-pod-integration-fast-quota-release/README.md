# KEP-6143: Pod Integration Fast Quota Release

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
batch/v1 Job). When enabled, quota from "plain pods" are released as soon as the
pods are terminating (i.e. has a `deletionTimestamp`), rather than waiting for pods
to fully terminate.

This addresses a consistency gap where the Job integration is considered not active 
as soon as `status.active == 0` on the job, and the Kubernetes job controller considers 
a pod active only if it has no `deletionTimestamp` set. Contrast this with the current 
pod controller, which considers a plain pod as active until the pod is fully terminated.

## Motivation

When Kueue preempts a Pod-based workload, the current Pod integration holds
onto its quota reservation until all Pods have fully terminated (reached
`Succeeded` or `Failed` phase). This termination often takes tens of seconds 
or even minutes (and has no practical upper bound, depending on how the pod's 
graceful shutdown period is configured). While the pod is terminating, ALL 
pods in each cluster queue is blocked from triggering further preemptions until 
it's finished because the terminating pod is always each potential preemptor pod's 
ideal preemption target. This obviously creates a large bottleneck for preemption 
that does not occur for preemptions of other types of workloads.

Ultimately, this behavioral inconsistency between integrations leads to:
- Delayed admission of higher-priority workloads during preemption
- Serialized preemption, where the scheduler must wait for one preemption to
  fully complete before starting the next

### Goals

- Align the Pod integration's quota release behavior with the Job integration
- Release quota for Pod workloads as soon as all Pods have a
  `deletionTimestamp`, without waiting for Pods to reach a terminal phase.
- Provide a feature gate (`PodIntegrationFastQuotaRelease`) to control this
  behavior.
- Target Beta in v0.17, with backports as Alpha to v0.15 and v0.16.

### Non-Goals

- Changing any default behavior, though it could be argued that this is a 
consistency bug that should be fixed.

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
arrives and Kueue preempts my running workload, I want the higher-priority 
workloads to start running as quickly as possible while still honoring 
graceful shutdown periods of preempted pods.

### Notes/Constraints/Caveats

- One could argue that this is a consistency bug fix. That being said, putting 
it behind a configuration flag would still be most prudent so users don't get any 
surprising changes in behavior.

### Risks and Mitigations

**Risk**: This could cause an increase in node churn if the cluster autoscaler 
is being used. While pods are pending, the cluster autoscaler may trigger scale-up 
even though pods could be terminating on existing nodes.

**Mitigation**: This is the same behavior that already exists for the Job
integration and other integrations, so it's a well established/understood 
behavior.

**Risk**: Even if a consistency bug, users may be relying on the current behavior (quota held until Pod
termination).

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
    // kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/6143-pod-integration-fast-quota-release
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

#### e2e tests

- Test preemption scenario with Pod workloads where a higher-priority workload
  preempts a lower-priority one and is admitted while the preempted Pods are
  still terminating.

### Graduation Criteria

## Implementation History

- 2026-02-17: KEP created.

## Drawbacks

- Adds a feature gate for a relatively small behavioral change. However, this
  is warranted because it changes when resources are released, which could
  affect scheduling behavior in surprising ways for some users.

## Alternatives

### Modify the generic reconciler instead of the Pod controller

Instead of changing just the pod controller's `IsActive()` logic, the reconciler 
could be modified to release quota for any workload as soon as it's marked for 
preemption. This was rejected because:
- It would be a more invasive change affecting all integrations
- The job-specific `IsActive()` implementation is considered the more appropriate 
place to dictate when to release quota.
- Keeping the change in the Pod controller maintains consistency with how other
  integrations already work.