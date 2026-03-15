# KEP-9734: Historical Borrowing Fair Sharing

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [DRS limitations that historical borrowing addresses](#drs-limitations-that-historical-borrowing-addresses)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Historical borrowing computation](#historical-borrowing-computation)
  - [Entry penalty](#entry-penalty)
  - [Interaction with Admission Fair Sharing](#interaction-with-admission-fair-sharing)
  - [User Stories](#user-stories)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
    - [Story 3](#story-3)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API changes](#api-changes)
    - [Kueue configuration](#kueue-configuration)
    - [ClusterQueue status](#clusterqueue-status)
  - [DominantResourceShare with strategy delegation](#dominantresourceshare-with-strategy-delegation)
  - [CQ-level borrowing tracking](#cq-level-borrowing-tracking)
  - [Preemption behavior](#preemption-behavior)
  - [Preemption frequency bounds](#preemption-frequency-bounds)
    - [Control-theory perspective](#control-theory-perspective)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Performance Impact](#performance-impact)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

This KEP introduces an alternative strategy for Dominant Resource Share
(DRS): half-life decayed historical borrowing. It extends the decay mechanism
from KEP-4136 (Admission Fair Sharing) to cohort-level scheduling and
preemption, while keeping the same DRS aggregation from KEP-1714.

Where DRS with borrowing strategy answers "who is borrowing the most right now?",
DRS with historical borrowing strategy answers "who has borrowed the most over
time?" — giving the system memory of past borrowing so that recent heavy
consumption of shared capacity is appropriately penalised.

## Motivation

Kueue's current cohort-level Fair Sharing (KEP-1714) uses Dominant Resource Share
(DRS), a point-in-time metric measuring how much a ClusterQueue is borrowing
relative to lendable capacity. KEP-1714 explicitly listed historical data as a
non-goal, noting that the design should be "expandable to support history-based
Fair Sharing without major redesign."

KEP-4136 later introduced Admission Fair Sharing with half-life decay — but
scoped it to ordering LocalQueues within a single ClusterQueue, not across CQs
in a cohort. There is no mechanism to apply usage-history-based fairness across
ClusterQueues within a cohort for either admission ordering or preemption.

### DRS limitations that historical borrowing addresses

1. **No memory of past bursts.** A CQ that finished hogging 80% of the cohort
   for a week gets `DRS = 0` the moment its workloads complete — identical to a
   CQ that has been idle for months.

2. **Bursts are not penalised.** A tenant that has been consuming heavily can
   go idle briefly and immediately regain full scheduling and preemption
   priority, even though their recent consumption was disproportionate.

### Goals

* Allow cluster admins to configure fair sharing so that historical borrowing
  — not just instantaneous borrowing — determines scheduling and preemption
  priority across ClusterQueues within a cohort.

* Apply the same historical borrowing score to both admission ordering and
  preemption target selection, so that the two mechanisms are consistent.

* Prevent destructive preemption oscillation under sustained contention,
  ensuring workloads get meaningful runtime before being eligible for
  fair-sharing preemption.

* Be complementary to the existing preemption-based fair sharing (KEP-1714) and
  admission fair sharing (KEP-4136).


### Non-Goals

* Replace DRS. The two strategies coexist; borrowing remains the default.

* Change how Admission Fair Sharing (KEP-4136) works within a single
  ClusterQueue. This KEP operates at the cohort level (inter-CQ); AFS operates
  at the ClusterQueue level (intra-CQ). The two are complementary.

* Store time series data or provide billing-grade accounting.

## Proposal

We introduce a new DRS strategy — `HistoricalBorrowing` — that replaces
instantaneous borrowing with half-life decayed borrowing as input to the
existing DRS aggregation (max ratio of borrowing to lendable capacity). The
aggregation and all downstream behavior — including fair sharing weights — remain unchanged.

### Historical borrowing computation

For each ClusterQueue, Kueue tracks decayed **borrowing** — the amount of
resource consumption beyond the CQ's own quota — using the same exponential
moving average as KEP-4136:

```
borrowing(r) = max(0, usage(r) - quota(r))
borrowing_sum(r) = (1 - α) × previous_borrowing_sum(r) + α × borrowing(r)
```

where `α = 1 - 0.5^(elapsed / halfLifeTime)`.

By tracking `max(0, usage - quota)` rather than total usage, CQs that stay
within their own quota accumulate no historical borrowing — consistent with how
DRS treats them (DRS = 0 when not borrowing).

The score aggregates per-resource historical borrowing into a single comparable
value using the same max-ratio aggregation as DRS:

```
ratio(r) = historicalBorrowing(r) × 1000 / lendable(r)
score = max over resources of ratio(r)
weightedScore = score / fairWeight
```

The aggregation method is unchanged — only the **input** to DRS changes from
instantaneous borrowing to half-life decayed historical borrowing. This is
deliberate: users want "DRS but with memory," and reusing max-ratio
aggregation means zero-config adoption (no `resourceWeights` to tune) with
built-in scarcity normalization via `lendable`.

### Entry penalty

When a workload is admitted, an entry penalty is immediately added to the CQ's
historical borrowing, ensuring that the impact on shared capacity is reflected
before the next sampling interval. Unlike KEP-4136 (which penalizes total
usage), the penalty here reflects only the **increase in borrowing**
caused by the admission:

```
newBorrowing(r) = max(0, currentUsage(r) + totalRequests(r) - quota(r))
oldBorrowing(r) = max(0, currentUsage(r) - quota(r))

penalty(r) = α × (newBorrowing(r) - oldBorrowing(r))
```

The penalty is the increase in borrowing caused by the admission. When the CQ
is already borrowing resource `r`, `oldBorrowing` and `newBorrowing` are both
positive, and the difference simplifies to `totalRequests(r)`. When the CQ is
at or below quota, `oldBorrowing = 0`, and only the portion of the request that
pushes the CQ above quota counts. If the admission does not cause borrowing for
a resource, the penalty for that resource is zero.

Since the penalty is maintained per resource type, it increases per-resource
historical borrowing before the max-ratio aggregation. Monotonicity holds: when
the CQ is borrowing at least one resource (which is the precondition for being
a preemption candidate), the admission penalty is positive for that resource,
guaranteeing that the DRS score increases.

### Interaction with Admission Fair Sharing

Historical borrowing fair sharing and Admission Fair Sharing (KEP-4136) operate
at different levels of the hierarchy.

Both use half-life decay but track different quantities: AFS tracks total
usage at the LocalQueue level, while historical borrowing tracks
`max(0, usage - quota)` at the ClusterQueue level. AFS continues working
exactly as today for intra-CQ ordering. If there is a single CQ with no
cohort, only AFS applies; this KEP has no effect.

### User Stories

#### Story 1

As a cluster admin, I manage a shared GPU cluster for multiple teams. Each team
has a weekly compute budget expressed as a percentage of the cluster, but their
workloads are bursty — a team might be idle for days and then need a large burst.
I want to configure small guaranteed quotas and allow teams to burst into unused
capacity without being preempted, as long as their cumulative usage over the week
stays within their fair share. Currently, fair sharing preempts teams the moment
they borrow above nominal, even if they have been idle all week.

#### Story 2

As a cluster admin, I have two departments sharing a cohort. One department ran a
large batch consuming most of the cohort for a week, then finished. I want
the other department's pending workloads to get scheduling priority for the next
period, since the first department already consumed its share. Currently, the
moment the first department's workloads finish, both departments are treated
equally again because fair sharing has no memory of past consumption.

#### Story 3

As a cluster admin, I see two teams that are both currently borrowing the same
amount of resources. One has been borrowing steadily for a week; the other just
started five minutes ago. I want to configure fair sharing so that the new
borrower is given more leniency than the long-running one, rather than treating
them identically based only on their current borrowing.

### Risks and Mitigations

* **Warm-up period.** When switching from borrowing to historical borrowing
  input, HB is seeded from current borrowing, so CQs that are already
  over-borrowing start with representative scores. The entry penalty further
  increases scores on each subsequent admission. Full convergence to steady
  state still requires 1-2 half-lives, but the system is usable immediately.

* **Two DRS strategies increase complexity.** Mitigation: both use the same DRS
  aggregation and the existing `DRS` struct is reused, so they share all
  downstream code. Only the input computation differs.

* **Preemption cycling under sustained contention.** Historical borrowing
  scores evolve over time, causing score reversals that lead to preemption
  oscillation. Two complementary mechanisms prevent this: a predictive
  preemption guard that adaptively suppresses preemptions which would cause
  a near-term score reversal, and a mandatory `minAdmitDuration` that
  provides a hard minimum runtime floor. See
  [Preemption frequency bounds](#preemption-frequency-bounds) for analysis.

## Design Details

### API changes

#### Kueue configuration

Add a `Strategy` field and an optional `HistoricalBorrowing` config block
to the existing `FairSharing` struct:

```go
type FairSharing struct {
    Enable               bool                  `json:"enable"`
    PreemptionStrategies []PreemptionStrategy  `json:"preemptionStrategies,omitempty"`

    // Strategy selects what data feeds the DRS score computation
    // for cohort-level ordering and preemption.
    // - Borrowing: instantaneous borrowing ratio (default).
    // - HistoricalBorrowing: half-life decayed borrowing over time.
    // +optional
    Strategy DRSStrategy `json:"strategy,omitempty"`

    // minAdmitDuration is the minimum time a workload must run before it
    // becomes eligible for fair-sharing preemption. A workload admitted
    // less than minAdmitDuration ago is skipped as a preemption candidate.
    // Required when Strategy = HistoricalBorrowing.
    // Inspired by Slurm's PreemptExemptTime.
    // +optional
    MinAdmitDuration *metav1.Duration `json:"minAdmitDuration,omitempty"`

    // historicalBorrowing configures the half-life decay parameters for
    // cohort-level historical borrowing scoring.
    // Only used when Strategy = HistoricalBorrowing.
    // +optional
    HistoricalBorrowing *HistoricalBorrowingConfig `json:"historicalBorrowing,omitempty"`
}

type DRSStrategy string

const (
    DRSStrategyBorrowing           DRSStrategy = "Borrowing"
    DRSStrategyHistoricalBorrowing DRSStrategy = "HistoricalBorrowing"
)

type HistoricalBorrowingConfig struct {
    // borrowingHalfLifeTime is the time after which historical borrowing
    // decays by half. Must be at least 30 minutes.
    // +optional
    BorrowingHalfLifeTime metav1.Duration `json:"borrowingHalfLifeTime"`

    // borrowingSamplingInterval is how often borrowing snapshots are taken.
    // +optional
    BorrowingSamplingInterval metav1.Duration `json:"borrowingSamplingInterval"`

    // preemptionProjectionHorizon is how far ahead to project DRS scores
    // when evaluating a preemption. If the preempting CQ's projected score
    // would exceed the target's within this horizon, the preemption is
    // skipped to avoid oscillation. Defaults to 3 × borrowingSamplingInterval.
    // +optional
    PreemptionProjectionHorizon *metav1.Duration `json:"preemptionProjectionHorizon,omitempty"`
}
```

`HistoricalBorrowingConfig` is separate from the existing
`admissionFairSharing` config because they operate at different levels
(cohort vs ClusterQueue) and track different quantities (borrowing beyond
quota vs total usage).

Validation rules:

* `borrowingHalfLifeTime >= 30m`. Below this, the EMA needs several sampling
  intervals per half-life to track properly. `borrowingHalfLifeTime` should be
  at least 5-6x `borrowingSamplingInterval`. Recommended values range from
  4 hours (iterative workloads with frequent checkpoints) to 7 days (weekly
  capacity planning); a value of 1 day is a good starting point.

* `minAdmitDuration` is **required** when `Strategy = HistoricalBorrowing`
  (see [Preemption frequency bounds](#preemption-frequency-bounds)).
  `minAdmitDuration >= 1m` is enforced as a floor.

* `preemptionProjectionHorizon >= borrowingSamplingInterval` if set. The
  projection must cover at least one sampling tick to be meaningful. Defaults
  to `3 × borrowingSamplingInterval` when not specified.

* When `Strategy = Borrowing` (default), `minAdmitDuration` is optional.
  Instantaneous DRS is loop-free and does not require a minimum runtime guard,
  but admins may still set one if desired.

#### ClusterQueue status

We extend `ClusterQueue.status.fairSharing` with an optional
`HistoricalBorrowingStatus` to expose per-resource decayed borrowing:

```go
type FairSharingStatus struct {
    WeightedShare             int64                      `json:"weightedShare"`
    HistoricalBorrowingStatus *HistoricalBorrowingStatus `json:"historicalBorrowingStatus,omitempty"`
}

type HistoricalBorrowingStatus struct {
    BorrowedResources corev1.ResourceList `json:"borrowedResources"`
    LastUpdate        metav1.Time         `json:"lastUpdate"`
}
```

`WeightedShare` is reused for both DRS strategies — existing dashboards
continue to reflect the operative score. `HistoricalBorrowingStatus` is
populated only when `Strategy = HistoricalBorrowing`.

### DominantResourceShare with strategy delegation

The existing `DominantResourceShare()` function in
`pkg/cache/scheduler/fair_sharing.go` is extended to delegate based on
`drsStrategy`. When `Strategy = HistoricalBorrowing`, it calls a private
`historicalBorrowingDRS()` helper that computes the max ratio from decayed
historical borrowing instead of instantaneous borrowing.

The existing `DRS` struct, all its methods, and all call sites remain
unchanged. `HistoricalBorrowing` is a `map[corev1.ResourceName]float64` field
added to `ClusterQueueSnapshot`, populated during snapshot creation from
decayed `max(0, usage - quota)` per resource.

The same delegation pattern applies to `CohortSnapshot`: it carries
`drsStrategy` and aggregates historical borrowing from its child
ClusterQueues.

### CQ-level borrowing tracking

A periodic sampling reconciler on the ClusterQueue controller computes
`max(0, usage(r) - quota(r))` for each CQ and applies the same decay formula
as Admission Fair Sharing. When the strategy is switched to
`HistoricalBorrowing`, the store is seeded from current borrowing to avoid a
cold start.

### Preemption behavior

With historical borrowing, removing a workload does not immediately change
the target CQ's score (inertia). This means the existing preemption strategies
(S2-a and S2-b) collapse into a single condition and continue to work without
code changes. Preemption candidates are filtered to CQs that are currently
borrowing, regardless of DRS strategy.

Two complementary filters prevent destructive preemption oscillation. Both
apply to fair-sharing preemption only; reclaim-within-cohort and within-CQ
preemption are unaffected.

1. **`minAdmitDuration` filter (hard floor).** When `minAdmitDuration` is set,
   a workload whose `QuotaReserved` condition was set less than
   `minAdmitDuration` ago is skipped as a preemption candidate. This uses
   the same `quotaReservationTime` helper already used for workload ordering
   in `pkg/scheduler/preemption/common/ordering.go`. It provides a hard
   minimum runtime guarantee regardless of score dynamics.

2. **Predictive preemption guard (adaptive).** Before preempting a workload
   from CQ-B to admit a workload to CQ-A, the scheduler projects historical
   DRS scores forward by `preemptionProjectionHorizon` (T) under two
   scenarios:

   ```
   projected_A = (1 - α_T) × hist_borrowing_A + α_T × new_borrowing_A
   projected_B = (1 - α_T) × hist_borrowing_B + α_T × borrowing_after_removal_B

   where α_T = 1 - 0.5^(T / halfLifeTime)
   ```

   If `projected_DRS_A > projected_DRS_B`, the preemption would cause a
   score reversal within the horizon — CQ-A would become the top borrower
   and be preempted in turn — so it is skipped. This naturally adapts to
   workload size and score margins: a small workload that barely moves
   scores passes the check, while a large workload that would cause
   immediate oscillation is blocked.

   The guard is always active when `Strategy = HistoricalBorrowing`. It
   relies on a steady-state assumption (borrowing levels remain constant
   during the projection window); see
   [Control-theory perspective](#control-theory-perspective) for analysis
   of when this holds.

### Preemption frequency bounds

Instantaneous DRS is stateless and loop-free. Historical borrowing scores
evolve over time via decay, so score reversal — and therefore
time-multiplexing — is expected when demand exceeds supply. This is
intentional: it is fairer than letting the first-admitted workload run
indefinitely.

However, without a guard the oscillation period is **2 ×
`borrowingSamplingInterval`** (e.g., 10 minutes with 5-min sampling),
regardless of the half-life. After each swap the running and idle CQ scores
are within one entry-penalty step (α) of each other, and two sampling ticks
are always sufficient for crossover.

`minAdmitDuration` solves this by skipping workloads admitted less than
`minAdmitDuration` ago as preemption candidates, decoupling preemption
*frequency* from preemption *decisions*. This is the same approach as Slurm's
`PreemptExemptTime`.

Immediate loop-freedom still holds within a single scheduling cycle:
monotonicity (entry penalty increases the preempting CQ's score) and inertia
(removing a workload does not immediately decrease the target CQ's score)
prevent A→B→A preemption in the same cycle.

A per-CQ override of `minAdmitDuration` (in `ClusterQueuePreemption`) can be
added in a future iteration if different CQs need different minimum runtimes.

#### Control-theory perspective

The design maps to a [PID controller](https://en.wikipedia.org/wiki/Proportional%E2%80%93integral%E2%80%93derivative_controller)
where the error signal is disproportionate borrowing:

* **Proportional (P):** Instantaneous DRS (`Strategy = Borrowing`) reacts to
  the current borrowing level.
* **Integral (I):** Historical borrowing reacts to accumulated borrowing over
  time via EMA decay.
* **Derivative (D):** The predictive preemption guard reacts to the *trend* —
  projecting where scores are heading and suppressing preemptions that would
  cause a near-term reversal.

This KEP implements all three terms. The proportional term is the existing
DRS strategy; the integral and derivative terms are introduced here for the
`HistoricalBorrowing` strategy.

The predictive guard's projection (see
[Preemption behavior](#preemption-behavior)) relies on a **steady-state
assumption**: borrowing levels remain constant during the projection window.
The accuracy of this assumption depends on workload characteristics:

* *Training jobs (low churn):* Long-running, stable resource consumption.
  Borrowing levels are genuinely flat over a 15-minute window, so the
  projection is accurate. This is also where oscillation is most costly
  (wasted GPU-hours), so the guard adds the most value.
* *Inference workloads (high churn):* Short-lived, rapid arrival/departure.
  Usage fluctuates significantly within the projection horizon, making the
  prediction unreliable. However, the cost of prediction errors is low —
  workloads are short-lived and recoverable.
* *Fortunate asymmetry:* Prediction accuracy correlates with the cost of
  being wrong. The guard is most accurate for training-heavy clusters where
  oscillation is expensive, and least accurate for inference-heavy clusters
  where oscillation is cheap.

Because the steady-state assumption can break under high churn,
`minAdmitDuration` is retained as a complementary hard floor — defense in
depth. The predictive guard handles the adaptive case (blocking only
preemptions that would oscillate), while `minAdmitDuration` guarantees a
minimum runtime regardless of prediction accuracy.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.

#### Unit Tests

The code will be thoroughly covered with unit tests.

In particular:
* Historical borrowing score computation and decay mechanics.
* Entry penalty calculation and simulation (addition and removal).
* `minAdmitDuration` filtering: workloads admitted less than `minAdmitDuration`
  ago are skipped as fair-sharing preemption candidates.
* Validation: `minAdmitDuration` required when `Strategy = HistoricalBorrowing`,
  floor of 1 minute enforced.
* Borrowing filter behavior with historical borrowing strategy.
* Predictive guard projection computation: verify projected scores and
  skip/proceed decisions for varying workload sizes and score margins.
* Predictive guard with `preemptionProjectionHorizon` validation.

#### Integration tests

They will mainly focus on cohort-level scheduling with historical borrowing
and interactions with existing fair sharing mechanisms.

In particular:
* Cohort-level admission ordering and preemption targeting based on historical
  borrowing scores.
* Decay behavior over time (burst-then-idle scenarios).
* `minAdmitDuration` prevents preemption of recently admitted workloads
  under sustained contention.
* Coexistence with Admission Fair Sharing (KEP-4136).
* Strategy switching from Borrowing to HistoricalBorrowing.
* Predictive guard skips preemption when projected scores would reverse
  within the projection horizon.

### Performance Impact

The historical borrowing score computation is O(R) per CQ (R = number of
resource types), comparable to the existing DRS computation. Memory overhead
is one float64 per resource per CQ. The sampling goroutine runs at
`borrowingSamplingInterval` and is lightweight (one multiply per resource per
CQ). The entry penalty is O(R) per admission, matching AFS. The predictive
preemption guard adds one O(R) projection per preemption candidate, matching
the existing DRS computation cost. When `Strategy = Borrowing` (default), no
historical borrowing code runs.

### Graduation Criteria

#### Alpha

* Historical borrowing DRS strategy for admission ordering and preemption.
* CQ-level borrowing tracking with decay and entry penalty.
* Mandatory `minAdmitDuration` with preemption candidate filtering.
* Predictive preemption guard with configurable projection horizon.
* CQ status fields and metrics to track historical borrowing.
* Unit and integration tests.
* Feature gate: `HistoricalBorrowingFairSharing`.

#### Beta

* Positive feedback from alpha users.
* Address open questions (see below).
* Scalability testing with >100 CQs and >1k workloads.
* Documentation and migration guide.

## Implementation History

(To be filled as implementation progresses.)

## Drawbacks

* **Two DRS strategies increase cognitive load.** Mitigation: clear
  documentation with scenario-based guidance.

* **Warm-up period.** HB is seeded from current borrowing, but full
  convergence requires 1-2 half-lives.

## Alternatives

* **Keep DRS only.** The status quo. Does not address the lack of memory or
  the inability to penalise recent heavy consumption of shared capacity.

* **Weighted-sum aggregation.** Use `Σ resourceWeight(r) × historicalBorrowing(r)`
  instead of max-ratio. Requires admin-configured `resourceWeights` and would
  make it a different scoring system rather than DRS with a different input. Can
  be added as a future option if demand exists.

* **Build parallel infrastructure.** Separate tournament, strategy, and
  comparison code for historical borrowing. Rejected: ~500-800 lines of
  duplicated code vs ~200 lines with the DRS strategy approach.

* **Extend KEP-4136 directly.** Add cohort-level scope to Admission Fair
  Sharing without changing the preemption score. Would only affect admission
  ordering, not preemption targeting.

* **`minAdmitDuration` only (without predictive guard).** Use only a fixed
  minimum runtime floor to prevent oscillation, without the adaptive
  predictive projection. Simpler to implement and reason about, but
  `minAdmitDuration` is a blunt instrument — a small workload that barely
  changes scores gets the same protection window as a large workload that
  causes a major score swing. The predictive guard was added to provide
  adaptive oscillation prevention that naturally scales with workload size
  and score margins, while `minAdmitDuration` is retained as a complementary
  hard safety floor. See
  [Control-theory perspective](#control-theory-perspective) for analysis of
  the steady-state assumption and workload-type tradeoffs.

