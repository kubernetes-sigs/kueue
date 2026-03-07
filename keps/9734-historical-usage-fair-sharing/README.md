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
  - [Preemption behavior](#preemption-behavior)
  - [CQ-level borrowing tracking](#cq-level-borrowing-tracking)
  - [Preemption loop safety](#preemption-loop-safety)
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

Where DRS with borrowing input answers "who is borrowing the most right now?",
DRS with historical borrowing input answers "who has borrowed the most over
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

* Preserve the preemption loop-free property that the current DRS-based fair
  sharing guarantees.

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
aggregation and all downstream behavior remain unchanged.

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
    // decays by half.
    // +optional
    BorrowingHalfLifeTime metav1.Duration `json:"borrowingHalfLifeTime"`

    // borrowingSamplingInterval is how often borrowing snapshots are taken.
    // +optional
    BorrowingSamplingInterval metav1.Duration `json:"borrowingSamplingInterval"`
}
```

`HistoricalBorrowingConfig` is separate from the existing
`admissionFairSharing` config because they operate at different levels
(cohort vs ClusterQueue) and track different quantities (borrowing beyond
quota vs total usage).

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

### Preemption behavior

With historical borrowing, removing a workload does not immediately change
the target CQ's score (inertia). This means the existing preemption strategies
(S2-a and S2-b) collapse into a single condition and continue to work without
code changes. Preemption candidates are filtered to CQs that are currently
borrowing, regardless of DRS strategy.

### CQ-level borrowing tracking

A periodic sampling reconciler on the ClusterQueue controller computes
`max(0, usage(r) - quota(r))` for each CQ and applies the same decay formula
as Admission Fair Sharing. When the strategy is switched to
`HistoricalBorrowing`, the store is seeded from current borrowing to avoid a
cold start.

### Preemption loop safety

Historical borrowing fair sharing does not introduce preemption loops. The
argument relies on two key properties:

* **Monotonicity:** Admitting a borrowing workload increases the CQ's
  historical borrowing score via the entry penalty.
* **Inertia:** Preempting (removing) a workload does not immediately decrease
  the target CQ's historical borrowing score — decay only occurs at future
  sampling intervals.

Together these properties ensure that if Workload A preempts Workload B, then
Workload B cannot preempt Workload A afterwards. A full proof following the
structure of the existing DRS proof will be added to
`site/content/en/docs/concepts/fair_sharing.md`.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.

#### Unit Tests

The code will be thoroughly covered with unit tests.

In particular:
* Historical borrowing score computation and decay mechanics.
* Entry penalty calculation and simulation (addition and removal).
* Preemption loop-free property across DRS strategies.
* Borrowing filter behavior with historical borrowing strategy.

#### Integration tests

They will mainly focus on cohort-level scheduling with historical borrowing
and interactions with existing fair sharing mechanisms.

In particular:
* Cohort-level admission ordering and preemption targeting based on historical
  borrowing scores.
* Decay behavior over time (burst-then-idle scenarios).
* Coexistence with Admission Fair Sharing (KEP-4136).
* Strategy switching from Borrowing to HistoricalBorrowing.

### Performance Impact

The historical borrowing score computation is O(R) per CQ (R = number of
resource types), comparable to the existing DRS computation. Memory overhead
is one float64 per resource per CQ. The sampling goroutine runs at
`borrowingSamplingInterval` and is lightweight (one multiply per resource per
CQ). The entry penalty is O(R) per admission, matching AFS. When
`Strategy = Borrowing` (default), no historical borrowing code runs.

### Graduation Criteria

#### Alpha

* Historical borrowing DRS strategy for admission ordering and preemption.
* CQ-level borrowing tracking with decay and entry penalty.
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

