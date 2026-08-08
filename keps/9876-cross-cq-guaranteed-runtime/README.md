# KEP-9876: Cross-ClusterQueue Minimum Admit Duration

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Guaranteeing Minimum Runtime for GPU Training Jobs](#story-1-guaranteeing-minimum-runtime-for-gpu-training-jobs)
    - [Story 2: Ensuring Checkpoint Completion Before Preemption](#story-2-ensuring-checkpoint-completion-before-preemption)
    - [Story 3: Grace Period for Nominal Quota Reclaim](#story-3-grace-period-for-nominal-quota-reclaim)
  - [Notes/Constraints/Caveats (Optional)](#notesconstraintscaveats-optional)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API Changes](#api-changes)
  - [Preemption Eligibility](#preemption-eligibility)
  - [Future Extensibility](#future-extensibility)
  - [Validation](#validation)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [GA](#ga)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Rely on Within-ClusterQueue Time-Based Preemption Alone](#rely-on-within-clusterqueue-time-based-preemption-alone)
  - [Per-ClusterQueue Configuration](#per-clusterqueue-configuration)
  - [Separate Duration for Each Reclaim Type](#separate-duration-for-each-reclaim-type)
<!-- /toc -->

## Summary

Cross-ClusterQueue preemption can cause workloads to be preempted before they
have had enough time to make meaningful progress. This KEP introduces two
configuration fields that allow administrators to guarantee a minimum runtime
before workloads become eligible for cross-CQ preemption:

- **`fairSharing.minAdmitDuration`**: protects workloads from fair sharing
  rebalancing (`InCohortFairSharing`). Lives in the `FairSharing`
  configuration because it only applies when fair sharing is enabled.
- **`reclaimMinAdmitDuration`**: protects workloads from cross-CQ reclaim
  (`InCohortReclamation` and `InCohortReclaimWhileBorrowing`). This is a
  top-level `Configuration` field because reclaim occurs in both classical
  and fair sharing preemption modes.

When configured, a workload whose `QuotaReserved` condition was set less than
the applicable duration ago is skipped as a preemption candidate.

**Terminology**: Throughout this KEP, "quota reservation time" refers to the
`LastTransitionTime` of the workload's `QuotaReserved` condition (when
`Status=True`). This is the moment when quota was reserved for the workload.

## Motivation

Cross-ClusterQueue preemption in Kueue can happen for several reasons with
fundamentally different semantics:

- **Fair sharing rebalancing** (`InCohortFairSharing`): When fair sharing is
  enabled, Kueue rebalances surplus resources across ClusterQueues within a
  cohort hierarchy by preempting workloads from ClusterQueues that are
  consuming more than their fair share. Neither side has a stronger
  entitlement — this is a fairness negotiation over shared surplus.

- **Nominal quota reclaim** (`InCohortReclamation`): A ClusterQueue reclaims
  resources it owns (its nominal quota) from other ClusterQueues that
  borrowed them. The owner has a clear entitlement to these resources. This
  applies regardless of whether fair sharing is enabled.

- **Reclaim while borrowing** (`InCohortReclaimWhileBorrowing`): A
  ClusterQueue that itself needs to borrow preempts workloads from other
  borrowing ClusterQueues via the `borrowWithinCohort` policy. This applies
  in classical preemption mode.

In all cases, a workload that was just admitted may be preempted before it
has had enough time to make meaningful progress — for example, before reaching
a useful checkpoint or completing a meaningful unit of work.

Administrators need a way to guarantee a minimum runtime for workloads so that
preemption only targets workloads that have already had a reasonable opportunity
to make progress. Because the preemption types carry different strength of
claim, administrators may want different thresholds — for example, a longer
protection window for fair sharing (where neither side has priority) and a
shorter one for reclaim (where the owner has a legitimate entitlement). This
is analogous to Slurm's `PreemptExemptTime`, which provides a guaranteed
minimum runtime before a job becomes preemptible.

### Goals

- Allow administrators to configure a global minimum runtime guarantee for
  workloads before they become eligible for fair sharing preemption, so that
  workloads can reach a useful checkpoint or complete a meaningful unit of work
- Allow administrators to independently configure a global minimum runtime
  guarantee for workloads before they become eligible for cross-CQ reclaim
  preemption (nominal quota reclaim and reclaim while borrowing)
- Maintain backward compatibility: existing configurations without these
  settings (i.e., both `nil`) behave exactly as before

### Non-Goals

- Within-ClusterQueue time-based preemption (addressed by KEP-8522)
- Per-CQ overrides for either duration (deferred to future iterations)
- Per-workload minimum runtime overrides
- Workload priority that decays over time based on runtime
- Precise CPU/GPU time accounting

## Proposal

Cross-CQ preemption candidates are tagged with a preemption reason:
`InCohortFairSharing` for fair sharing rebalancing, `InCohortReclamation`
for nominal quota reclaim, or `InCohortReclaimWhileBorrowing` for reclaim
while borrowing. This KEP adds two optional configuration fields that
together cover all cross-CQ preemption types:

- **`fairSharing.minAdmitDuration`**: protects workloads from fair sharing
  rebalancing preemption. A workload admitted less than this duration ago is
  excluded from `InCohortFairSharing` preemption candidates. Lives in the
  `FairSharing` configuration because it only applies when fair sharing is
  enabled.
- **`reclaimMinAdmitDuration`**: protects workloads from cross-CQ reclaim
  preemption. A workload admitted less than this duration ago is excluded
  from both `InCohortReclamation` and `InCohortReclaimWhileBorrowing`
  preemption candidates. This is a top-level `Configuration` field because
  these reclaim types apply in both classical and fair sharing preemption
  modes.

Both fields default to `nil` (no protection), and each can be set
independently. Having separate fields lets administrators set appropriate
thresholds for each type — for example, a longer window for fair sharing
(where neither side has priority) and a shorter one (or none) for quota
reclaim (where the owner has a stronger entitlement).

Both fields are gated behind the `CrossCQMinAdmitDuration` feature gate.

### User Stories

#### Story 1: Guaranteeing Minimum Runtime for GPU Training Jobs

As a cluster administrator managing GPU resources shared across multiple teams
via ClusterQueues in a cohort, I want to ensure that when fair sharing
preemption rebalances resources between borrowing ClusterQueues, the affected
workloads have had enough time to make meaningful progress.

By setting `minAdmitDuration: 2h` in the fair sharing configuration, workloads
are guaranteed at least 2 hours of runtime before they can be preempted by
another ClusterQueue rebalancing its fair share.

#### Story 2: Ensuring Checkpoint Completion Before Preemption

As a platform team running ML training workloads across multiple teams, I want
workloads to have enough time to reach a checkpoint before fair sharing
preemption can reclaim resources. This avoids wasting the compute that was
already invested in the current training step.

By setting `minAdmitDuration: 30m`, workloads get at least 30 minutes of
uninterrupted runtime, which is typically enough time to complete a training
step and write a checkpoint.

#### Story 3: Grace Period for Nominal Quota Reclaim

As a cluster administrator, I want to give borrowing workloads a short grace
period before a ClusterQueue reclaims its own nominal quota, so that workloads
are not killed immediately when the owner's demand increases.

By setting `reclaimMinAdmitDuration: 10m`, borrowing workloads get at least
10 minutes to make progress before they can be reclaimed. This is shorter than
the fair sharing `minAdmitDuration` because the owner has a legitimate
entitlement to its quota.

### Notes/Constraints/Caveats (Optional)

Each field filters preemption candidates based on their preemption reason.
`fairSharing.minAdmitDuration` applies only to candidates tagged as
`InCohortFairSharing` (fair sharing path only), while
`reclaimMinAdmitDuration` applies to candidates tagged as
`InCohortReclamation` or `InCohortReclaimWhileBorrowing` in both classical
and fair sharing preemption paths. Together, the two fields cover all
cross-CQ preemption types. Within-CQ candidates (`InClusterQueue`) are
unaffected by either field.

This feature is complementary to KEP-8522 (time-based same-priority
preemption). KEP-8522 addresses time-sharing among workloads with identical
priority within the same ClusterQueue, while this feature addresses minimum
runtime guarantees for workloads across ClusterQueues. The two mechanisms
operate independently and do not conflict.

**Interaction with Fair Sharing strategies**: Workloads that have not yet
reached their `minAdmitDuration` are removed from the preemption candidate set.
This applies regardless of which fair sharing strategy is configured.

**Re-admission**: Preempted workloads follow the existing eviction and
re-admission path. When a workload is re-admitted, its `QuotaReserved`
timestamp resets, restarting the applicable duration timer.

### Risks and Mitigations

**Duration set too low**: If either duration is set too low, workloads might
be preempted before making meaningful progress. Mitigated by validation
requiring a minimum of 1 minute for both fields.

**Checkpoint Dependency**: Workloads that don't implement checkpointing lose
progress when preempted. This is not new; workloads already need to handle
preemption gracefully.

**All candidates protected (fair sharing)**: If all fair sharing preemption
candidates in a cohort are still within their `minAdmitDuration` window, fair
sharing preemption is blocked entirely until at least one candidate's duration
expires. This is the intended behavior — the administrator has configured a
minimum runtime guarantee and the system respects it.

**All candidates protected (reclaim)**: Similarly, if all reclaim candidates
are within their `reclaimMinAdmitDuration` window, an owner CQ is temporarily
unable to reclaim its own quota. Administrators should set
`reclaimMinAdmitDuration` conservatively (or leave it `nil`) to avoid
delaying reclaim of owned resources.

**Controller Restart**: All timing is based on the persisted `QuotaReserved`
condition timestamps stored in workload status, so workloads maintain their
accurate admission time across controller restarts. No special handling is
required.

## Design Details

### API Changes

**`minAdmitDuration` in `FairSharing`**: Add a new optional field to the
`FairSharing` struct in `apis/config/v1beta2/configuration_types.go` (and the
corresponding v1beta1 type):

```go
type FairSharing struct {
	PreemptionStrategies []PreemptionStrategy `json:"preemptionStrategies"`

	// minAdmitDuration is the minimum time a workload must be admitted before
	// it becomes eligible for fair sharing (InCohortFairSharing) preemption.
	// A workload whose QuotaReserved condition was set less than
	// minAdmitDuration ago is skipped as a preemption candidate.
	// When nil, no minimum is enforced. If set, must be at least 1 minute.
	// +optional
	MinAdmitDuration *metav1.Duration `json:"minAdmitDuration,omitempty"`
}
```

**`reclaimMinAdmitDuration` on `Configuration`**: Add a new top-level
optional field to the `Configuration` struct. This is top-level because
nominal quota reclaim occurs in both classical and fair sharing preemption
modes:

```go
type Configuration struct {
	// ...existing fields...

	// reclaimMinAdmitDuration is the minimum time a workload must be admitted
	// before it becomes eligible for cross-CQ reclaim preemption
	// (InCohortReclamation and InCohortReclaimWhileBorrowing). A workload
	// whose QuotaReserved condition was set less than reclaimMinAdmitDuration
	// ago is skipped as a preemption candidate. When nil, no minimum is
	// enforced. If set, must be at least 1 minute.
	// Applies to both classical and fair sharing preemption modes.
	// +optional
	ReclaimMinAdmitDuration *metav1.Duration `json:"reclaimMinAdmitDuration,omitempty"`
}
```

YAML example (Kueue configuration):

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
reclaimMinAdmitDuration: 10m
fairSharing:
  preemptionStrategies:
    - LessThanOrEqualToFinalShare
    - LessThanInitialShare
  minAdmitDuration: 1h
```

Both fields default to `nil` independently. Setting only one field protects
workloads from that specific type of cross-CQ preemption while leaving the
other type unaffected.

### Preemption Eligibility

When the `CrossCQMinAdmitDuration` feature gate is enabled, preemption
candidates are filtered based on their preemption reason and the
corresponding duration:

- **`InCohortFairSharing`** candidates: filtered by
  `fairSharing.minAdmitDuration`. A candidate whose time since admission is
  less than `minAdmitDuration` is excluded. Only applies when fair sharing
  is enabled.
- **`InCohortReclamation`** and **`InCohortReclaimWhileBorrowing`**
  candidates: filtered by `reclaimMinAdmitDuration`. A candidate whose time
  since admission is less than `reclaimMinAdmitDuration` is excluded. Applies
  in both classical and fair sharing preemption paths.
- **`InClusterQueue`** candidates: unaffected by either field.
  Within-CQ preemption is governed by KEP-8522.

Admission time is determined by the `LastTransitionTime` of the
`QuotaReserved` condition with `Status=True`. If no eligible candidates
remain after filtering for a given preemption type, that type of preemption
is skipped for the current scheduling cycle.

### Future Extensibility

**Per-CQ reclaim overrides**: A per-CQ `reclaimMinAdmitDuration` on
`ClusterQueuePreemption` (next to `reclaimWithinCohort`) would let each quota
owner control how long borrowers are protected before reclaim of its resources.
The global `reclaimMinAdmitDuration` would serve as the cluster-wide default,
with a per-CQ override taking precedence. This is deferred to a future
iteration to keep the initial implementation simple and to avoid making it
difficult for administrators to reason about why workloads in different
ClusterQueues have different protection windows.

**Per-workload overrides**: A per-workload `minAdmitDuration` override (e.g.,
via an annotation) was considered but deferred. Allowing individual users to
set their own minimum duration introduces the risk of gaming the system by
requesting arbitrarily long protection windows. Mitigating this would require
additional policy controls — such as administrator-defined bounds on allowed
values or delegation to external policy engines like Kyverno — adding
significant complexity. This may be revisited in future iterations if demand
arises.

### Validation

Both `minAdmitDuration` and `reclaimMinAdmitDuration` follow the same
validation rules:

1. Must be at least 1 minute. Values less than 1 minute (including zero and
   negative values) are rejected.
2. `nil` (unset) is valid and means no minimum is enforced (existing behavior).

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.

#### Unit Tests

- `pkg/scheduler/preemption`: Test preemption candidate filtering with both
  duration fields:
  - `minAdmitDuration`: recently admitted candidates are skipped for
    `InCohortFairSharing`; candidates beyond the duration are eligible
  - `reclaimMinAdmitDuration`: recently admitted candidates are skipped for
    `InCohortReclamation`; candidates beyond the duration are eligible
  - When all candidates for a given type are protected, that type of
    preemption does not occur
  - When a duration is `nil`, no filtering occurs for that type
  - One field set, other `nil`: only the configured type is filtered
- `pkg/config`: Test validation rules for both fields:
  - Valid: duration >= 1 minute
  - Valid: duration is `nil`
  - Invalid: duration < 1 minute

#### Integration tests

1. Two ClusterQueues in a cohort with fair sharing enabled, fair sharing
   preemption candidates are protected while within `minAdmitDuration`
2. Nominal quota reclaim (`InCohortReclamation`) candidates are protected
   while within `reclaimMinAdmitDuration` (fair sharing path)
3. Nominal quota reclaim (`InCohortReclamation`) candidates are protected
   while within `reclaimMinAdmitDuration` (classical preemption path)
4. Reclaim while borrowing (`InCohortReclaimWhileBorrowing`) candidates are
   protected while within `reclaimMinAdmitDuration` (classical preemption
   path)
5. One field set, other `nil`: only the configured preemption type is filtered
6. Both fields `nil`: existing behavior preserved
7. Controller restart: workload timestamps persist correctly and preemption
   timing remains accurate
8. Within-CQ preemption is unaffected by either field

### Graduation Criteria

#### Alpha

- Feature behind `CrossCQMinAdmitDuration` feature gate (disabled by
  default)
- `FairSharing.minAdmitDuration` and top-level `reclaimMinAdmitDuration`
  configuration fields implemented
- Preemption logic updated to filter candidates based on admission time and
  preemption reason
- Validation for minimum duration (>= 1 minute) for both fields
- Unit tests for preemption candidate filtering and validation
- Integration tests for both types of cross-CQ preemption with their
  respective duration fields

#### Beta

- Feature gate enabled by default
- Gather feedback from real-world adoption on duration semantics
- E2E tests for cross-CQ preemption with both duration fields
- Documentation for enabling and configuring the feature
- Document tuning guidance for duration values

#### GA

- Feature gate removed
- Stable for at least 2 releases
- All reported bugs addressed
- Re-evaluate minimum duration threshold (1 minute) based on user feedback

## Implementation History

- 2026-03-15: Initial KEP proposal

## Drawbacks

- Adds two configuration knobs to the preemption system
- If `minAdmitDuration` is set too high, fair sharing preemption becomes
  less effective at rebalancing resources across ClusterQueues in a timely
  manner
- If `reclaimMinAdmitDuration` is set too high, quota owners may be unable
  to reclaim their own resources promptly

## Alternatives

### Rely on Within-ClusterQueue Time-Based Preemption Alone

KEP-8522 introduces `minAdmitDuration` for within-ClusterQueue same-priority
preemption. However, it is explicitly scoped to `withinClusterQueue` and does
not affect cross-ClusterQueue preemption. The two operate at different levels
and are complementary.

### Per-ClusterQueue Configuration

Instead of global duration fields, each ClusterQueue could define its own
durations. While this offers finer-grained control, Per-CQ configuration
multiplies the number of interacting knobs and makes it harder to reason
about fair sharing behavior. Global values keep the system predictable:
all workloads in the cluster get the same minimum runtime guarantees,
and administrators do not need to reason about interactions between
different CQ-level durations. This is sufficient for the initial
implementation. Per-CQ overrides can be reconsidered in future iterations
if demand arises.

### Separate Duration for Each Reclaim Type

A third field, `reclaimWhileBorrowingMinAdmitDuration`, could give
administrators independent control over all three cross-CQ preemption types.
However, three duration knobs is too many to reason about. The distinction
between "reclaim while within nominal" (`InCohortReclamation`) and "reclaim
while borrowing" (`InCohortReclaimWhileBorrowing`) is an implementation detail
that most administrators do not need to configure separately. Grouping both
reclaim types under a single `reclaimMinAdmitDuration` keeps the configuration
surface manageable while still distinguishing the fundamentally different
cases: fairness negotiation versus reclaim.
