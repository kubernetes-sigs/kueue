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

Introduce an optional, user-provided preemption cost signal via
`Job` annotation `kueue.x-k8s.io/preemption-cost`, copied onto `Workload`,
and make Kueue consider this signal when ordering preemption candidates.

Preemption ordering uses an **effective priority for preemption**:
`workloadPriority - preemptionCost`.

This enables cluster operators to steer preemption toward workloads for which
preemption is expected to be less disruptive (e.g. based on checkpointing, phase
of execution, or prior preemptions), without hardcoding cost policies into Kueue.

## Motivation

The “cost” of preempting a workload is not uniform. Some workloads are cheap to
interrupt (e.g. still initializing), while others are expensive (e.g. mid-epoch
without checkpoint). Kueue today makes preemption decisions primarily based on
cluster queue, fairness configuration, workload priority, and recency; it has no
mechanism to incorporate these runtime cost signals.

This KEP adds a generic mechanism to accept a user-facing signal and use it to
reduce wasted work and improve overall system efficiency, with an optional
reference external controller that can populate the signal automatically.

### Goals

- Add a new optional `Job` annotation (`kueue.x-k8s.io/preemption-cost`) that
  is copied to `Workload` and used to express preemption cost.
- Use preemption cost in an **effective priority for preemption**
  (`workloadPriority - preemptionCost`) for candidate ordering.
- Provide a reference external controller that automatically computes and sets
  `kueue.x-k8s.io/preemption-cost` on `Job`.

### Non-Goals

- Define a universal “correct” cost model inside Kueue.
- Change preemption eligibility semantics (preemption policies, reclaim rules,
  priority comparisons) beyond candidate ordering tie-breaks.
- Provide guarantees about graceful termination or checkpointing.

## Proposal

Add an optional preemption cost signal as a `Job` annotation
(`kueue.x-k8s.io/preemption-cost`) that is copied to `Workload`, and
incorporate it into preemption candidate ordering logic.

### User Stories

- As a cluster administrator, I want Kueue to preferentially preempt workloads
  that are cheaper to interrupt when multiple candidates have the same priority.
- As a platform team, I want to implement organization-specific logic (e.g.
  checkpoint-aware) to compute preemption cost and have Kueue respect it without
  modifying the scheduler.

### Ordering semantics

For preemption candidate ordering, Kueue uses priorities; with this KEP it uses
an **effective priority for preemption** expressed as:
`workloadPriority - preemptionCost`.

### Defaulting and backward compatibility

- If a job/workload has no preemption cost annotation set, it is treated as
  **zero** cost.
  - This preserves existing behavior when the annotation is unused.
- The feature is “opt-in by signal”: behavior changes only when users or
  platform automation set the annotation.

### Risks and Mitigations

- **Risk**: Users/platform automation set unexpected values, causing undesirable
  ordering.
  - **Mitigation**: Document semantics clearly; recommend bounded ranges;
    preserve deterministic ordering.
- **Risk**: Annotation value is invalid or out of expected range.
  - **Mitigation**: Treat invalid/unparsable values as zero and emit a warning
    event/log.

## Design Details

### API changes

Add a new optional annotation on `Job`:

- `kueue.x-k8s.io/preemption-cost: "<int>"`
  - **Semantics**: unitless score, higher means more expensive to preempt.
  - **Unset**: equivalent to zero.
  - **Alpha behavior**: if the value is invalid/unparsable, it is treated as
    zero.

Kueue copies this annotation to the corresponding `Workload` and the scheduler
uses the value from the `Workload` object during preemption.

YAML example (`Job`):

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: example
  namespace: default
  annotations:
    # Higher values mean more expensive to preempt. Unset means 0.
    kueue.x-k8s.io/preemption-cost: "250"
spec:
  suspend: true
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: main
        image: busybox
        command: ["sh", "-c", "echo hello && sleep 3600"]
```

### Scheduler changes

Update preemption candidate ordering in:

- `pkg/scheduler/preemption/common/CandidatesOrdering`

to use effective priority for preemption:
`workloadPriority - preemptionCost`, where preemption cost is read from
`Workload` annotation `kueue.x-k8s.io/preemption-cost` (default `0`).

### Reference controller

Provide a reference external controller that:

- Watches `Job` objects managed by Kueue.
- Computes preemption cost using a simple, policy-neutral heuristic.
- Writes/updates `metadata.annotations["kueue.x-k8s.io/preemption-cost"]` on
  the `Job`.

Kueue then propagates this annotation to `Workload`, and scheduler preemption
logic uses the propagated value.

Reference algorithm (example):

- Start with base cost `0`.
- Add `1` for each prior eviction with reason `Preempted` observed for the
  corresponding Workload.
- Optionally add a bounded phase-based increment (for example, running
  workloads can receive a higher cost than initializing workloads).

The reference implementation is intentionally simple and demonstrates how
platform teams can plug in richer, organization-specific logic.

### Observability

No new scheduler metrics are required initially.

- Kueue should log invalid preemption cost annotation values at a low verbosity
  level.
- The reference controller should log annotation updates and expose
  reconciliation errors.

### Security, Privacy, and RBAC

Scheduler side: no additional RBAC is required beyond existing permissions for
reading `Workload` objects used by preemption logic.

Reference controller requires permissions to:

- `get`, `list`, `watch` `jobs.batch`
- `patch` `jobs.batch`
- `get`, `list`, `watch` `workloads.kueue.x-k8s.io`

### Test Plan

#### Unit tests

- Extend existing unit tests for preemption candidate ordering to validate:
  - effective priority for preemption uses `priority - preemptionCost`
  - missing or invalid annotation values are treated as zero
- Add unit tests for the reference controller:
  - computes expected cost for representative Job/Workload states
  - writes `kueue.x-k8s.io/preemption-cost` annotation idempotently
  - handles update conflicts/retries

#### Integration/E2E tests

- Optional follow-up: run a small integration scenario with two same-priority
  Jobs and different `kueue.x-k8s.io/preemption-cost` values, and confirm
  preemption selection order.
- Add integration coverage where the reference controller updates Job
  annotations and preemption order changes accordingly.

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


