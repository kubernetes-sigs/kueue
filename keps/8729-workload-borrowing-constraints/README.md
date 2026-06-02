# KEP-8729: Workload Borrowing Constraints

> Part of [issue #8729](https://github.com/kubernetes-sigs/kueue/issues/8729). This
> KEP covers the per-workload no-borrow constraint behind an alpha gate. Forbidding
> preemption and preference-aware MultiKueue dispatching are tracked as non-goals /
> follow-ups.

<!--
This is the title of your KEP. Keep it short, simple, and descriptive. A good
title can help communicate what the KEP is and should be considered as part of
any review.
-->

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API Definition](#api-definition)
    - [Workload API](#workload-api)
  - [Kueue Scheduler](#kueue-scheduler)
  - [Surfacing the Rejection](#surfacing-the-rejection)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration Tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
  - [Possible Follow-ups](#possible-follow-ups)
    - [Forbidding preemption](#forbidding-preemption)
    - [Preference-aware MultiKueue dispatching](#preference-aware-multikueue-dispatching)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Use an annotation instead of a spec field](#use-an-annotation-instead-of-a-spec-field)
  - [Express the constraint on the ClusterQueue](#express-the-constraint-on-the-clusterqueue)
  - [Reuse FlavorFungibility](#reuse-flavorfungibility)
<!-- /toc -->

## Summary

Kueue lets a `ClusterQueue` borrow unused quota from its `Cohort`, which improves
overall utilization. Whether borrowing happens is decided per flavor assignment by
the scheduler and influenced by the `ClusterQueue`-wide
[`FlavorFungibility`](https://kueue.sigs.k8s.io/docs/concepts/cluster_queue/#flavorfungibility)
preference. These controls are soft and queue-scoped: an individual workload sharing
a `ClusterQueue` cannot opt out of borrowing.

This KEP adds a workload-level `PlacementPolicy` to the `Workload` spec, starting
with a single field that lets a workload forbid borrowing as a hard precondition for
admission. A workload that forbids borrowing stays pending until it can be admitted
using its `ClusterQueue`'s nominal quota, rather than being admitted via borrowed
quota from the `Cohort`.

## Motivation

Borrowing is opportunistic and shared. A workload admitted via borrowed quota can be
reclaimed when the lending `ClusterQueue` needs its capacity back, which leads to
preemption and disruption. For some workloads, waiting for guaranteed nominal quota
is preferable to being admitted on borrowed capacity that may be reclaimed.

This forces users to either split workloads across separate `ClusterQueue`s purely to
get different borrowing behavior, or accept that any workload may be admitted on
borrowed quota.

### Goals

- Allow a workload to declare borrowing as forbidden, so it is only admitted using
  the `ClusterQueue`'s nominal quota.
- Keep the default behavior unchanged: when the field is unset, borrowing is allowed
  exactly as it is today.
- Introduce the API in a shape that can be extended with additional per-workload
  constraints without breaking existing objects.

### Non-Goals

- Forbidding preemption per workload. This is a natural extension of the same API and
  is described under [Possible Follow-ups](#forbidding-preemption), but it is out of
  scope here.
- Changing MultiKueue dispatching to be preference-aware. This depends on per-workload
  constraints and structured rejection reasons and is described under
  [Possible Follow-ups](#preference-aware-multikueue-dispatching).
- Adding new borrowing semantics. This KEP only gates the existing borrowing decision;
  it does not change how borrowing is computed.

## Proposal

Add an optional `PlacementPolicy` struct to `WorkloadSpec` with a single `Borrowing`
field of type `PlacementOption` (`Allowed` or `Forbidden`). When `Borrowing` is
`Forbidden` and the scheduler's flavor assignment for the workload would require
borrowing quota from the `Cohort`, the workload is treated as inadmissible for that
scheduling cycle and requeued, instead of being admitted.

The flavor assignment process itself is unchanged. The constraint is evaluated after
the assignment is computed, using the borrowing amount the scheduler already
calculates. This keeps the change small and localized and preserves the semantics of
`FlavorFungibility`.

### User Stories

#### Story 1

As an ML platform operator, I run interactive and training jobs from ML engineers on
the same shared `ClusterQueue` as other teams. My engineers care most about not being
disrupted: a job that gets preempted mid-run wastes their time and compute.

Borrowing helps utilization, but a job admitted on borrowed quota can be reclaimed and
preempted when the lending team needs its capacity back (for example when a production
job arrives). Today I can either enable borrowing for the whole queue (better
utilization, but every job is exposed to reclaim) or disable it (no exposure, but lower
utilization). I cannot give a single job the guarantee that it will only run on quota
that belongs to us.

I want to mark such a job so it is admitted only on our nominal quota and is therefore
not subject to reclaim-driven preemption. If that quota is not available, I would
rather the job wait than start and risk being preempted.

#### Story 2

As an ML engineer, I want borrowing to be an explicit, opt-in trade-off. By default my
job should stay on our guaranteed quota. When I am willing to take extra capacity in
exchange for accepting that a production job may later preempt me, I opt in by allowing
borrowing. This makes the disruption contract clear: no borrowing means no
reclaim-driven preemption; allowing borrowing means I accepted the risk in return for
running sooner.

### Risks and Mitigations

1. Users may not realize that forbidding borrowing can keep a workload pending
   indefinitely if the `ClusterQueue`'s nominal quota is never sufficient. This is
   mitigated by a clear condition message on the workload and documentation of the
   trade-off between admission latency and placement guarantees.
2. The behavior interacts with flavor ordering and fungibility, which can be subtle.
   This is mitigated by documenting the interplay, mirroring the existing
   documentation for `FlavorFungibility`.

## Design Details

### API Definition

#### Workload API

A new optional `PlacementPolicy` is added to `WorkloadSpec`. Fields that are unset are
interpreted as `Allowed`, preserving existing behavior.

```go
// PlacementOption defines how strictly a particular scheduling
// concession (e.g. borrowing) is allowed for a Workload.
// +enum
// +kubebuilder:validation:Enum=Allowed;Forbidden
type PlacementOption string

const (
	// PlacementOptionAllowed indicates that the corresponding scheduling
	// concession may be used during admission. This is the default and
	// preserves the historical behavior.
	PlacementOptionAllowed PlacementOption = "Allowed"

	// PlacementOptionForbidden indicates that the workload must not be
	// admitted if the corresponding scheduling concession is required.
	// The workload remains pending until it can be admitted without it.
	PlacementOptionForbidden PlacementOption = "Forbidden"
)

// PlacementPolicy describes workload-level hard scheduling constraints.
//
// Fields not set are interpreted as PlacementOptionAllowed.
type PlacementPolicy struct {
	// borrowing controls whether this workload may be admitted by
	// borrowing quota from its Cohort.
	//
	//   - Allowed (default): the workload may be admitted even if the
	//     selected flavor assignment requires borrowing quota from
	//     another ClusterQueue in the Cohort.
	//   - Forbidden: the workload is only admitted when its flavor
	//     assignment can be satisfied using the ClusterQueue's nominal
	//     quota (without borrowing). Otherwise it stays pending.
	//
	// +optional
	Borrowing PlacementOption `json:"borrowing,omitempty"`
}

type WorkloadSpec struct {
	// ...
	// placementPolicy describes hard scheduling constraints that the
	// Workload places on its own admission.
	// +optional
	PlacementPolicy *PlacementPolicy `json:"placementPolicy,omitempty"`
}
```

Example:
```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: Workload
spec:
  queueName: lq-borrower
  placementPolicy:
    borrowing: Forbidden
  podSets:
  - name: main
    count: 1
    template:
      # ...
```

The `PlacementPolicy` wrapper (rather than a flat boolean on `WorkloadSpec`) is chosen
so that future per-workload constraints, such as forbidding preemption, can be added
as sibling fields without churning existing objects (see
[Possible Follow-ups](#possible-follow-ups)).

`PlacementPolicy` is a `v1beta2`-only field. Conversion to `v1beta1` drops it, using
the same approach already established for `PreemptionGates`.

### Kueue Scheduler

The flavor assignment process is not changed. The scheduler already computes, for
each candidate assignment, how much quota would need to be borrowed from the `Cohort`
(`Assignment.Borrowing`, exposed via `Assignment.RequiresBorrowing()`).

The constraint is evaluated in the scheduler's per-workload admission path, after the
assignment is computed and after the existing `NoFit` short-circuit. If the workload
forbids borrowing and the assignment requires borrowing, the workload is marked
inadmissible for the cycle and requeued with a dedicated requeue reason
(`BorrowingForbidden`); the cached assignment is cleared so the next cycle reconsiders
all flavors.

Because the order of flavors and the configured `FlavorFungibility` determine whether
an assignment requires borrowing, they continue to determine whether this constraint
is triggered. The constraint never changes the assignment; it only rejects an
assignment that would borrow.

The behavior under the configured queueing strategy follows the existing pattern for
inadmissible workloads:

* `BestEffortFIFO` - the workload is marked inadmissible and does not block its queue.
  It is reconsidered when a relevant cache change occurs (e.g. its `ClusterQueue`
  gains nominal quota, or a neighbor frees capacity). When reconsidered, it is
  admitted only if the new assignment no longer requires borrowing.
* `StrictFIFO` - the workload is put back into the heap and can block the admission of
  lower-priority workloads in its `ClusterQueue`, consistent with other inadmissible
  heads.

### Surfacing the Rejection

When the workload is rejected for this reason, the `QuotaReserved` condition is set to
`False` with reason `Pending` and a message indicating the cause, for example:

```
Workload requires borrowing quota, but PlacementPolicy.Borrowing is Forbidden
```

This makes the reason visible via `kubectl get workload` and through the workload's
events, and gives higher-level controllers (including future MultiKueue dispatching) a
clear signal that the rejection was due to a borrowing constraint rather than a lack
of capacity.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.

#### Unit Tests

- A helper that reports whether a workload forbids borrowing.
- Scheduler unit tests covering:
  - a forbidding workload that would borrow stays pending with the expected message,
  - a workload with the default policy is admitted via borrowing,
  - a forbidding workload is admitted once it no longer requires borrowing (e.g. after
    nominal quota is increased).

#### Integration Tests

- A forbidding workload remains pending while the only feasible assignment requires
  borrowing, and is admitted once nominal quota becomes sufficient.
- A non-forbidding workload in the same `ClusterQueue` is admitted via borrowing in
  the same scenario.

### Graduation Criteria

The feature is introduced behind a `WorkloadPlacementPolicy` feature gate.

- **Alpha**:
  - Feature implemented behind the feature gate, disabled by default.
  - Unit and integration tests are implemented.
- **Beta**:
  - Feature gate is enabled by default.
  - The feature has been exercised in a production-like environment.
  - User feedback is gathered and emerging constraints are considered for the
    `PlacementPolicy` API.
- **Stable**:
  - The feature is considered stable and the feature gate is removed.

### Possible Follow-ups

#### Forbidding preemption

A `preemption: Forbidden` field can be added to `PlacementPolicy` as a sibling of
`borrowing`, letting a workload refuse placements that would require preempting other
workloads. This builds on the same evaluation point in the scheduler (the assignment
mode is already known) and the same surfacing mechanism. It is kept out of this KEP to
keep the initial change small.

#### Preference-aware MultiKueue dispatching

With per-workload constraints and structured rejection reasons in place, MultiKueue
dispatching can be made preference-aware: prefer a worker cluster that admits a
workload without borrowing (or preemption) over one that does, rather than admitting
on the first cluster to respond. This is a larger, separate effort that depends on the
primitives introduced here.

## Implementation History

- 2026-06-02: Initial draft of the KEP.

## Drawbacks

The feature adds a per-workload knob that interacts with flavor ordering and
fungibility, which increases the surface users must understand to predict admission
behavior. A forbidding workload can also remain pending indefinitely if its
`ClusterQueue` never has sufficient nominal quota.

## Alternatives

### Use an annotation instead of a spec field

The constraint could be expressed via an annotation (for example
`kueue.x-k8s.io/cannot-borrow`) rather than a typed spec field. An annotation avoids an
API change and is easy to introduce in alpha.

**Reasons for discarding/deferring**

A typed field is discoverable, validated by the API server, and self-documenting in
the API reference. It also composes cleanly with future per-workload constraints under
a single `PlacementPolicy`, whereas a set of independent annotations does not. Given
that `PreemptionGates` already established the pattern of a typed workload-level
scheduling field, a spec field is the more consistent choice.

### Express the constraint on the ClusterQueue

Borrowing could be disabled at the `ClusterQueue` level. This already partially exists
via borrowing limits, but it is queue-scoped and cannot distinguish between workloads
in the same queue. The motivation of this KEP is precisely to allow different
workloads sharing a `ClusterQueue` to have different borrowing behavior.

### Reuse FlavorFungibility

`FlavorFungibility` controls whether the scheduler prefers borrowing over trying the
next flavor, but it is a soft, queue-wide preference and cannot express a hard
per-workload guarantee. It also cannot keep a workload pending; it only changes which
flavor is chosen.
