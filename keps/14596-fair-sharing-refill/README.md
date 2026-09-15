# KEP-14596: Fair Sharing Refill

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: a deep backlog drains one Workload per cycle](#story-1-a-deep-backlog-drains-one-workload-per-cycle)
    - [Story 2: available capacity goes to a ClusterQueue with a higher share](#story-2-available-capacity-goes-to-a-clusterqueue-with-a-higher-share)
  - [Terminology](#terminology)
  - [Refill behavior](#refill-behavior)
  - [Refill budget](#refill-budget)
  - [Interaction with other scheduling features](#interaction-with-other-scheduling-features)
  - [Observability](#observability)
  - [Configuration](#configuration)
  - [Notes, Constraints, and Caveats](#notes-constraints-and-caveats)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [Longer cycles act on an older snapshot](#longer-cycles-act-on-an-older-snapshot)
    - [Latency for Workloads arriving mid-cycle](#latency-for-workloads-arriving-mid-cycle)
    - [Fairness residue when the refill budget binds](#fairness-residue-when-the-refill-budget-binds)
- [Test Plan](#test-plan)
  - [Unit tests](#unit-tests)
  - [Integration tests](#integration-tests)
  - [Benchmark](#benchmark)
- [Proposed Alpha semantics](#proposed-alpha-semantics)
- [Open questions before Beta](#open-questions-before-beta)
  - [Budget allocation](#budget-allocation)
  - [Budget exhaustion](#budget-exhaustion)
  - [Scope beyond Fair Sharing](#scope-beyond-fair-sharing)
- [Graduation Criteria](#graduation-criteria)
  - [Alpha (v0.20)](#alpha-v020)
  - [Beta](#beta)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Look-ahead](#look-ahead)
  - [Scanning past a blocked ClusterQueue head](#scanning-past-a-blocked-clusterqueue-head)
  - [An unbounded refill](#an-unbounded-refill)
  - [Charging the budget only for successful admissions](#charging-the-budget-only-for-successful-admissions)
  - [Related mechanisms](#related-mechanisms)
<!-- /toc -->

## Summary

Today the scheduler considers at most one Workload per ClusterQueue per scheduling cycle.
A ClusterQueue that admits a Workload cannot admit again until the next cycle, even if it is still the furthest below its fair share.
Capacity left in the cycle can therefore go to siblings with a higher share first.

Fair Sharing Refill removes that wait: after each successful admission, the ClusterQueue's next Workload joins the current cycle and competes under the freshly recomputed fair-sharing ordering.
A fixed per-cycle budget bounds the extra scheduling work.

## Motivation

The concrete example from [#9345](https://github.com/kubernetes-sigs/kueue/issues/9345):

```text
- Root Cohort (4 GPU)
  - CQ-A (0 GPU)
  - CQ-B (0 GPU)
```

Each workload requests 1 GPU.

|  | Without refill (as reported in #9345) | With refill |
|---|---|---|
| t0 | `create a0, a1` | `create a0, a1` |
| t1 | `a0, a1 schedule; DRS(CQ-A): 500` | `a0, a1 schedule; DRS(CQ-A): 500` |
| t2 | `create a2, b0, b1` | `create a2, b0, b1` |
| t3 | `a2, b0 schedule; DRS(CQ-A): 750, DRS(CQ-B): 250` | `b0 schedules; ordering recomputed: DRS(CQ-B): 250 < DRS(CQ-A): 500, refill pops b1` |
| t3 (cont.) | | `b1 schedules; DRS(CQ-A): 500, DRS(CQ-B): 500` |

The fair-sharing ordering was never wrong here. CQ-B loses the second GPU because the cycle sees only one workload per ClusterQueue, so b1 never competes even though CQ-B is still furthest below its fair share. Refill lets the ordering keep deciding within the cycle until capacity or the budget runs out, converging to 500/500 instead of locking in 750/250.

### Goals

- Define a bounded mechanism for adding candidates to a running Fair Sharing cycle after successful admissions.
- Preserve scheduler correctness when the candidate set grows during a cycle.
- Bound the additional work, and make exhaustion observable.

### Non-Goals

- Letting a refilled Workload preempt, or reserve capacity that another Workload in the same cycle is waiting for.

## Proposal

### User Stories

#### Story 1: a deep backlog drains one Workload per cycle

A ClusterQueue has 100 pending Workloads and the lowest fair share in its cohort.
It admits one Workload per cycle, and each cycle re-snapshots the cache and re-evaluates every ClusterQueue's head for that single admission.

With refill, the queue's next Workload competes in the same cycle after each admission.
Each extra admission costs one evaluation instead of a whole cycle, so the backlog drains in less wall time.

#### Story 2: available capacity goes to a ClusterQueue with a higher share

A large Workload finishes and frees capacity.
The poorest ClusterQueue admits its head.
That was its only candidate in the cycle, so richer siblings absorb the rest of the capacity while the poorest queue still has pending Workloads.
The cycle saw all of the capacity but only one of the poorest queue's Workloads.

With refill, the queue's next Workload enters the cycle after each admission.
The fair-sharing ordering decides who receives the freed capacity, not who happened to have a candidate present.

### Terminology

- **Successor**: the next Workload in a ClusterQueue's active queue.
- **Refill pop**: taking a successor out of the queue and into the running cycle.
- **Refill budget**: the number of refill pops a cycle may make.
  It bounds scheduling work only, and grants a ClusterQueue no additional quota.
  [KEP-1714](../1714-fair-sharing/README.md) uses "budget" for a ClusterQueue's quota allowance, so this document always qualifies the term as *refill budget* to keep the two apart.

### Refill behavior

The mechanism is simple: after each admission, the winner's successor joins the cycle's ordering and is picked next if its ClusterQueue is still the poorest, at most N times per cycle.

Refill is available only with Fair Sharing, where the ordering is recomputed on every pop.
This makes a mid-cycle candidate meaningful: the successor competes under the ordering that holds after its predecessor's admission.

A refill chain starts only after a fresh admission.

It stops when:

- the preceding entry did not create a new admission opportunity;
- continuing would violate the contract of another scheduling feature;
- the ClusterQueue has no successor;
- the refill budget is exhausted; or
- the successor cannot safely act on the current cycle's state.

A refilled Workload may act in the current cycle only when its assignment is `Fit`.
For example, it may see capacity consumed by an earlier admission in the same cycle, so a non-`Fit` result can reflect transient state.
Refill must not turn that view into a reservation or preemption decision.
The Workload instead returns to the queue and is reconsidered from fresh cycle state, carrying nothing forward from what it saw mid-cycle.

Requeue signals that arrive during the cycle must remain visible to a refilled Workload.
Otherwise it could be parked as inadmissible and wait for another event instead of being reconsidered in the next cycle.

### Refill budget

Alpha uses one global refill budget per scheduling cycle, so the additional scheduling work has a single upper bound regardless of how many cohorts exist.
Each Workload actually pulled into the cycle spends one unit, whether or not it is then admitted, and an attempt that finds the queue empty spends nothing.
Charging failed evaluations is deliberate: the budget bounds the additional work a cycle performs, not the number of extra admissions it may make.
Once the budget is exhausted, no new successors enter the cycle, and candidates already present continue to be processed.

The Alpha default is 8 refill pops per cycle, enough to exercise refill beyond a single successor.
It is a provisional operating point, not a benchmark-derived optimum: the benchmark shows that no fixed value is universally best, because the useful allowance depends mainly on how many ClusterQueues are actively admitting.

### Interaction with other scheduling features

- **ConcurrentAdmission**: refill stops the chain after admitting a Variant.
  KEP-8691 relies on at most one Variant per scheduling cycle so that siblings of the same parent are not admitted against the same frozen snapshot.
- **Preemption**: refilled Workloads do not act on preemption or other non-`Fit` outcomes in the current cycle, and are re-evaluated from a fresh snapshot in the next one.
- **WaitForPodsReady**: refill is disabled when `blockAdmission` serializes admission, because another candidate cannot make progress in the same cycle.
- **Topology Aware Scheduling**: placement semantics are unchanged, and a refilled Workload is evaluated against the same current-cycle snapshot as other candidates.
  Correctness is covered at Alpha, and scheduler cost on topology-heavy workloads remains a Beta validation item.
- **Admission Fair Sharing**: the existing accounting semantics are unchanged.
  A refilling cycle makes several admissions, each recording an entry penalty, while the usage it reasons about stays as captured at cycle start.
  What a refilled candidate observes has to be characterized and tested before the gate is enabled by default.

### Observability

Why a refill chain stopped is observable, with budget exhaustion distinguished from an empty queue, so an operator can tell when the bound actually limits progress rather than when it is merely spent.
The metric surface follows the in-cycle recompute metrics discussed in [#14205](https://github.com/kubernetes-sigs/kueue/issues/14205).

Queue diagnostics also report which Workloads the scheduler currently holds, so leaked ownership stays visible now that a ClusterQueue can have more than one Workload in flight within a cycle.

### Configuration

At Alpha the refill budget is a constant, with a test and benchmark hook that sets arbitrary values.
No user-facing field is introduced.

A user-facing field waits until [Budget allocation](#budget-allocation) is settled, so that Alpha does not encode the global-budget model into the API before it is chosen.
The field then follows the scheduler configuration work in [#14190](https://github.com/kubernetes-sigs/kueue/issues/14190).

### Notes, Constraints, and Caveats

Cycles are not a proxy for time.
A cycle that admits anything is followed immediately by another, so a reduction in cycle count overstates what refill saves.
Refill is better described as amortizing the fixed cost of a cycle than as making nomination cheaper.

The benefit depends on the refill budget relative to the number of ClusterQueues actively admitting, so a fixed value is not intrinsically meaningful.
The same budget that shortens a drain for a small number of borrowing ClusterQueues can buy no cycles at all across a wide cohort.

Refill costs work where capacity is scarce.
In capacity-constrained shapes, a refill pop can be evaluated and then returned to the queue, which adds scheduling work without adding an admission.

The drain benchmark in [#13730](https://github.com/kubernetes-sigs/kueue/pull/13730) measures these effects, and the results are discussed on [#13729](https://github.com/kubernetes-sigs/kueue/pull/13729).

### Risks and Mitigations

#### Longer cycles act on an older snapshot

A cycle works from one snapshot, so a longer cycle acts on a staler one.
Refill increases cycle duration as the budget grows, because more candidates are evaluated against a single snapshot.
An unbounded refill can collapse a drain into one substantially longer cycle.

The refill budget exists to bound this, and the benchmark reports per-cycle quantiles for every configuration so the cost is visible when a default is chosen.

#### Latency for Workloads arriving mid-cycle

Refill can reduce latency for a new Workload that enters a ClusterQueue which continues admitting in the same cycle.
It can increase latency for an unrelated new Workload by lengthening the cycle that Workload must wait for.

Refill does not necessarily add a cycle of waiting, but it can make the cycle a new Workload is already waiting for longer.

#### Fairness residue when the refill budget binds

When the budget is exhausted, a poorer ClusterQueue may have a successor that never enters the remaining fair-sharing ordering.
The benchmark demonstrates that this situation occurs under contested shares, although it does not establish that the hidden successor would necessarily have been admitted.
The exhaustion policy is therefore treated as an open question rather than as settled behavior, as described under [Budget exhaustion](#budget-exhaustion).

## Test Plan

### Unit tests

Scheduler coverage includes the #9345 scenario with the gate on and off, budget boundaries, failed and dropped evaluations, re-ranking against the ordering recomputed after an admission, mid-cycle requeue signals, deferral of non-`Fit` outcomes, `ConcurrentAdmission`, topology-aware contention within one cycle, and the queue-layer bookkeeping for more than one in-flight Workload.
The shared scheduler test body asserts after every case that any in-flight claim left standing belongs to a Workload that was admitted.

### Integration tests

The #9345 shape is exercised end to end with the gate enabled and disabled.

### Benchmark

A drain-to-empty benchmark measures repeated cycles until a fixture's Workloads are all admitted, across the gate being disabled and a range of bounded and unbounded refill budgets.
The fixtures vary backlog concentration, cohort width, how much capacity is available per cycle, contested shares, two independent cohorts sharing one allowance, and assignment cost.
It reports drain wall-clock time, cycles, per-cycle quantiles, allocations, CPU time, and the wait a Workload arriving mid-cycle experiences.

Preemption-heavy and topology-aware benchmark shapes are not yet covered.
Preemption benchmarking in particular requires simulating the workload controller so that evictions take effect.

## Proposed Alpha semantics

Fair Sharing Refill is proposed as an Alpha, Fair-Sharing-only behavior:

- after a fresh admission, the scheduler may consider the next Workload from the same ClusterQueue in the current cycle;
- the successor is ranked together with the remaining candidates using the fair-sharing ordering recomputed after the preceding admission;
- only a `Fit` successor may act in the current cycle, and other outcomes defer to the next cycle;
- a per-cycle refill budget bounds additional candidate evaluation;
- the Alpha implementation uses one global per-cycle budget with a default of 8 refill pops;
- interactions that rely on the existing one-candidate-per-ClusterQueue assumption stop the refill chain rather than extending it.

These choices are intended to make the Alpha behavior easy to bound and reason about.
They are not all proposed as the final policy for Beta.

## Open questions before Beta

This KEP intentionally leaves three policy choices open for Beta.

### Budget allocation

A global allowance does not guarantee how refill work is divided between independent cohorts.
Alternatives include a per-cohort allowance, or a global cap combined with per-cohort limits.
A per-cohort allowance would let the additional work grow with the number of cohorts, which is why Alpha starts from the stronger bound.
The Beta decision should balance per-cycle work bounds against cross-cohort predictability, and the default of 8 is revisited together with it and with the configuration surface.

### Budget exhaustion

Ending the cycle immediately would avoid further admissions while successors are hidden behind the exhausted budget, but it would defer the remaining candidates and may repeat some scheduling work in the next cycle.
A third option is to end the cycle only when the ClusterQueue that just won still has a backlog.
The exhaustion policy will be revisited using the benchmark and operational data.

### Scope beyond Fair Sharing

The underlying mechanism is not inherently fair-sharing-specific.
Generalizing it requires defining ordering semantics for non-fair-sharing schedulers, and is not part of Alpha.

## Graduation Criteria

### Alpha (v0.20)

- Feature gate disabled by default.
- Fair Sharing only, and a refilled Workload acts only on `Fit`.
- Global per-cycle refill budget with a constant default, and no user-facing configuration.
- Drain benchmark covering cycle duration, arrival latency, and a budget sweep.

### Beta

- Select and document the refill-budget allocation model.
- Select and document the budget-exhaustion behavior.
- Decide whether refill remains Fair-Sharing-only.
- Introduce the user-facing configuration surface and default, if required by the selected budget model.
- Define and test the interaction with Admission Fair Sharing.
- Validate scheduler cost on preemption-heavy and topology-aware workloads.
- Add metrics for refill termination and exhaustion, sufficient to evaluate the chosen policy in production.
- Demonstrate no known correctness regressions with the gate enabled by default.

## Implementation History

- 2026-02-18: The [motivating issue](https://github.com/kubernetes-sigs/kueue/issues/9345) is raised in Kueue.
- 2026-08-02: Prototype behind the `FairSharingRefill` gate, with a drain benchmark in a companion PR.
  - [#13729: Prototype](https://github.com/kubernetes-sigs/kueue/pull/13729)
  - [#13730: Drain benchmark](https://github.com/kubernetes-sigs/kueue/pull/13730)
- 2026-08-17: First draft of the KEP.

## Drawbacks

Refill makes the amount of work in a scheduling cycle depend on what the cycle admits, which is harder to reason about than a fixed candidate set.
It lengthens cycles, and a longer cycle acts on an older snapshot.
Where capacity is scarce it adds work that is thrown away.
Each of these is bounded by the refill budget, but the bound is a value that has to be chosen, and this KEP does not claim the Alpha default is the right one.

## Alternatives

### Look-ahead

Look-ahead nominates several Workloads per ClusterQueue at cycle start, before any of them is admitted.
Both approaches address the same limitation, which is how many candidates a cycle can see, from opposite ends.
Look-ahead brings candidates in early, and refill brings the next candidate in after progress has been made.
Look-ahead needs a stopping rule decided before any outcome is known, and it evaluates candidates that may never be reached, while refill spends additional work only after the ClusterQueue has made progress through an admission.

The two are not mutually exclusive.
Look-ahead may suit shapes where the first candidate of a ClusterQueue is frequently inadmissible, which refill cannot help with at all, since it never provides a second candidate for a ClusterQueue that has not admitted.

### Scanning past a blocked ClusterQueue head

Scanning past a blocked head searches for another Workload when the current head cannot progress.
Refill does not do this: a successor becomes eligible only after the current candidate is admitted.

This distinction keeps the refill trigger tied to demonstrated progress, and leaves blocked-head semantics unchanged.

### An unbounded refill

On a concentrated backlog, removing the bound can collapse the drain into a single cycle, at the cost of a cycle that is substantially longer than any the scheduler would otherwise run.
Everything the scheduler learns during a cycle is learned from one snapshot, which is the argument for having a bound at all.

### Charging the budget only for successful admissions

This would make the budget a promise about admissions rather than about work.
On a contended cohort, where most refill pops are returned, failed evaluations would no longer be bounded by the configured refill budget, which is the case the bound exists for.

### Related mechanisms

The Sticky ClusterQueue Head Policy applies to `BestEffortFIFO` ClusterQueues, and governs which Workload a ClusterQueue offers as its head across cycles.
Refill applies only under Fair Sharing, and governs whether a ClusterQueue offers a further Workload within one cycle.
The two do not overlap today, but they answer neighbouring questions about which Workload the scheduler serves next, so a future generalization of either should account for the other.
