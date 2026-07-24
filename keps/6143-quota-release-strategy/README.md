# KEP-6143: Quota Release Strategy

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Preemption of &quot;plain pod&quot; workloads](#story-1-preemption-of-plain-pod-workloads)
    - [Story 2: ClusterQueue migration to new clusters](#story-2-clusterqueue-migration-to-new-clusters)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [Node churn could be increased in cluster autoscaling environment.](#node-churn-could-be-increased-in-cluster-autoscaling-environment)
    - [Discrepancy could occur from actual available quota](#discrepancy-could-occur-from-actual-available-quota)
- [Design Details](#design-details)
  - [Implementation overview](#implementation-overview)
  - [Compatibility &amp; Defaulting](#compatibility--defaulting)
  - [Integration Signals &amp; Stuck Pods](#integration-signals--stuck-pods)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha (v0.20)](#alpha-v020)
    - [Beta (v0.21)](#beta-v021)
- [Implementation History](#implementation-history)
- [Alternatives](#alternatives)
  - [Modify the generic reconciler instead of the integration controllers](#modify-the-generic-reconciler-instead-of-the-integration-controllers)
  - [Feature gates instead of Configuration API](#feature-gates-instead-of-configuration-api)
<!-- /toc -->

## Summary

This KEP aims to standardize and configure the quota release strategy for terminating jobs.
Currently, the Pod integration holds quota until pods reach a terminal phase,
while most other integrations (e.g., batch/v1 Job) release quota as soon as
pods begin terminating. This inconsistency leads to unnecessarily delayed
admission and serialized preemption for Pod workloads. On the other hand,
TopologyAwareScheduling (TAS) requires quota to be held until underlying pods
are fully terminated to prevent scheduling failures on physically full nodes.
To address both use cases, this KEP introduces a `.scheduling.quotaReleaseStrategy`
Configuration API knob that allows administrators to select between different
quota release strategies across all integrations.

## Motivation

When Kueue preempts a Pod-based workload, the current Pod integration holds
onto its quota reservation until all Pods have fully terminated (reached
`Succeeded` or `Failed` phase). This termination often takes tens of seconds
or even minutes (and has no practical upper bound, depending on how the pod's
graceful shutdown period is configured). While the pod is terminating, ALL
pods in each cluster queue is blocked from triggering further preemptions until
it's finished because the terminating pod is always each potential preemptor pod's
ideal preemption target. This creates a large bottleneck for preemption
that does not occur for preemptions of other types of workloads.

Ultimately, this behavioral inconsistency between integrations leads to:
- Delayed admission of higher-priority workloads during preemption
- Serialized preemption, where each ClusterQueue is head-of-line blocking causing
  the scheduler to wait for one preemption within the ClusterQueue to fully complete
  before starting the next

### Goals

- Introduce a `.scheduling.quotaReleaseStrategy` Configuration API knob
  that allows administrators to select when quota is released (either fast on
  `deletionTimestamp` or delayed until terminal state).
- Standardize the quota release behavior across all integrations (Pod, Job, JobSet, etc.)
  based on this global configuration.



## Proposal

Introduce a `.scheduling.quotaReleaseStrategy` field to the Kueue Configuration
API with two supported modes:

1. `OnTermination`: (Default) Quota is released as soon as workloads are marked
   terminating (e.g., all Pods have a `deletionTimestamp`). This preserves the
   existing behavior of the Job integration and allows fast readmission of
   preempted workloads.
2. `OnTerminalBestEffort`: Quota is held until underlying pods are fully
   terminated (completely dead and no longer consuming resources). This is critical
   for TAS workloads where hardware constraints prevent new pods from running
   until old pods physically release the hardware.

The implementation will delegate to each integration's `job.IsActive()` function
to respect this global configuration.

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

#### Node churn could be increased in cluster autoscaling environment.
**Risk**: Fast quota release could cause an increase in node churn if the
cluster autoscaler is being used. While pods are pending, the cluster
autoscaler may trigger scale-up even though pods could be terminating on
existing nodes.

**Mitigation**: This is the same behavior that already exists for the Job
integration and other integrations, so it's a well established/understood
behavior.

#### Discrepancy could occur from actual available quota
**Risk**: With fast quota release (`OnTermination`), if a pod gets "stuck" in a terminating
state, there may be a discrepancy between the amount of quota available and the
actual resources available on the cluster. In fixed-size clusters where the sum
of nominal quotas strictly equals cluster capacity, this can cause temporary
capacity oversubscription.

**Mitigation**: Administrators running fixed-size bare-metal clusters (like
TAS environments) can configure the `OnTerminalBestEffort` strategy.
Additionally, [setup failure recovery](https://kueue.sigs.k8s.io/docs/tasks/manage/setup_failure_recovery/)
provides a failure recovery mechanism that automatically transitions
pods into the Failed phase when they are assigned to unreachable nodes and stuck
terminating.

## Design Details

The `.scheduling.quotaReleaseStrategy` Configuration API will be introduced in
v0.20 as an Alpha feature.

### Implementation overview

The `Configuration` struct in `apis/config/v1beta1/configuration_types.go` will
be updated to include a `Scheduling` struct containing the `QuotaReleaseStrategy`
field.

This global configuration will be passed down to the `job.IsActive()` function
of each integration (e.g., Pod, Job, JobSet) via the `JobReconciler` context.

When `OnTerminalBestEffort` is selected, `IsActive()` will be modified to return
true until all underlying pods have fully transitioned out of the terminating
state (i.e., they are physically dead). When `OnTermination` is selected,
`IsActive()` will return false as soon as the workload begins terminating
(e.g., `deletionTimestamp` is present).

No changes are needed to the generic reconciler. The existing flow already
handles this:

1. When a workload is evicted (e.g., by preemption), the reconciler calls
   `job.IsActive()` to check if the job is still active.
2. If `IsActive()` returns false, the reconciler clears the workload's
   admission, releasing quota.
3. The released quota becomes available for the next scheduling cycle.

### Compatibility & Defaulting

Making `OnTermination` the default configuration introduces a behavioral change
for existing Pod integration workloads upon upgrading. Previously, the Pod
integration held quota until the grace period expired or the pods reached a
terminal phase. With `OnTermination`, quota is released immediately upon
termination. 

Administrators relying on exact physical capacity guarantees (such as TAS users)
**must** explicitly configure `.scheduling.quotaReleaseStrategy = OnTerminalBestEffort`
in their Kueue Configuration when upgrading to ensure their capacity guarantees
are preserved.

### Integration Signals & Stuck Pods

Integrations will map the configuration strategies as follows:
- **`OnTermination`**: Integrations that wrap upstream controllers (like Job and
  JobSet) will simply return their upstream controller's status (e.g., `Job.status.active`).
  Upstream controllers natively ignore terminating pods in these counts.
- **`OnTerminalBestEffort`**: Integrations will actively inspect the underlying
  Pods (or wait for the upstream controller's terminal status) to verify they
  have physically transitioned out of the terminating state.

**Stuck Pod Fallback:** Under `OnTerminalBestEffort`, if a hardware failure
causes a pod to get stuck terminating indefinitely beyond its grace period,
Kueue will intentionally hold the quota indefinitely. Releasing the quota based
on a grace period timeout would violate TAS capacity constraints. Administrators
must use Kueue's [setup failure recovery](https://kueue.sigs.k8s.io/docs/tasks/manage/setup_failure_recovery/)
mechanism (or manual intervention) to forcefully remove the stuck pod, which
will subsequently release the quota.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.

#### Prerequisite testing updates

None.

#### Unit tests

- `pkg/controller/jobs/pod` & `pkg/controller/jobs/job`: Test `IsActive()`
  with both configuration strategies, covering:
  - Pod with `deletionTimestamp` and `OnTermination` returns inactive.
  - Pod with `deletionTimestamp` and `OnTerminalBestEffort` returns active.
  - Mixed groups with some terminating and some running Pods.

#### Integration tests

- Test that when a workload is evicted with `OnTermination`, quota is
  released as soon as all Pods have a `deletionTimestamp`.
- Test that when a workload is evicted with `OnTerminalBestEffort`, quota
  is held until all Pods are fully terminated.

#### e2e tests

None required for Alpha.

### Graduation Criteria

#### Alpha (v0.20)

- `.scheduling.quotaReleaseStrategy` Configuration API introduced with
  `OnTermination` and `OnTerminalBestEffort` modes.
- Core integrations (Pod, Job, JobSet) support the API.

#### Beta (v0.21)

- Address feedback from Alpha users.
- Support added for all remaining integrations.

## Implementation History

## Alternatives

### Modify the generic reconciler instead of the integration controllers

Instead of changing the `IsActive()` logic within each integration, the reconciler
could be modified to release quota based on the strategy. However, the
`job.IsActive()` approach was preferred because:
- It keeps the change scoped and allows integrations to define their own terminal states.
- The job-specific `IsActive()` implementation is the established pattern for
  controlling when to release quota.

### Feature gates instead of Configuration API

We originally considered introducing a `FastQuotaRelease` feature gate just for
the Pod integration. This approach was rejected because it did not address the
urgent TAS capacity gap for other integrations (like Job and JobSet), which
require a global `OnTerminalBestEffort` configuration to delay quota release
until hardware is physically freed.
