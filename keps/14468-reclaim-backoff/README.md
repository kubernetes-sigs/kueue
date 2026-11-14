# KEP-14468: Reclaim backoff for re-borrowing after preemption-driven reclamation

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
  - [Notes/Constraints/Caveats (Optional)](#notesconstraintscaveats-optional)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Kueue Configuration API](#kueue-configuration-api)
  - [Arming the backoff](#arming-the-backoff)
  - [Deferring assignments](#deferring-assignments)
  - [Waking deferred workloads](#waking-deferred-workloads)
  - [Backoff state tracker](#backoff-state-tracker)
  - [Metrics](#metrics)
  - [Test Plan](#test-plan)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Feature gate instead of configuration opt-in](#feature-gate-instead-of-configuration-opt-in)
  - [Extend waitForPodsReady instead of a new block](#extend-waitforpodsready-instead-of-a-new-block)
  - [Persist the backoff state in ClusterQueue status](#persist-the-backoff-state-in-clusterqueue-status)
  - [Block all admissions of the ClusterQueue after a reclaim](#block-all-admissions-of-the-clusterqueue-after-a-reclaim)
  - [Rely on the existing requeue backoff](#rely-on-the-existing-requeue-backoff)
<!-- /toc -->

## Summary

This proposal introduces an opt-in, per-(ClusterQueue, FlavorResource) reclaim
backoff. After a ClusterQueue's borrowed resource is reclaimed by preemption,
the scheduler applies an exponentially growing cooldown during which it defers
only the flavor assignments that would borrow that same resource again.
Assignments within nominal quota, and assignments of other resources, are
unaffected. This breaks the "admitted, then immediately reclaimed again" spin
loop without changing quota semantics for well-behaved workloads.

## Motivation

When several ClusterQueues in a cohort contend for shared capacity, a
ClusterQueue whose borrowed quota is reclaimed by preemption can be re-admitted
in the very next scheduling cycle by borrowing the same resource again — only
to be reclaimed again shortly after. The result is an admit → reclaim →
re-admit spin loop: workloads are repeatedly evicted and requeued without
making progress, eviction and preemption events flood the API server, and the
scheduling cycle latency degrades for the entire cohort, delaying admission of
unrelated workloads.

The existing mechanisms do not cover this case. The requeue backoff of
`waitForPodsReady.requeuingStrategy` delays requeueing of individual evicted
workloads, but it does not prevent the ClusterQueue from immediately
re-borrowing the reclaimed resource for another workload. Fair sharing
redistributes capacity over longer horizons, but does not debounce repeated
reclamation of the same borrowed resource.

### Goals

- break the admit-then-immediately-reclaimed spin loop with a per-(ClusterQueue,
  FlavorResource) cooldown after reclamation;
- defer only assignments that would re-borrow the reclaimed resource;
  assignments within nominal quota and assignments of other resources must not
  be delayed;
- make the feature strictly opt-in via the Kueue configuration, with tunable
  backoff parameters;
- expose the arming of the backoff through a metric and a dedicated pending
  reason on the Workload, so operators can observe the feature working;
- keep the in-memory state bounded.

### Non-Goals

- changing the behavior of same-ClusterQueue priority preemption
  (`InClusterQueue`) or Fair Sharing preemption (`InCohortFairSharing`);
  the backoff only applies to cohort reclamation;
- persisting the backoff state across controller restarts;
- changing quota, borrowing, or preemption semantics when the feature is
  disabled;
- a per-workload backoff; the existing
  `waitForPodsReady.requeuingStrategy` already covers workload-level requeue
  delays.

## Proposal

We extend the global Kueue Configuration API with a new `reclaimBackoff`
block. When `enable` is true, the scheduler records each reclamation of a
borrowed resource and, for a cooldown window, defers flavor assignments that
would borrow that resource again on the same ClusterQueue. The cooldown grows
exponentially with the number of consecutive reclamations and resets after a
configurable quiet period.

The cooldown for the n-th consecutive reclaim is about `b*2^(n-1)+Rand`, where
`b` is `backoffBaseSeconds` and `Rand` is a small random jitter, capped at
`backoffMaxSeconds` — the same formula and defaults as the existing
`waitForPodsReady.requeuingStrategy` backoff. If the pair is not reclaimed
again within `backoffResetSeconds`, the counter restarts from the base.

The implementation reuses the Kubernetes `wait.Backoff` primitive, the same one
backing the requeue backoff: the jitter is multiplicative at 0.01%, and the cap
is applied to the exponential term before the jitter is added, so the effective
cooldown exceeds `backoffMaxSeconds` by at most the jitter fraction.

A workload deferred this way gets the `ReclaimBackoff` pending reason in its
`QuotaReserved` condition, and the scheduler schedules a delayed requeue of the
ClusterQueue's inadmissible workloads for when the cooldown expires, so the
workload is retried even when no quota-freeing event occurs.

### User Stories (Optional)

#### Story 1

Two batch teams share a cohort. Team A's ClusterQueue has most of its capacity
borrowed by a large job mix. Whenever team B's workloads reclaim their nominal
quota, a few of team A's workloads are preempted — but in the next scheduling
cycle team A's pending workloads immediately borrow the same resources again,
and the cycle repeats. With reclaim backoff enabled, team A's ClusterQueue
stops re-borrowing the reclaimed resource for a growing cooldown, so team B's
reclamation makes progress and the repeated evictions stop.

#### Story 2

A cluster admin observes a high rate of `InCohortReclamation` preemptions and
workloads oscillating between admitted and evicted. They enable
`reclaimBackoff` and watch `kueue_reclaim_backoff_armed_total` to identify the
ClusterQueues whose borrowing is repeatedly reclaimed, then adjust nominal
quotas to reduce the contention.

### Notes/Constraints/Caveats (Optional)

The backoff state is held in memory on the scheduler. A controller restart
clears it, which briefly loses debouncing during the restart window; this is
an accepted trade-off, since preemption spin loops are a seconds-scale
phenomenon and persisting the state would add API write pressure (see
[Alternatives](#alternatives)).

### Risks and Mitigations

- **Delaying a legitimate re-borrow.** After a one-off reclaim, capacity may
  be genuinely free, but the ClusterQueue is held back for one base cooldown.
  Mitigation: the feature is opt-in; the base cooldown defaults to 60s; the
  counter resets after `backoffResetSeconds` of quiet; assignments within
  nominal quota are never delayed.
- **Workloads parked inadmissible with no wake-up event.** Ordinary
  inadmissible-workload retries are triggered by quota-freeing events, which
  may not occur while the cohort is idle. Mitigation: arming the backoff and
  every deferral both schedule a delayed requeue of the affected ClusterQueue
  (and its cohort tree) shortly after the earliest-expiring active cooldown.
- **Unbounded state growth.** The tracker keeps one entry per (ClusterQueue,
  FlavorResource) pair. Mitigation: entries whose cooldown has expired and
  whose reset window has also passed are pruned on every record and when
  encountered on the read path, and a deleted ClusterQueue's entries are purged
  on the delete event, so the map stays bounded by the number of distinct pairs
  in the system and shrinks back once reclamation stops.
- **Arming on preemptions that never happened.** If eviction fails, or the
  target was already evicted in an earlier cycle, nothing was reclaimed in
  this cycle. Mitigation: only targets whose eviction was actually issued in
  the current cycle arm the backoff.

## Design Details

### Kueue Configuration API

We extend the global Kueue Configuration API (`config.kueue.x-k8s.io/v1beta2`)
with a new optional block:

```golang
// Configuration is the Schema for the kueueconfigurations API
type Configuration struct {
	...
	// ReclaimBackoff configures the per-resource reclaim backoff. After a
	// ClusterQueue's borrowed resource is reclaimed by preemption, the scheduler
	// applies an exponential cooldown that defers only the assignments which would
	// borrow that same resource again. The feature is enabled only when this field
	// is set and its Enable subfield is true; unset or Enable=false disables it.
	// +optional
	ReclaimBackoff *ReclaimBackoff `json:"reclaimBackoff,omitempty"`
}

type ReclaimBackoff struct {
	// Enable controls whether the per-resource reclaim backoff is active.
	// +optional
	Enable *bool `json:"enable,omitempty"`

	// BackoffBaseSeconds defines the base for the exponential backoff applied to
	// a (ClusterQueue, resource) pair after its borrowed quota is reclaimed.
	//
	// The cooldown for the n-th consecutive reclaim is about "b*2^(n-1)+Rand"
	// where "b" is BackoffBaseSeconds and "Rand" is a small random jitter, capped
	// at BackoffMaxSeconds. By default, the consecutive cooldowns are around
	// (60s, 120s, 240s, ...).
	//
	// Defaults to 60.
	// +optional
	BackoffBaseSeconds *int32 `json:"backoffBaseSeconds,omitempty"`

	// BackoffMaxSeconds defines the maximum cooldown, in seconds, applied to a
	// single (ClusterQueue, resource) pair.
	//
	// Defaults to 3600.
	// +optional
	BackoffMaxSeconds *int32 `json:"backoffMaxSeconds,omitempty"`

	// BackoffResetSeconds defines the quiet period, in seconds, after which the
	// consecutive-reclaim counter for a (ClusterQueue, resource) pair is reset.
	//
	// Defaults to 600.
	// +optional
	BackoffResetSeconds *int32 `json:"backoffResetSeconds,omitempty"`
}
```

The field name, pointer-to-int32 shape, formula, and defaults deliberately
mirror `waitForPodsReady.requeuingStrategy.backoffBaseSeconds/backoffMaxSeconds`
(KEP-1282), so operators can reuse their mental model.

The block is validated whenever present (regardless of `enable`): all values
must be positive, `backoffMaxSeconds >= backoffBaseSeconds`, and
`backoffResetSeconds > backoffBaseSeconds`, so the consecutive-reclaim counter
can actually grow. Cross-field checks compare the effective values (defaults
for unset fields) and are reported against the field that was set explicitly.

Following the precedent of `visibilityServer` and other newer blocks, the
field is added only to `v1beta2`; `v1beta1` configurations are unaffected, and
a `v1beta1` config file containing `reclaimBackoff` is rejected by strict
decoding rather than silently ignored.

### Arming the backoff

The backoff is armed in the scheduler's preemption path. When preemption
targets are evicted, the scheduler arms the backoff for each target that:

1. has a cohort-reclamation reason (`InCohortReclamation` or
   `InCohortReclaimWhileBorrowing`); same-ClusterQueue priority preemption and
   Fair Sharing preemption do not arm it; and
2. whose eviction was actually issued in the current cycle — targets already
   evicted, still awaiting observation from an earlier cycle, or whose
   eviction failed, do not re-arm it.

For each such victim, the armed dimensions are exactly the FlavorResources the
victim occupies that its ClusterQueue was borrowing at scheduling time (usage
above nominal). Arming records the cooldown and emits the
`kueue_reclaim_backoff_armed_total` metric.

A scheduling cycle records at most one increment per (ClusterQueue,
FlavorResource) pair: if a single preemption batch evicts several victims that
were borrowing the same resource on the same ClusterQueue, the pair is armed
once, and the metric is emitted once. The cooldown therefore grows with the
number of distinct reclamation events, not with the victim count of a single
event.

### Deferring assignments

The read side lives in the flavor assigner. When evaluating whether a
pod-set's resource fits, an assignment that would push the ClusterQueue into
borrowing a FlavorResource currently in cooldown is rejected with `NoFit` and
the pending reason `ReclaimBackoff`. Assignments that fit within nominal quota
skip the check entirely, and cooldowns on other resources or other
ClusterQueues have no effect. In the failure-reason severity ordering,
`ReclaimBackoff` ranks between `WaitingForQuota` and `ExceedsMaxQuota`: it is
a stricter signal than waiting for quota, but less fundamental than exceeding
max quota or flavor mismatches.

The pending reason is reported through the Workload's `QuotaReserved`
condition:

```golang
// WorkloadQuotaReservedReasonReclaimBackoff indicates that the workload is waiting
// because its ClusterQueue recently had a borrowed resource reclaimed by preemption,
// and the resource it would borrow is in the reclaim backoff cooldown.
WorkloadQuotaReservedReasonReclaimBackoff = "ReclaimBackoff"
```

### Waking deferred workloads

Ordinary inadmissible-workload retries fire on quota-freeing events, which may
never occur while the cohort is idle. To guarantee a deferred workload is
retried, the scheduler schedules a delayed requeue of the ClusterQueue (or its
root cohort, matching the existing requeue notification semantics) for the
earliest-expiring active cooldown on that ClusterQueue plus a small margin.
This happens both when the backoff is armed and whenever an assignment is
deferred. When the wake-up fires, workloads still blocked by longer cooldowns
on other resources defer again and re-arm the next wake-up, so using the
earliest deadline never strands a deferred workload, and a workload blocked
only by the shortest cooldown is retried as soon as it can succeed.

### Backoff state tracker

The state lives in an in-memory tracker (`pkg/scheduler/reclaimbackoff`):
a mutex-guarded map keyed by (ClusterQueue, FlavorResource), holding the
consecutive-reclaim count, the cooldown deadline, and the last reclaim time.
Recording a reclaim resets the count if the pair was quiet for longer than the
reset window, and prunes entries whose cooldown and reset window have both
expired. Expired entries are also dropped when encountered on the read path
(cooldown checks and wake-up computation), so entries do not linger when the
system goes idle after a storm. Deleting a ClusterQueue purges its entries
immediately via the ClusterQueue reconciler's delete path; entries keyed on a
deleted ResourceFlavor under a live ClusterQueue need no explicit cleanup and
fall out via the same time-based pruning. The map is therefore bounded by the
number of distinct (ClusterQueue, FlavorResource) pairs in the system. The
clock is injectable for deterministic tests.

### Metrics

We introduce a new counter:

```golang
ReclaimBackoffArmedTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Subsystem: constants.KueueName,
		Name:      "reclaim_backoff_armed_total",
		Help: `The number of times reclaim backoff was armed for a borrowed resource, per 'cluster_queue', 'flavor' and 'resource'.
Reported only when Configuration.ReclaimBackoff is set.`,
	}, []string{"cluster_queue", "flavor", "resource", "replica_role"},
)
```

A counter is used rather than a gauge because backoff expiry is time-based and
lazily evaluated, so there is no reliable moment at which to reset an "active"
gauge. The label set matches the existing ClusterQueue metrics plus the
`flavor` and `resource` dimensions that identify the backoff entry; the
configured custom ClusterQueue labels are appended as with the other
ClusterQueue metrics.

### Test Plan

#### Unit tests

- `pkg/scheduler/reclaimbackoff`: exponential growth, the cap applied before
  jitter (over long consecutive-reclaim sequences delays stay within the
  documented post-jitter bound, `backoffMaxSeconds` plus up to the 0.01%
  jitter), reset after the quiet period, cooldown expiry, key isolation,
  pruning of dead entries both on record and on the read path (including
  keeping an active cooldown past the reset window), and purging a deleted
  ClusterQueue's entries before their cooldown expires.
- `pkg/scheduler/flavorassigner`: a borrowing assignment is deferred with the
  `ReclaimBackoff` reason while backing off; assignments within nominal quota
  are unaffected; a nil tracker (feature disabled) leaves behavior unchanged.
- `pkg/scheduler`: only preemption targets whose eviction was actually issued
  arm the backoff; only FlavorResources the victim's ClusterQueue was
  borrowing are armed; a preemption batch evicting multiple victims on the
  same pair arms it once; the wake-up is scheduled for the earliest-expiring
  cooldown and re-armed when a workload defers again (two-resource case with
  different deadlines).
- `pkg/config`: validation of the `reclaimBackoff` block, including the
  cross-field checks with partially defaulted values and rejection of invalid
  values when the feature is disabled.
- `pkg/metrics`: the armed-total counter is reported only when
  `Configuration.ReclaimBackoff` is set and only for cohort-reclamation
  arming, carries the fixed and custom ClusterQueue labels, and its series
  are cleaned up when the ClusterQueue is removed.

#### Integration tests

A dedicated suite (`test/integration/singlecluster/scheduler/reclaimbackoff`)
covers the end-to-end loop: a workload borrowing above nominal is reclaimed;
with the feature enabled, the ClusterQueue's next borrowing assignment is
deferred with the `ReclaimBackoff` reason until the cooldown expires, while an
in-quota workload is admitted immediately; with the feature disabled,
re-borrowing proceeds without delay.

### Graduation Criteria

Alpha (v0.20):

- the feature is opt-in via `reclaimBackoff.enable: true`;
- unit and integration coverage as listed above;
- the `kueue_reclaim_backoff_armed_total` metric and the `ReclaimBackoff`
  pending reason are exposed.

Beta (future):

- user-facing documentation under `site/content/en/docs`;
- soak time in production-like environments showing the spin loop is broken
  without regressing admission latency for non-borrowing workloads.

## Implementation History

- 2026-08-14: KEP drafted.

## Drawbacks

- The state is in-memory: a controller restart during an active cooldown lets
  the ClusterQueue re-borrow immediately. Given the seconds-scale nature of
  the spin loop, the debounce loss is brief and self-heals on the next
  reclaim.
- A ClusterQueue that was reclaimed once due to a transient spike still pays
  one base cooldown before it can re-borrow, even if capacity is genuinely
  free. The reset window bounds how long this history is remembered.

## Alternatives

### Feature gate instead of configuration opt-in

Kueue's comparable admission-affecting mechanisms are configuration opt-ins,
not feature gates: `waitForPodsReady` (KEP-349) is stable with
`disable-supported: false`, and the requeue backoff (KEP-1282) shipped with
`feature-gates: []`. A configuration block additionally carries the timing
parameters, which a bare feature gate cannot.

### Extend waitForPodsReady instead of a new block

`waitForPodsReady` triggers on pod-readiness timeouts of admitted workloads;
reclaim backoff triggers on cohort reclamation of borrowed quota, which is
unrelated to pod readiness. The requeue strategy under it delays individual
workloads, while reclaim backoff constrains what the ClusterQueue may borrow.
Mixing the two would obscure both.

### Persist the backoff state in ClusterQueue status

Writing every reclamation to the API server adds write pressure precisely when
the system is under a preemption storm. The phenomenon the backoff suppresses
plays out over seconds, so surviving restarts buys little.

### Block all admissions of the ClusterQueue after a reclaim

A whole-ClusterQueue cooldown would also block assignments within nominal
quota and assignments of unrelated resources, punishing well-behaved workloads
and effectively reducing the ClusterQueue's guaranteed capacity after every
reclaim. The per-FlavorResource, borrow-only scoping is the minimal
suppression that breaks the loop.

### Rely on the existing requeue backoff

`waitForPodsReady.requeuingStrategy` only applies to workloads evicted for
pods-ready timeouts, and even when enabled it delays requeueing of the evicted
workload — it does not stop the ClusterQueue from immediately re-borrowing the
reclaimed resource for a different pending workload, which is the loop this
KEP targets.
