# KEP-7990: Preemption cost signal for workload preemption

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
  - [Ordering semantics](#ordering-semantics)
  - [Defaulting and backward compatibility](#defaulting-and-backward-compatibility)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API changes](#api-changes)
  - [Scheduler changes](#scheduler-changes)
  - [Reference controller](#reference-controller)
  - [Observability](#observability)
  - [Security, Privacy, and RBAC](#security-privacy-and-rbac)
  - [Test Plan](#test-plan)
    - [Unit tests](#unit-tests)
    - [Integration/E2E tests](#integratione2e-tests)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

Introduce an optional, externally-populated **preemption cost signal** on `Workload`
and make Kueue consider this signal when ordering preemption candidates.

When multiple preemption candidates have the **same workload priority**, Kueue
should prefer preempting the candidate with the **lower** preemption cost.

This enables cluster operators to steer preemption toward workloads for which
preemption is expected to be less disruptive (e.g. based on checkpointing, phase
of execution, or prior preemptions), without hardcoding cost policies into Kueue.

## Motivation

The “cost” of preempting a workload is not uniform. Some workloads are cheap to
interrupt (e.g. still initializing), while others are expensive (e.g. mid-epoch
without checkpoint). Kueue today makes preemption decisions primarily based on
cluster queue, fairness configuration, workload priority, and recency; it has no
mechanism to incorporate these runtime cost signals.

This KEP adds a generic mechanism to accept an external signal and use it to
reduce wasted work and improve overall system efficiency.

### Goals

- Add a new optional `Workload` field that can be set by an external controller
  to express preemption cost.
- Use preemption cost as a **tie-breaker among equal-priority preemption
  candidates**.
- Provide a reference implementation of an external controller that populates
  the field (simple, policy-neutral example).

### Non-Goals

- Define a universal “correct” cost model inside Kueue.
- Change preemption eligibility semantics (preemption policies, reclaim rules,
  priority comparisons) beyond candidate ordering tie-breaks.
- Provide guarantees about graceful termination or checkpointing.

## Proposal

Add an optional preemption cost signal on `Workload.status` and incorporate it
into the preemption candidate ordering logic.

### User Stories

- As a cluster administrator, I want Kueue to preferentially preempt workloads
  that are cheaper to interrupt when multiple candidates have the same priority.
- As a platform team, I want to implement organization-specific logic (e.g.
  checkpoint-aware) to compute preemption cost and have Kueue respect it without
  modifying the scheduler.

### Ordering semantics

For preemption candidate ordering, Kueue currently compares candidates by:

- whether already evicted
- whether in a different ClusterQueue than the preemptor
- (optional) local queue fair-sharing usage
- **workload priority**
- quota reservation time (more recent first)
- UID for deterministic ordering

This KEP adds one step:

- If (and only if) two candidates have **equal priority**, compare their
  preemption cost and prefer **lower** cost first.

### Defaulting and backward compatibility

- If a workload has no preemption cost set, it is treated as **zero** cost.
  - This preserves existing behavior when the field is unused (all costs equal).
- The feature is “opt-in by signal”: behavior changes only when an external
  controller populates the field.

### Risks and Mitigations

- **Risk**: External controller writes unexpected values, causing undesirable
  ordering.
  - **Mitigation**: Document semantics clearly; recommend bounded ranges;
    preserve deterministic ordering; keep priority as primary signal.
- **Risk**: Multiple writers to the same status field could conflict.
  - **Mitigation**: Document that a single controller should own the field; use
    merge patch on status updates; recommend a dedicated field manager.

## Design Details

### API changes

Add a new optional field to `WorkloadStatus`:

- `status.preemptionCost`: `resource.Quantity`
  - **Semantics**: unitless score, higher means more expensive to preempt.
  - **Unset**: equivalent to zero.

Go API (illustrative):

```golang
import "k8s.io/apimachinery/pkg/api/resource"

type WorkloadStatus struct {
  // ... existing fields ...

  // preemptionCost is an optional, externally populated signal that indicates
  // how expensive it is to preempt this Workload.
  //
  // Higher values mean more expensive to preempt.
  // If unset, it is treated as 0.
  //
  // +optional
  PreemptionCost *resource.Quantity `json:"preemptionCost,omitempty"`
}
```

YAML example:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: Workload
metadata:
  name: example
  namespace: default
status:
  # Higher values mean more expensive to preempt. Unset means 0.
  preemptionCost: "250"
```

Rationale for `status`:
- Cost is derived from runtime state and external observation; it is not a user
  intent like `spec`.

### Scheduler changes

Update preemption candidate ordering in:

- `pkg/scheduler/preemption/common/CandidatesOrdering`

to compare `status.preemptionCost` after priority comparison (and therefore only
when priority is equal).

### Reference controller

Provide a reference controller that:

- Watches `Workload` objects.
- Computes `status.preemptionCost` from simple signals available on the Workload.

Reference algorithm (example):
- Define cost as a base plus the number of evictions with reason `Preempted`
  recorded in `status.schedulingStats.evictions[]`.

This example demonstrates how to integrate with the signal and encourages users
to implement richer policies as needed.

### Observability

No new scheduler metrics are required initially. The reference controller should
log updates at a low verbosity level and expose reconciliation errors.

### Security, Privacy, and RBAC

The reference controller requires permissions to:

- `get`, `list`, `watch` `workloads`
- `patch` `workloads/status`

### Test Plan

#### Unit tests

- Extend existing unit tests for preemption candidate ordering to validate:
  - equal priority + different cost => lower cost comes first
  - different priority => priority ordering unchanged (cost ignored)

#### Integration/E2E tests

- Optional follow-up: run a small integration scenario where a controller sets
  the cost and confirm preemption selection order.

## Implementation History

- 2026-01-12: Initial KEP drafted.

## Drawbacks

- Adds another signal that can make preemption behavior more complex to reason
  about if set incorrectly.

## Alternatives

- Encode cost purely via priority: rejected, because priority is a global
  scheduling policy knob and conflating it with runtime cost loses expressiveness.
- Include a Kueue-native cost policy: rejected to keep Kueue policy-neutral and
  allow external controllers to express organization-specific logic.


