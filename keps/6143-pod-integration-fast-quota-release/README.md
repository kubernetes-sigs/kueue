# KEP-6143: Pod Integration Fast Quota Release

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Preemption of &quot;plain pod&quot; workloads](#story-1-preemption-of-plain-pod-workloads)
    - [Story 2: ClusterQueue migration to new clusters](#story-2-clusterqueue-migration-to-new-clusters)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [<code>IsActive()</code> modification](#isactive-modification)
  - [Reconciliation flow](#reconciliation-flow)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha (v0.17)](#alpha-v017)
    - [Beta (v0.18)](#beta-v018)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Modify the generic reconciler instead of the Pod controller](#modify-the-generic-reconciler-instead-of-the-pod-controller)
<!-- /toc -->

## Summary

This KEP introduces two feature gates to give users control over when the Pod
integration releases quota:

- `FastQuotaRelease`: Aligns the Pod integration's quota release behavior with
  other integrations (e.g., batch/v1 Job). When enabled, quota from "plain pods"
  is released as soon as the pods are terminating (i.e. have a
  `deletionTimestamp`), rather than waiting for pods to fully terminate.
- `DelayedQuotaRelease`: Preserves the current Pod integration behavior where
  quota is only released once all pods have reached a terminal phase
  (`Succeeded` or `Failed`). This is important for fixed-size clusters where
  strict quota management is required.

Both feature gates are introduced as Alpha (disabled by default) in v0.17. In
v0.18, these feature gates will be replaced by configuration knobs in the
Kueue Configuration API, allowing cluster administrators to select the
appropriate quota release mode for their environment.

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
- Serialized preemption, where each ClusterQueue is head-of-line blocking causing
  the scheduler to wait for one preemption within the ClusterQueue to fully complete
  before starting the next

### Goals

- Provide two quota release modes for the Pod integration:
  - **Fast quota release** (`FastQuotaRelease`): Release quota as soon as all
    Pods have a `deletionTimestamp`, aligning with current Job integration
    behavior.
  - **Delayed quota release** (`DelayedQuotaRelease`): Release quota only after
    all Pods have reached a terminal phase, preserving strict quota tracking for
    fixed-size clusters.
- Introduce both modes behind feature gates in v0.17 (Alpha, disabled by default).
- Convert feature gates into configuration knobs in the Kueue Configuration API
  in v0.18 (Beta).

### Non-Goals

## Proposal

Introduce two mutually exclusive quota release modes for the Pod integration,
each controlled by a feature gate:

**`FastQuotaRelease` (Alpha, disabled by default)**

Modify the Pod integration's `IsActive()` method to treat Pods with a
`deletionTimestamp` as inactive. This means:

- A Pod with a `deletionTimestamp` is considered inactive, regardless of its
  current phase (including `Running`).
- Once all Pods in a workload are inactive (i.e., all have a
  `deletionTimestamp`), `IsActive()` returns false.
- The generic reconciler already uses `IsActive()` to determine when to release
  quota for evicted workloads, so no changes to the reconciler are needed.

This aligns with how the Kubernetes Job controller handles its `status.active`
field: a Pod is no longer counted as active as soon as it has a
`deletionTimestamp`, even if it is still running.

**`DelayedQuotaRelease` (Alpha, disabled by default)**

When enabled, this explicitly preserves and formalizes the current Pod
integration behavior: quota is only released once all Pods in the workload
have reached a terminal phase (`Succeeded` or `Failed`). This mode exists to
provide strict quota tracking for environments (e.g., fixed-size clusters
without autoscaling) where temporary quota oversubscription is not acceptable.

When neither feature gate is enabled, the current default behavior is retained
(delayed quota release). When both feature gates are enabled, `FastQuotaRelease`
takes precedence.

### User Stories

#### Story 1: Preemption of "plain pod" workloads

As a cluster administrator, I run long-running Pod workloads managed by Kueue
with a termination grace period of 60 seconds. When a higher-priority workload
arrives and Kueue preempts my running workload, I want the higher-priority
workloads to start running as quickly as possible while still honoring
graceful shutdown periods of preempted pods.

#### Story 2: ClusterQueue migration to new clusters

As a cluster administrator, I migrate ClusterQueues to a new cluster one by one.
Because burst is free for the initial CQs, these CQs will burst past their
nominal quota. I want subsequent CQs to be able to reclaim their nominal quota
within a reasonable amount of time.

### Notes/Constraints/Caveats

### Risks and Mitigations

**Risk**: `FastQuotaRelease` could cause an increase in node churn if the
cluster autoscaler is being used. While pods are pending, the cluster
autoscaler may trigger scale-up even though pods could be terminating on
existing nodes.

**Mitigation**: This is the same behavior that already exists for the Job
integration and other integrations, so it's a well established/understood
behavior. Users on fixed-size clusters can use `DelayedQuotaRelease` to
maintain strict quota tracking.

**Risk**: With `FastQuotaRelease`, if a pod gets "stuck" in a terminating
state, there may be a discrepancy between the amount of quota available and the
actual resources available on the cluster. In fixed-size clusters where the sum
of nominal quotas strictly equals cluster capacity, this can cause temporary
capacity oversubscription.

**Mitigation**: Users who require strict quota management should use
`DelayedQuotaRelease`. Additionally, [setup failure recovery](https://kueue.sigs.k8s.io/docs/tasks/manage/setup_failure_recovery/)
provides a failure recovery mechanism that automatically transitions
pods into the Failed phase when they are assigned to unreachable nodes and stuck
terminating.

## Design Details

The change is scoped to the Pod controller in
`pkg/controller/jobs/pod/pod_controller.go`.

### `IsActive()` modification

When the `FastQuotaRelease` feature gate is enabled, the `IsActive()` method is
modified to treat any Pod with a `deletionTimestamp` as inactive.

When the `DelayedQuotaRelease` feature gate is enabled (or neither gate is
enabled), the existing behavior is preserved: a Pod is only considered inactive
once it has reached a terminal phase (`Succeeded` or `Failed`).

### Reconciliation flow

No changes are needed to the generic reconciler. The existing flow already
handles this:

1. When a workload is evicted (e.g., by preemption), the reconciler calls
   `job.IsActive()` to check if the job is still active.
2. If `IsActive()` returns false, the reconciler clears the workload's
   admission, releasing quota.
3. The released quota becomes available for the next scheduling cycle.

### Test Plan

[ ] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.

#### Prerequisite testing updates

None.

#### Unit tests

- `pkg/controller/jobs/pod`: Test `IsActive()` with each feature gate
  combination, covering:
  - Pod with `deletionTimestamp` and `FastQuotaRelease` enabled returns inactive.
  - Pod with `deletionTimestamp` and `DelayedQuotaRelease` enabled returns
    active (unless in terminal phase).
  - Pod with `deletionTimestamp` and neither gate enabled preserves current
    behavior.
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

#### Alpha (v0.17)

- `FastQuotaRelease` feature gate (disabled by default)
- `DelayedQuotaRelease` feature gate (disabled by default)
- Unit and integration tests covering both modes
- e2e tests for fast quota release preemption scenario
- Backport to v0.15 and v0.16 with feature gates disabled by default

#### Beta (v0.18)

- Replace feature gates with configuration knobs in the Kueue Configuration API
  to select the quota release mode
- `FastQuotaRelease` enabled by default
- Address feedback from Alpha users
- Evaluate whether to extend the configuration to other integrations (e.g., Job)

## Implementation History

## Drawbacks

- Introduces two feature gates for a relatively small behavioral change.
  However, this is warranted because different cluster environments (autoscaled
  vs. fixed-size) have different requirements for when quota should be released.
  The two modes give administrators explicit control over this trade-off.

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