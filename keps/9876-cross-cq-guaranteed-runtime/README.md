# KEP-9876: Preemption Protection — Guaranteed Minimum Runtime for Cross-ClusterQueue Preemption

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Guaranteeing Checkpoint Completion Before Fair Sharing Preemption](#story-1-guaranteeing-checkpoint-completion-before-fair-sharing-preemption)
    - [Story 2: Grace Period for Nominal Quota Reclaim](#story-2-grace-period-for-nominal-quota-reclaim)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Cross-CQ Preemption Types](#cross-cq-preemption-types)
  - [API Changes](#api-changes)
  - [Admission Time](#admission-time)
  - [Preemption Eligibility](#preemption-eligibility)
  - [Interaction with the Preemption Oracle](#interaction-with-the-preemption-oracle)
  - [Retrying After Protection Expiry](#retrying-after-protection-expiry)
  - [Observability](#observability)
  - [Validation](#validation)
  - [Future Extensibility](#future-extensibility)
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
  - [Scatter fields across existing Configuration blocks](#scatter-fields-across-existing-configuration-blocks)
  - [List-based preemptionProtection API](#list-based-preemptionprotection-api)
  - [Per-ClusterQueue configuration](#per-clusterqueue-configuration)
  - [Exempt candidates admitted while the preemptor was pending](#exempt-candidates-admitted-while-the-preemptor-was-pending)
  - [Count cumulative runtime across admissions](#count-cumulative-runtime-across-admissions)
  - [Rely on gracefulTerminationPeriod or terminationGracePeriodSeconds](#rely-on-gracefulterminationperiod-or-terminationgraceperiodseconds)
  - [Per-workload overrides](#per-workload-overrides)
  - [Separate duration for each reclaim type](#separate-duration-for-each-reclaim-type)
  - [Rely on within-ClusterQueue time-based preemption alone](#rely-on-within-clusterqueue-time-based-preemption-alone)
<!-- /toc -->

## Summary

This KEP introduces preemption protection: configuration that allows
administrators to guarantee a minimum runtime before admitted workloads
become eligible for cross-ClusterQueue preemption, both as a result of
fair sharing rebalancing and of quota reclamation.

## Motivation

Cross-ClusterQueue preemption in Kueue happens for several reasons — fair
sharing rebalancing across a cohort, reclaim of nominal quota by its owner,
and reclaim while borrowing (see
[Cross-CQ Preemption Types](#cross-cq-preemption-types) for their exact
semantics). In all cases, a workload that was just admitted may be preempted before it
has had enough time to make meaningful progress — for example, before
reaching a checkpoint.

Administrators need a way to guarantee a minimum runtime so that preemption
only targets workloads that have already had a reasonable opportunity to make
progress. Because the preemption types carry different strengths of claim,
administrators may want different thresholds — a longer protection window for
fair sharing (where neither side has priority) and a shorter one, or none,
for reclaim (where the owner has a legitimate entitlement).

### Goals

- Allow administrators to configure a global minimum runtime guarantee for
  workloads before they become eligible for fair sharing preemption
- Allow administrators to independently configure a global minimum runtime
  guarantee for workloads before they become eligible for cross-CQ reclaim
  preemption (nominal quota reclaim and reclaim while borrowing)
- Shape the API so that future protection rules (for example,
  within-ClusterQueue protection) have an obvious place to live
- Maintain backward compatibility: existing configurations without these
  settings behave exactly as before

### Non-Goals

- Within-ClusterQueue time-based preemption (addressed by KEP-8522; see
  [Alternatives](#rely-on-within-clusterqueue-time-based-preemption-alone))
- Per-ClusterQueue overrides for either duration (see
  [Future Extensibility](#future-extensibility))
- Per-workload minimum runtime overrides
- Workload priority that decays over time based on runtime
- Precise CPU/GPU time accounting

## Proposal

Introduce a grouped preemption-protection section in the Kueue Configuration
that carries two rules, each with a single minimum-admit-duration setting:

- protection from **fair sharing rebalancing**: a workload admitted less than
  the configured duration ago is excluded from `InCohortFairSharing`
  preemption candidates;
- protection from **cross-CQ reclaim**: a workload admitted less than the
  configured duration ago is excluded from `InCohortReclamation` and
  `InCohortReclaimWhileBorrowing` preemption candidates.

Both rules default to disabled and can be set independently. Grouping them in
one place keeps related rules together rather than scattered across the
Configuration, and gives future protection rules an obvious extension point.

Protection is measured from the workload's admission time — the moment Kueue
allows it to start — and the feature is gated behind a feature gate,
disabled by default in its Alpha release.

This is analogous to Slurm's `PreemptExemptTime`, which provides a guaranteed
minimum runtime before a job becomes preemptible.

### User Stories

Both settings are global (cluster-wide), so each story describes a single
cluster-wide policy.

#### Story 1: Guaranteeing Checkpoint Completion Before Fair Sharing Preemption

As a cluster administrator managing GPU resources shared across multiple
teams via ClusterQueues in a cohort, I want workloads to have enough time to
reach a checkpoint before fair sharing rebalancing can reclaim resources, so
that the compute already invested in the current training step is not wasted.

By configuring a cluster-wide fair-sharing protection duration of 1 hour,
every workload is guaranteed at least 1 hour of uninterrupted runtime —
enough for our training jobs to complete a step and write a checkpoint —
before another ClusterQueue can preempt it to rebalance its fair share.

#### Story 2: Grace Period for Nominal Quota Reclaim

As a cluster administrator, I want to give borrowing workloads a short grace
period before a ClusterQueue reclaims its own nominal quota, so that
workloads are not killed moments after starting when the owner's demand
increases.

By configuring a cluster-wide reclaim protection duration of 10 minutes,
borrowing workloads get at least 10 minutes before they can be reclaimed.
Because the window is measured from admission — before pods are scheduled
and container images are pulled — this is enough time for image pulls to
complete and for the workload to initialize and start doing useful work,
rather than being reclaimed while still starting up. This is deliberately
shorter than the fair sharing protection
in Story 1: borrowed resources should be returned to the owner's nominal
quota promptly, while usage negotiated purely by fairness can afford a longer
protection budget.

### Notes/Constraints/Caveats

Workloads that don't implement checkpointing lose progress when preempted.
This is not new; workloads already need to handle preemption gracefully.
Protection reduces how often that happens but does not remove the need.

This feature is complementary to KEP-8522 (time-based same-priority
preemption within a ClusterQueue). KEP-8522 addresses time-sharing among
workloads within the same ClusterQueue, while this feature addresses minimum
runtime guarantees across ClusterQueues. The two mechanisms operate
independently and do not conflict; the protection rules are grouped so that
a within-ClusterQueue rule can slot in later (see
[Future Extensibility](#future-extensibility)).

It is also complementary to the `SchedulerTimestampPreemptionBuffer` feature
gate, which adds a fixed 5-minute buffer preventing near-simultaneous
equal-priority workloads from preempting each other within a ClusterQueue.
Preemption protection provides an administrator-configurable, cross-CQ
guarantee measured from admission time rather than queue timestamps.

**Interaction with Fair Sharing strategies**: protected cross-CQ candidates
are excluded during target classification, before the configured
`preemptionStrategies` are applied to them; `InClusterQueue` candidates are
never affected.

**Re-admission**: preempted workloads follow the existing eviction and
re-admission path. On re-admission the workload's admission time is set
afresh, restarting the protection window.

### Risks and Mitigations

**Fair sharing convergence is delayed**: if the fair-sharing protection
duration is set high, surplus resources may remain imbalanced across the
cohort for extended periods, because rebalancing preemptions can only target
workloads that have exceeded their protection window. This is the intended
trade-off; documentation will include tuning guidance (typically: slightly
above the workloads' checkpoint interval).

**Reclaim can be starved under borrower churn**: if all reclaim candidates
are within their protection window, an owner CQ must wait to get its own
nominal quota back — and because every newly admitted borrower starts a
fresh protection window, a sustained stream of short-lived borrower
admissions can delay reclaim indefinitely, not just by one window. Two
design elements bound this risk: while an owner's reclaim fails solely due
to protection, the scheduler reserves the contested capacity within the
scheduling cycle rather than admitting new borrowers onto it (see
[Preemption Eligibility](#preemption-eligibility) for the `CanAlwaysReclaim`
interaction), and the pending owner is retried when the earliest protection
window expires (see
[Retrying After Protection Expiry](#retrying-after-protection-expiry)).
Administrators should additionally keep the reclaim protection duration
short (or unset). An alternative that exempts late-admitted borrowers
entirely is discussed in
[Alternatives](#exempt-candidates-admitted-while-the-preemptor-was-pending).

## Design Details

### Cross-CQ Preemption Types

The preemption types this KEP protects against have fundamentally different
semantics:

- **Fair sharing rebalancing** (`InCohortFairSharing`): when Fair Sharing is
  enabled, Kueue rebalances surplus resources across ClusterQueues within a
  cohort hierarchy by preempting workloads from ClusterQueues that consume
  more than their fair share. Neither side has a stronger entitlement — this
  is a fairness negotiation over shared surplus.

- **Nominal quota reclaim** (`InCohortReclamation`): a ClusterQueue reclaims
  resources it owns (its nominal quota) from other ClusterQueues that
  borrowed them. The owner has a clear entitlement. This applies in
  classical preemption mode and, via the `FairSharingPreemptWithinNominal`
  feature gate (enabled by default since v0.17), in fair sharing mode.

- **Reclaim while borrowing** (`InCohortReclaimWhileBorrowing`): a
  ClusterQueue that itself needs to borrow preempts workloads from other
  borrowing ClusterQueues via the `borrowWithinCohort` policy (classical
  preemption mode).

### API Changes

A new optional `preemptionProtection` block on the `Configuration` struct in
`apis/config/v1beta2`. Following the precedent of recent Configuration
additions (for example `quotaCheckStrategy`), the field is **not** mirrored
into the deprecated `v1beta1` Configuration; the `v1beta1` conversion
functions are regenerated to record the new field as v1beta2-only.

```go
type Configuration struct {
	// ...existing fields...

	// preemptionProtection groups rules that protect admitted workloads
	// from preemption until they have run for a minimum duration.
	// It has no effect unless the PreemptionProtection feature gate is
	// enabled.
	// +optional
	PreemptionProtection *PreemptionProtection `json:"preemptionProtection,omitempty"`
}

// PreemptionProtection groups preemption-protection rules by the type of
// preemption they protect against.
type PreemptionProtection struct {
	// fairSharing protects workloads from fair sharing rebalancing
	// preemption (InCohortFairSharing). It only has an effect when
	// fair sharing is enabled.
	// +optional
	FairSharing *PreemptionProtectionPolicy `json:"fairSharing,omitempty"`

	// reclaimWithinCohort protects workloads from cross-ClusterQueue
	// reclaim preemption (InCohortReclamation and
	// InCohortReclaimWhileBorrowing). It applies in classical preemption
	// mode and, via the FairSharingPreemptWithinNominal feature gate
	// (enabled by default), in fair sharing mode.
	// +optional
	ReclaimWithinCohort *PreemptionProtectionPolicy `json:"reclaimWithinCohort,omitempty"`
}

// PreemptionProtectionPolicy defines a single preemption-protection rule.
type PreemptionProtectionPolicy struct {
	// minAdmitDuration is the minimum time a workload must have been
	// admitted (Admitted condition set to True) before it becomes
	// eligible for this type of preemption. A workload whose runtime
	// since admission is less than minAdmitDuration is skipped as a
	// preemption candidate. When nil, no minimum is enforced.
	// If set, it must be greater than zero.
	// +optional
	MinAdmitDuration *metav1.Duration `json:"minAdmitDuration,omitempty"`
}
```

YAML example (Kueue configuration):

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
preemptionProtection:
  fairSharing:
    minAdmitDuration: 1h
  reclaimWithinCohort:
    minAdmitDuration: 10m
fairSharing:
  preemptionStrategies:
    - LessThanOrEqualToFinalShare
    - LessThanInitialShare
```

Design notes:

- The two rules share one `PreemptionProtectionPolicy` type. A rule-scoped
  struct (rather than bare duration fields) gives each rule room to grow —
  for example resource-dependent protection — without another API round.
- The group name `reclaimWithinCohort` intentionally matches the existing
  `ClusterQueue.spec.preemption.reclaimWithinCohort` field: it protects
  against the reclaim preemptions (`InCohortReclamation` and
  `InCohortReclaimWhileBorrowing`) that policy and `borrowWithinCohort`
  enable. (In fair sharing mode the same ClusterQueue policy also gates
  collection of candidates that become `InCohortFairSharing` targets; those
  are governed by the `fairSharing` rule instead.)
- Because this is Configuration API only, no CRD schema changes, no CRD
  conversion webhooks, no client-go regeneration, and no ClusterQueue
  webhook validation are needed. The change surface is deliberately small:
  the config types, regenerated `apis/config/v1beta1` conversion functions,
  config validation, one feature gate, and candidate filtering in
  `pkg/scheduler/preemption`.

### Admission Time

Protection timing is based on the `LastTransitionTime` of the workload's
`Admitted` condition (`Status=True`) — the moment Kueue allows the workload
to start (the job is unsuspended) — not the `QuotaReserved` condition. The
distinction matters:

- In two-phase admission, AdmissionChecks (for example a ProvisioningRequest
  handled by the Cluster Autoscaler) can take a long time between
  `QuotaReserved` and `Admitted`. Counting from `QuotaReserved` could consume
  the entire protection budget before the workload runs a single pod.
- In two-pass scheduling (Topology-Aware Scheduling), a workload can be
  `QuotaReserved` in the first pass and only become `Admitted` after
  topology assignment in the second pass.

Note that pod scheduling and startup happen after `Admitted`, so pod startup
time counts against the protection window; administrators should size
durations accordingly.

If the `Admitted` condition is not `True`, the workload is not protected by
this mechanism: it is not running yet, so it has no runtime to protect.
(Such workloads still hold quota; preempting them wastes no runtime
progress, though it may discard in-flight provisioning work, which is out of
scope for a runtime-protection mechanism.)

Eviction does not flip `Admitted` immediately: an evicted workload carries
`Evicted=True` alongside `Admitted=True` until its quota reservation is
released, and such workloads are deliberately the *preferred* preemption
victims (preempting them costs nothing extra). Protection therefore applies
only to candidates with `Admitted=True` and not `Evicted=True`. When the
workload is later re-admitted, `Admitted` transitions back to `True` with a
fresh timestamp, so the protection window always refers to the current
uninterrupted run. Because the condition and its timestamp are persisted in
the workload status, protection windows survive controller restarts with no
special handling.

### Preemption Eligibility

A workload is *protected* from a given preemption type while:

```
now - Admitted.lastTransitionTime < minAdmitDuration
```

It becomes eligible once its runtime is greater than or equal to
`minAdmitDuration`. Candidates with `Evicted=True` are never protected (see
[Admission Time](#admission-time)).

When the `PreemptionProtection` feature gate is enabled, preemption
candidates are filtered at the point where their preemption type is known:

- **Classical path**: cross-CQ candidates are classified in
  `classifyPreemptionVariant`
  (`pkg/scheduler/preemption/classical/hierarchical_preemption.go`). A
  cross-CQ candidate (reclaim, with or without borrowing) that is still
  within `reclaimWithinCohort.minAdmitDuration` is treated as not satisfying
  the preemption policy and is skipped.
- **Fair sharing path**: targets are selected in `runFirstFsStrategy` /
  `runSecondFsStrategy` (`pkg/scheduler/preemption/preemption.go`).
  Candidates that would become `InCohortFairSharing` targets are checked
  against `fairSharing.minAdmitDuration`; candidates that would become
  `InCohortReclamation` targets (the `FairSharingPreemptWithinNominal` path)
  are checked against `reclaimWithinCohort.minAdmitDuration`. If the
  `FairSharingPreemptWithinNominal` gate is disabled, fair sharing mode
  never produces reclaim-typed preemptions: all cross-CQ fair-sharing
  preemptions — including an owner reclaiming nominal quota — carry
  `InCohortFairSharing` and are governed by `fairSharing.minAdmitDuration`.
- **`InClusterQueue` candidates are unaffected** on both paths. Within-CQ
  protection is the subject of KEP-8522.

The current time is taken once per scheduling attempt from the preemptor's
injected clock, keeping the behavior deterministic and testable with a fake
clock.

If no eligible candidates remain after filtering for a given preemption
type, that type of preemption is simply not available in the current cycle:
the incoming workload follows the existing no-candidates behavior (requeued
with `RequeueReasonPreemptionNoCandidates`).

One existing assumption needs a targeted update: `CanAlwaysReclaim`
(`pkg/scheduler/preemption/policy.go`) reports that a ClusterQueue with
`reclaimWithinCohort: Any` can always reclaim its nominal quota later, which
lets the scheduler skip reserving capacity for its pending workloads
(`reserveCapacityForUnreclaimablePreempt`, `pkg/scheduler/scheduler.go`).
Reclaim protection breaks that premise while candidates are protected. When
a reclaim protection duration is configured, `CanAlwaysReclaim` will return
`false`, so the existing capacity-reservation path prevents new borrowers
from being admitted onto contested capacity within the same scheduling
cycle while the owner waits out protection windows.

### Interaction with the Preemption Oracle

During flavor assignment, the scheduler consults the preemption oracle
(`SimulatePreemption`, `pkg/scheduler/preemption/preemption_oracle.go`) to
determine whether preemption or reclaim is possible for a flavor. The oracle
runs the same target-selection code (`getTargets`) that later computes real
victims, so filtering inside that shared path automatically propagates:
a flavor whose only candidates are protected reports no candidates, which
makes the flavor assigner try subsequent flavors; if none fit, the workload
keeps mode `Preempt` with zero targets and the scheduler requeues it with
the existing no-candidates behavior described above. No oracle-specific
changes are required.

### Retrying After Protection Expiry

A workload requeued because all candidates were protected is placed in the
inadmissible set, which today is only retried on cluster events (workload
finished/evicted/updated, ClusterQueue or Cohort changes). Protection expiry
is a purely time-based event that produces no such trigger, so in a quiet
cohort the preemptor could wait far longer than the protection window.

To keep the guarantee "protected for the duration" from silently becoming
"blocked until the next unrelated event", target selection records the
earliest `Admitted + minAdmitDuration` among candidates skipped due to
protection, and the scheduler schedules a re-queue of the pending workload's
ClusterQueue at that time (following the delayed-requeue precedent of the
TAS second-pass queue). This is a liveness optimization: correctness does
not depend on the timer, and a conservative implementation (for example,
rounding expiry times up to a coarse interval) is acceptable at Alpha.

### Observability

When a candidate is skipped due to protection, the scheduler logs it at
`V(4)` with the workload, the preemption type, and the remaining protection
time — matching the existing fair-sharing strategy-evaluation logging. This
ships at Alpha: without it, "preemption skipped because protected" is
indistinguishable from "no viable victims" when debugging why an owner CQ is
not reclaiming. At Beta, the pending workload's requeue message is extended
to mention how many candidates were protected, and a metric for
protection-skipped candidates will be added based on user feedback.

### Validation

Validation lives in `pkg/config/validation.go`, alongside the existing
fair-sharing configuration validation:

1. Each `minAdmitDuration`, when set, must be greater than zero. Zero and
   negative values are rejected.
2. `nil` (unset) is valid at every level and means no protection (existing
   behavior).
3. The fields are validated regardless of the feature-gate state, and are
   inert at runtime while the `PreemptionProtection` gate is disabled —
   matching the `admissionFairSharing` precedent.

There is deliberately **no minimum threshold** (such as 1 minute): the
effective value today is `0s`, any positive duration is meaningful, and a
floor would complicate integration and e2e tests. This mirrors
`waitForPodsReady`, which has no such floor.

### Future Extensibility

**Within-ClusterQueue protection**: KEP-8522 proposes time-based
same-priority preemption within a ClusterQueue, including distinguishing
incumbent workloads from opportunistically admitted ones (queued after the
preemptor but admitted first, e.g., via BestEffortFIFO). If a global default
for that mechanism is desired, it has a natural home here as
`preemptionProtection.withinClusterQueue`, reusing
`PreemptionProtectionPolicy` (possibly extended with an
`opportunisticMinAdmitDuration`).

**Per-ClusterQueue overrides**: a per-CQ override could later take precedence
over the global value. Note that a per-CQ *reclaim* protection set by the
borrower effectively taxes the lender, so such an extension must include a
lender-side guard — analogous to how `lendingLimit` bounds what
`borrowingLimit` can take — letting quota owners cap how long borrowers may
be protected against reclaim of their resources. This is deferred until
there is a concrete need; the global knobs keep the initial system easy to
reason about.

**Per-rule refinements**: because each rule is a struct, refinements such as
resource-dependent protection (for example, reducing the guaranteed runtime
for workloads holding scarce resources) can be added per rule without
breaking the API.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.

#### Unit Tests

- `pkg/scheduler/preemption`: candidate filtering with a fake clock:
  - `fairSharing.minAdmitDuration`: recently admitted candidates are skipped
    for `InCohortFairSharing`; candidates at or beyond the duration are
    eligible (boundary: runtime exactly equal to the duration is eligible)
  - `reclaimWithinCohort.minAdmitDuration`: recently admitted candidates are
    skipped for `InCohortReclamation` (classical path, and fair sharing path
    with `FairSharingPreemptWithinNominal` enabled) and
    `InCohortReclaimWhileBorrowing` (classical path)
  - Candidates without an `Admitted=True` condition are not protected
  - Candidates with `Evicted=True` are not protected even when recently
    admitted
  - When all candidates for a given type are protected, that type of
    preemption does not occur; `InClusterQueue` preemption is unaffected
  - When a rule is `nil`, no filtering occurs for that type; one rule set
    and the other `nil` filters only the configured type
  - With the `PreemptionProtection` gate disabled, configured rules have no
    effect
  - Oracle consistency: `SimulatePreemption` reports no candidates when all
    candidates for the flavor are protected
  - `CanAlwaysReclaim` returns `false` when a reclaim protection duration is
    configured
- `pkg/config`: validation rules:
  - Valid: positive duration; `nil` at each level
  - Invalid: zero or negative duration
  - Fields are validated even when the feature gate is disabled

#### Integration tests

1. Two ClusterQueues in a cohort with fair sharing enabled: fair sharing
   preemption candidates are protected while within
   `fairSharing.minAdmitDuration`, and become preemptible after it elapses
2. Nominal quota reclaim (`InCohortReclamation`) candidates are protected
   while within `reclaimWithinCohort.minAdmitDuration` (fair sharing path,
   with `FairSharingPreemptWithinNominal` enabled)
3. Nominal quota reclaim candidates are protected (classical path)
4. Reclaim while borrowing (`InCohortReclaimWhileBorrowing`) candidates are
   protected (classical path)
5. Only the configured preemption type is filtered: with a single rule set,
   the other cross-CQ type and within-CQ preemption are unaffected
6. Two-phase admission: a workload whose AdmissionCheck delayed `Admitted`
   is protected for the full duration counted from `Admitted`
7. A pending reclaimer is retried once the candidates' protection windows
   expire, without requiring an unrelated cluster event
8. While an owner's reclaim is blocked only by protection, a new workload
   in another borrowing CQ is not admitted onto the contested capacity in
   the same scheduling cycle

### Graduation Criteria

#### Alpha

- Feature behind the `PreemptionProtection` feature gate (disabled by
  default)
- `preemptionProtection` Configuration block with `fairSharing` and
  `reclaimWithinCohort` rules
- Preemption candidate filtering by admission time and preemption type on
  both classical and fair sharing paths, excluding already-evicted
  candidates
- `CanAlwaysReclaim` accounts for configured reclaim protection
- Retry of pending preemptors on protection expiry
- `V(4)` logging for protection-skipped candidates
- Validation (positive durations)
- Unit and integration tests above

#### Beta

- Feature gate enabled by default
- Gather feedback from real-world adoption on duration semantics
- E2E tests for cross-CQ preemption with both rules
- Documentation, including tuning guidance for duration values
- Requeue message mentions protected candidates; add a metric for
  protection-skipped candidates based on user feedback

#### GA

- Feature gate removed
- Stable for at least 2 releases
- All reported bugs addressed

## Implementation History

- 2026-03-15: Initial KEP proposal (`fairSharing.minAdmitDuration` +
  top-level `reclaimMinAdmitDuration`, `QuotaReserved`-based timing)
- 2026-07-04: Revised per reviewer feedback: grouped the rules under a
  `preemptionProtection` Configuration block, switched timing to the
  `Admitted` condition, dropped the 1-minute validation floor, single
  `PreemptionProtection` feature gate, documented within-CQ extensibility;
  added starvation mitigations (`CanAlwaysReclaim` interaction, expiry
  retry), evicted-candidate exclusion, and Alpha observability; moved the
  preemption-type background from Motivation into Design Details

## Drawbacks

- Adds configuration knobs to the preemption system
- If the fair-sharing protection duration is set too high, fair sharing
  becomes slow at rebalancing resources across the cohort
- If the reclaim protection duration is set too high, quota owners cannot
  promptly reclaim their own resources

## Alternatives

### Scatter fields across existing Configuration blocks

Two earlier shapes were considered: the original proposal
(`fairSharing.minAdmitDuration` inside the existing `FairSharing` block plus
a top-level `reclaimMinAdmitDuration`), and nesting the rules under
mode-specific blocks (`fairSharing.preemptionProtectionStrategies` plus a
`classicPreemption.protectionStrategies` block).

Both were rejected in favor of one grouped block because:

- reclaim protection applies in *both* classical and fair sharing preemption
  modes, so nesting it under either mode's block would misstate its scope;
- grouping keeps the preemption-protection rules together rather than
  scattered across the Configuration;
- the group provides a clear place for future extensions such as
  within-ClusterQueue protection.

### List-based preemptionProtection API

A list-based API mapping rules to preemption reason enums was considered:

```yaml
preemptionProtection:
  - reason: InCohortFairSharing
    minAdmitDuration: 1h
  - reason: InCohortReclaim
    minAdmitDuration: 30m
```

While maximally extensible, it is over-general for two or three entries:
validation must reject duplicate and unknown reasons, merging configurations
becomes positional, and the schema cannot express per-reason refinements as
naturally as named structs. The struct-based grouping keeps the same
extensibility with simpler validation.

### Per-ClusterQueue configuration

Instead of global rules in the Configuration, each ClusterQueue could define
its own protection (for example under `spec.preemption`). This was deferred:

- there is no strong user story yet for per-CQ values, and global values
  keep behavior predictable — every workload in the cluster gets the same
  guarantee;
- borrower-set reclaim protection takes resources from the lender, so a
  per-CQ API is only sound together with a lender-side guard (analogous to
  `lendingLimit` bounding `borrowingLimit`), which is significant additional
  API surface;
- a CRD field also requires schema regeneration, versioned conversion,
  client-go updates, and webhook validation — a much larger change than
  Configuration fields.

If per-CQ needs materialize, an override can be added under the same
`preemptionProtection` concept with the global value as the default.

### Exempt candidates admitted while the preemptor was pending

To eliminate reclaim starvation entirely, protection could be denied to any
candidate admitted after the preemptor started waiting: late borrowers would
be preemptible immediately. This was rejected for the initial version: it
requires tracking a per-preemptor pending-since timestamp through nomination
and simulation, and it makes the guarantee unpredictable for users — whether
a workload gets its minimum runtime would depend on queue state invisible to
it at admission time. The capacity-reservation interaction and expiry retry
(see [Risks and Mitigations](#risks-and-mitigations)) bound the same risk
with simpler, more predictable semantics. This can be revisited at Beta with
adoption feedback.

### Count cumulative runtime across admissions

Kueue already tracks `status.accumulatedPastExecutionTimeSeconds` across
admission cycles, so protection could be based on total runtime rather than
the current run. This was rejected: the purpose of protection is to let the
*current* run reach a safe stopping point (such as a checkpoint); runtime
accumulated before a previous eviction does not contribute to that.

### Rely on gracefulTerminationPeriod or terminationGracePeriodSeconds

Kubernetes' `terminationGracePeriodSeconds` on Pods controls how long the
kubelet waits between SIGTERM and SIGKILL — it governs shutdown behavior
*after* a preemption decision has been made. Preemption protection governs
*eligibility*: whether a workload can be chosen as a victim at all. A grace
period measured in hours would also hold quota in a half-terminated state,
whereas protection simply defers the decision. Kueue has no
`gracefulTerminationPeriod` field; the closest concept,
`waitForPodsReady.timeout`, controls eviction of workloads that fail to
become ready and is unrelated to preemption timing. These mechanisms are
complementary.

### Per-workload overrides

A per-workload `minAdmitDuration` override (for example via an annotation)
was considered but deferred. Letting users set their own protection invites
gaming the system with arbitrarily long windows; mitigating that requires
administrator-defined bounds or external policy engines, adding significant
complexity. This may be revisited if demand arises.

### Separate duration for each reclaim type

A third rule could split `InCohortReclamation` from
`InCohortReclaimWhileBorrowing`. However, the distinction is an
implementation detail of the borrowing policy that most administrators do
not need to configure separately, and three duration knobs are harder to
reason about than two. Grouping both reclaim types under
`reclaimWithinCohort` keeps the surface manageable while preserving the
fundamental distinction: fairness negotiation versus reclaim of owned
quota.

### Rely on within-ClusterQueue time-based preemption alone

KEP-8522 introduces minimum-runtime semantics for within-ClusterQueue
same-priority preemption. It is explicitly scoped to `withinClusterQueue`
and does not affect cross-ClusterQueue preemption. The two operate at
different levels and are complementary; this KEP's API leaves room for the
within-CQ rule to join the same `preemptionProtection` block later.
