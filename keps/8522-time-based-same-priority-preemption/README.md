# KEP-8522: Time-Based Same-Priority Preemption

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Fair GPU Time-Sharing Among Data Scientists](#story-1-fair-gpu-time-sharing-among-data-scientists)
    - [Story 2: Preventing Resource Monopolization in Shared Clusters](#story-2-preventing-resource-monopolization-in-shared-clusters)
  - [Notes/Constraints/Caveats (Optional)](#notesconstraintscaveats-optional)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API Changes](#api-changes)
  - [Preemption Eligibility](#preemption-eligibility)
  - [Webhook Validation](#webhook-validation)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Beta](#beta)
    - [GA](#ga)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Alternative 1: Dynamic Priority Decay](#alternative-1-dynamic-priority-decay)
  - [Alternative 2: Rely on Admission Fair Sharing Alone](#alternative-2-rely-on-admission-fair-sharing-alone)
  - [Alternative 3: Usage-Based Preemption](#alternative-3-usage-based-preemption)
<!-- /toc -->

## Summary

This KEP introduces time-based same-priority preemption, allowing workloads to
preempt other workloads of equal priority after the target workloads have been
admitted for a minimum duration (`minAdmitDuration`). This addresses the
resource monopolization problem where the first workload admitted to a
ClusterQueue continues to run indefinitely while same-priority workloads
remain pending.

**Terminology**: Throughout this KEP, "quota reservation time" refers to the
`LastTransitionTime` of the workload's `QuotaReserved` condition (when
`Status=True`). This is the moment when quota was reserved for the workload.

## Motivation

Currently, Kueue offers two preemption policies for `withinClusterQueue`:

1. **`LowerPriority`**: Only preempt workloads with strictly lower priority
2. **`LowerOrNewerEqualPriority`**: Preempt workloads with lower priority OR
   workloads with equal priority that were admitted more recently

The `LowerOrNewerEqualPriority` policy was designed to prevent queue starvation
by allowing newly arrived high-demand workloads to preempt recently admitted
same-priority workloads. However, this creates a subtle but significant
problem: **the first workload admitted monopolizes resources indefinitely**.

Consider this scenario:
- WorkloadA (priority=10) is admitted at T=0
- WorkloadB (priority=10) arrives at T=5min
- WorkloadB cannot preempt WorkloadA because WorkloadA is older
- WorkloadA runs for 24 hours while WorkloadB waits

This behavior contradicts the expectation that same-priority workloads should
have fair access to cluster resources over time. In multi-tenant environments,
this can lead to:

- One team's long-running workload blocking other teams indefinitely
- Users submitting artificially low resource requests to get admitted first,
  then consuming resources for extended periods
- Reduced cluster utilization efficiency due to suboptimal scheduling

### Goals

- Extend the `LowerOrNewerEqualPriority` preemption policy with a
  `minAdmitDuration` configuration that allows preemption of same-priority
  workloads after they have been admitted for a specified duration
- Maintain backward compatibility: existing configurations without
  `minAdmitDuration` behave exactly as before

### Non-Goals

- Dynamic priority that decays over time
- Per-workload or per-LocalQueue `minAdmitDuration` overrides
- Precise CPU/GPU time accounting (wall-clock time from admission is used)
- Cross-ClusterQueue time-based preemption (this feature is scoped to
  `withinClusterQueue` only)

## Proposal

Introduce a new configuration structure `WithinClusterQueueConfig` that extends
the `withinClusterQueue` preemption policy with a `minAdmitDuration` field.
When `withinClusterQueue` is set to `LowerOrNewerEqualPriority` and
`minAdmitDuration` is configured, workloads become eligible for same-priority
preemption only after they have been admitted for at least the specified
duration.

### User Stories

#### Story 1: Fair GPU Time-Sharing Among Data Scientists

As a cluster administrator managing GPU resources for a team of 20 data
scientists, I want to ensure that each team member gets fair access to GPU
resources for their training jobs, all of which have the same priority.

Currently, if one data scientist submits a job that starts training and runs
for days, other team members' jobs remain pending indefinitely. With
`minAdmitDuration: 4h`, each job is guaranteed 4 hours of runtime before it
can be preempted by a waiting job.

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: ml-training
spec:
  preemption:
    withinClusterQueue: LowerOrNewerEqualPriority
    withinClusterQueueConfig:
      minAdmitDuration: 4h
```

#### Story 2: Preventing Resource Monopolization in Shared Clusters

As a platform team managing a shared Kubernetes cluster for multiple teams,
I want to prevent any single team from monopolizing cluster resources with
long-running jobs while ensuring jobs have enough time to make meaningful
progress.

By setting `minAdmitDuration: 2h`, we ensure that:
- Jobs have at least 2 hours to run and checkpoint
- No single job can block others indefinitely
- Resource access rotates fairly among all pending workloads

### Notes/Constraints/Caveats (Optional)

The `minAdmitDuration` field modifies the behavior of the
`LowerOrNewerEqualPriority` policy. Without `minAdmitDuration`, the policy
only preempts workloads that are newer than the preempting workload. With
`minAdmitDuration`, it additionally preempts workloads that have exceeded
the minimum duration, regardless of relative age.

This feature is complementary to Fair Sharing (KEP-1714). Fair Sharing
addresses resource distribution across different ClusterQueues/Cohorts via
preemption, while this feature addresses time-sharing among workloads with
identical priority within the same ClusterQueue.

**Interaction with Fair Sharing**: When both Fair Sharing and time-based
preemption are enabled, time-based preemption only applies to the
`withinClusterQueue` policy. Cross-ClusterQueue preemption continues to be
governed by Fair Sharing algorithms. The two mechanisms operate independently
and do not conflict.

This is similar to Slurm's `PreemptExemptTime` which provides a guaranteed
minimum runtime before a job becomes preemptible.

### Risks and Mitigations

**Rapid Preemption Cycles**: If `minAdmitDuration` is set too low, workloads
might be preempted before making meaningful progress. Mitigated by webhook
validation requiring a minimum of 1 minute.

**Checkpoint Dependency**: Workloads that don't implement checkpointing lose
progress when preempted. This is not new; workloads already need to handle
preemption gracefully.

**Controller Restart**: If the Kueue controller restarts, all timing is based
on the persisted `QuotaReserved` condition timestamps stored in workload
status, so workloads maintain their accurate admission time across restarts.
No special handling is required.

## Design Details

### API Changes

Add a new optional field `withinClusterQueueConfig` to `ClusterQueuePreemption`:

```go
type ClusterQueuePreemption struct {
	ReclaimWithinCohort PreemptionPolicy `json:"reclaimWithinCohort,omitempty"`
	BorrowWithinCohort  *BorrowWithinCohort `json:"borrowWithinCohort,omitempty"`
	WithinClusterQueue  PreemptionPolicy `json:"withinClusterQueue,omitempty"`

	// withinClusterQueueConfig provides additional configuration for the
	// withinClusterQueue preemption policy. Only valid when withinClusterQueue
	// is set to LowerOrNewerEqualPriority.
	// +optional
	WithinClusterQueueConfig *WithinClusterQueueConfig `json:"withinClusterQueueConfig,omitempty"`
}

type WithinClusterQueueConfig struct {
	// minAdmitDuration specifies the minimum duration that a workload must be
	// admitted before it becomes eligible for same-priority preemption.
	// The duration is measured from the workload's quota reservation time.
	// If nil or omitted, time-based same-priority preemption is disabled and
	// only the existing behavior applies. When set, the minimum allowed value
	// is 1 minute; values less than 1 minute (including 0) are rejected.
	// +optional
	MinAdmitDuration *metav1.Duration `json:"minAdmitDuration,omitempty"`
}
```

### Preemption Eligibility

Preemption only occurs when there is a pending workload that cannot be
admitted due to insufficient quota. A pending workload can preempt an
admitted same-priority workload if **either** of these conditions is true:

1. **Existing behavior**: The target's quota reservation time is more recent
   than the preemptor's creation time (i.e., the target was admitted after the
   preemptor entered the queue)
2. **New behavior**: The target has exceeded `minAdmitDuration` (i.e.,
   `now - target.quotaReservationTime > minAdmitDuration`)

Note: Condition 1 preserves backward compatibility with the existing
`LowerOrNewerEqualPriority` behavior where a workload that has been waiting
longer in the queue has priority over one that was admitted after the waiting
workload was created.

**Preemption order among same-priority candidates**:
1. Time-expired workloads (exceeded `minAdmitDuration`) are preempted before
   "newer" workloads (admitted after preemptor was created)
2. Among time-expired workloads, the longest-running is preempted first
3. Among "newer" workloads, the most recently admitted is preempted first

This ordering ensures workloads that have had their guaranteed runtime are
preempted before those still within their protected period.

### Webhook Validation

1. `minAdmitDuration` must be at least 1 minute. Values less than 1 minute
   (including explicit `0s` and negative values) are rejected with an error.
2. If `withinClusterQueueConfig` is specified but `minAdmitDuration` is `nil`,
   the configuration is valid but has no effect (time-based preemption is
   disabled, only the existing `LowerOrNewerEqualPriority` behavior applies).
3. `withinClusterQueueConfig` is only valid when `withinClusterQueue` is
   `LowerOrNewerEqualPriority`. Setting it with `Never` or `LowerPriority`
   is rejected with an error indicating the incompatible configuration.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.

#### Unit Tests

- `pkg/scheduler/preemption`: Test preemption candidate selection with
  `minAdmitDuration`
- `apis/kueue/v1beta2/clusterqueue_webhook`: Test validation rules

#### Integration tests

1. Two same-priority workloads competing for quota, first admitted workload
   is preempted after `minAdmitDuration`
2. Multiple workloads queued, fair rotation occurs as each exceeds threshold
3. Mixed priorities: lower priority always preempted before same-priority
4. `minAdmitDuration` not set: existing behavior preserved
5. Edge case: workload exactly at `minAdmitDuration` threshold (should NOT
   be preempted, as comparison requires strictly greater than)
6. Controller restart: workload timestamps persist correctly and preemption
   timing remains accurate
7. Multiple workloads expiring simultaneously: deterministic ordering based
   on longest-running first
8. Interaction with Fair Sharing: time-based preemption only affects
   `withinClusterQueue`, cross-queue preemption follows Fair Sharing rules
9. Webhook validation: reject `minAdmitDuration` < 1 minute, reject config
   when `withinClusterQueue` is `Never` or `LowerPriority`

### Graduation Criteria

#### Beta

This feature is released directly at Beta maturity level. The implementation is
a straightforward extension of the existing `LowerOrNewerEqualPriority`
preemption policy, adding only a single configuration field (`minAdmitDuration`)
with well-defined semantics. No new CRDs or complex API changes are introduced.

- Feature behind `TimeBasedSamePriorityPreemption` feature gate (enabled by
  default)
- `withinClusterQueueConfig.minAdmitDuration` API field implemented
- Preemption logic updated to consider time-expired workloads
- Webhook validation for minimum duration and policy compatibility
- Unit tests for preemption candidate selection
- Integration tests for time-based preemption scenarios
- E2E tests for time-based preemption scenarios
- Documentation for enabling and configuring the feature
- Add a new reason value `InClusterQueueTimeBased` to the existing `reason`
  label on `kueue_preempted_workloads_total` metric to distinguish time-based
  preemptions from priority-based preemptions within the same ClusterQueue

#### GA

- Feature gate removed
- Stable for at least 2 releases
- All reported bugs addressed
- Re-evaluate minimum duration threshold (1 minute) based on user feedback

## Implementation History

- 2026-01-16: Initial KEP proposal

## Drawbacks

- Adds another preemption configuration knob
- Setting `minAdmitDuration` too low could cause thrashing; too high defeats
  the purpose

## Alternatives

### Alternative 1: Dynamic Priority Decay

Implement a system where a workload's effective priority decreases over time.

**Reason for deferral**: More complex to implement and reason about.
Unpredictable preemption timing. Can be considered as a separate future
enhancement.

### Alternative 2: Rely on Admission Fair Sharing Alone

Don't implement time-based preemption; rely on Admission Fair Sharing to
prevent monopolization.

**Reason for rejection**: Admission Fair Sharing controls admission order,
not preemption. A long-running workload already admitted cannot be preempted.
These address different problem spaces and are complementary.

### Alternative 3: Usage-Based Preemption

Measure actual GPU/CPU usage instead of wall-clock time, similar to
AdmissionFairSharing's usage tracking.

**Reason for deferral**: As discussed in the issue, for spare resource
scenarios where GPU utilization is consistently high across all workloads,
usage-based eviction provides little differentiation. Wall-clock time is
simpler to understand, predict, and implement. Usage-based preemption could
be considered as a separate future enhancement for heterogeneous workload
scenarios.
