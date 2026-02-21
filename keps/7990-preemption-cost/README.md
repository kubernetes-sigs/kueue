# KEP-7990: Support workload priority boost by external controllers

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
  - [Effective priority](#effective-priority)
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
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

Introduce an optional `Workload` annotation `kueue.x-k8s.io/priority-boost`
that allows external controllers to boost (or reduce) a workload's effective
priority. Kueue computes the **effective priority** as:

`effectivePriority = workload.priority + priorityBoost`

and uses it wherever workload priority is currently used for ordering within a
ClusterQueue (both scheduling and preemption candidate ordering).

This enables platform teams to dynamically adjust workload priority based on
runtime signals (e.g. checkpoint status, execution phase, number of prior
preemptions), without hardcoding cost policies into Kueue and without requiring
a proliferation of WorkloadPriority classes.

## Motivation

The "cost" of preempting a workload is not uniform. Some workloads are cheap to
interrupt (e.g. still initializing), while others are expensive (e.g. mid-epoch
without checkpoint). Kueue today makes preemption decisions primarily based on
cluster queue, fairness configuration, workload priority, and recency; it has no
mechanism to incorporate runtime cost signals from external systems.

Existing priority classes are static: a workload receives a priority at creation
time and keeps it for its lifetime. In practice, platform teams need a way to
adjust effective priority dynamically:

- **Visibility**: an explicit annotation makes it clear to both users and
  external controllers that the priority was boosted, avoiding repeated or
  conflicting adjustments.
- **Fine-grained priority without class proliferation**: with a numeric boost,
  controllers can express priorities like "150" or "156" without creating a zoo
  of WorkloadPriority classes for every combination of base priority and runtime
  state.
- **Controller convenience**: the annotation lives on the `Workload` object, so
  the external controller only needs to operate on a single Kind rather than
  every schedulable object type (Job, JobSet, Deployment, etc.).

An important use case is preventing starvation of low-priority workloads: if a
low-priority workload has been preempted too many times and has never completed,
an external controller can boost it above medium-priority workloads so it
eventually makes progress.

### Goals

- Add a new optional `Workload` annotation (`kueue.x-k8s.io/priority-boost`)
  which allows external controllers to boost priority for some workloads.
- Define effective priority as `workload.priority + priorityBoost` and use it
  for workload ordering within a ClusterQueue (scheduling and preemption).
- Provide a reference external controller that demonstrates how to compute and
  set `kueue.x-k8s.io/priority-boost` on `Workload`.

### Non-Goals

- Define a universal "correct" cost or boost model inside Kueue.
- Impact DRS (Dominant Resource Share) calculations; the boost only affects
  priority-based ordering within a ClusterQueue.
- Define anti-flapping policies inside Kueue; it is the responsibility of the
  external controller to avoid pathological update patterns.
- Provide guarantees about graceful termination or checkpointing.

## Proposal

Add an optional `Workload` annotation `kueue.x-k8s.io/priority-boost` that
external controllers can set to dynamically adjust a workload's effective
priority. Kueue incorporates this signal into both scheduling order and
preemption candidate ordering within a ClusterQueue.

### User Stories

- As a cluster administrator, I want Kueue to preferentially preempt workloads
  that are cheaper to interrupt, even when multiple candidates share the same
  WorkloadPriority class.
- As a platform team, I want to implement organization-specific logic (e.g.
  checkpoint-aware, phase-aware, starvation-prevention) that dynamically adjusts
  workload priority and have Kueue respect it without modifying the scheduler.
- As a platform team, I want to boost a low-priority workload that has been
  preempted too many times so that it can cross priority class boundaries and
  eventually make progress.

### Effective priority

Wherever Kueue currently uses workload priority for ordering within a
ClusterQueue, it uses instead:

`effectivePriority = workload.priority + priorityBoost`

where `priorityBoost` is read from the `Workload` annotation
`kueue.x-k8s.io/priority-boost` (default `0`).

The boost can cross priority class boundaries. For example, if priorities are
`low=100`, `mid=200`, `high=300`, a low-priority workload with
`priority-boost: "150"` would have effective priority 250, placing it between
mid and high.

### Defaulting and backward compatibility

- If a workload has no `kueue.x-k8s.io/priority-boost` annotation set, it is
  treated as **zero** boost.
  - This preserves existing behavior when the annotation is unused.
- The feature is "opt-in by signal": behavior changes only when an external
  controller (or administrator) sets the annotation.

### Risks and Mitigations

- **Risk**: Infinite preemption cycles. Introducing a dynamic signal that
  changes ordering creates the possibility of workloads repeatedly preempting
  each other (A preempts B, B's boost increases, B preempts A, ...).
  - **Mitigation**: Kueue uses the boost value as a point-in-time snapshot
    during a scheduling cycle. The responsibility for avoiding flapping lies
    with the external controller. The reference controller (see below) is
    designed to limit oscillation by using bounded, threshold-based increments
    rather than reacting to every preemption.
  - **Safeguard**: As an additional safety measure, Kueue may skip recently
    admitted workloads when selecting preemption candidates.

- **Risk**: Malicious or misconfigured users set extreme boost values on their
  workloads to avoid preemption.
  - **Mitigation**: The annotation is on the `Workload` object, which is not
    directly user-editable in typical deployments (users submit Jobs; Kueue
    creates Workloads). For Alpha, restricting the annotation to `Workload`
    prevents batch users from exploiting the mechanism. RBAC on the `Workload`
    resource should be configured to allow only trusted controllers to patch it.

- **Risk**: Users/platform automation set unexpected values, causing undesirable
  ordering.
  - **Mitigation**: Document semantics clearly; recommend bounded ranges;
    preserve deterministic ordering.

- **Risk**: Annotation value is invalid or out of expected range.
  - **Mitigation**: Treat invalid/unparsable values as zero and emit a warning
    event/log.

## Design Details

### API changes

Add a new optional annotation on `Workload`:

- `kueue.x-k8s.io/priority-boost: "<int>"`
  - **Semantics**: signed integer that adjusts effective priority. Positive
    values increase priority (harder to preempt, scheduled sooner); negative
    values decrease it.
  - **Unset**: equivalent to zero.
  - **Alpha behavior**: if the value is invalid/unparsable, it is treated as
    zero and a warning is logged.

The annotation is set directly on the `Workload` object by an external
controller (not propagated from Jobs).

YAML example (`Workload`):

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: Workload
metadata:
  name: example-workload
  namespace: default
  annotations:
    # Positive values increase effective priority. Unset means 0.
    kueue.x-k8s.io/priority-boost: "100"
spec:
  priorityClassName: medium
  # ... rest of spec
```

### Scheduler changes

Update workload ordering and preemption candidate ordering to use effective
priority:

`effectivePriority = workload.priority + priorityBoost`

where `priorityBoost` is read from `Workload` annotation
`kueue.x-k8s.io/priority-boost` (default `0`).

This affects ordering within a ClusterQueue for both scheduling and preemption
candidate selection. It does not change DRS calculations or preemption
eligibility rules (preemption policies, reclaim rules).

### Reference controller

Provide a reference external controller that:

- Watches `Workload` objects managed by Kueue.
- Computes priority boost using a simple, policy-neutral heuristic.
- Writes/updates `metadata.annotations["kueue.x-k8s.io/priority-boost"]` on
  the `Workload`.

Reference algorithm (example):

- Start with base boost `0`.
- Add a bounded increment for each prior eviction with reason `Preempted`
  observed in the Workload's `schedulingStats`. To avoid preemption flapping,
  use a threshold-based approach: increase the boost only after every N
  preemptions (e.g. every 2nd preemption), so that in the worst case a single
  extra preemption can occur but the cycle does not recur.
- Optionally add a bounded phase-based increment (for example, running
  workloads can receive a higher boost than initializing workloads).
- The controller can combine multiple factors (preemption count, job phase,
  checkpoint status, crash count) into a single boost value using configurable
  weights.

The reference implementation is intentionally simple and demonstrates how
platform teams can plug in richer, organization-specific logic.

### Observability

No new scheduler metrics are required initially.

- Kueue should log invalid `kueue.x-k8s.io/priority-boost` annotation values
  at a low verbosity level.
- The reference controller should log annotation updates and expose
  reconciliation errors.

### Security, Privacy, and RBAC

Scheduler side: no additional RBAC is required beyond existing permissions for
reading `Workload` objects.

Reference controller requires permissions to:

- `get`, `list`, `watch` `workloads.kueue.x-k8s.io`
- `patch` `workloads.kueue.x-k8s.io`

Cluster administrators should ensure that only trusted controllers have `patch`
access to `Workload` objects to prevent abuse of the `priority-boost`
annotation.

### Test Plan

#### Unit tests

- Extend existing unit tests for workload ordering and preemption candidate
  ordering to validate:
  - effective priority uses `priority + priorityBoost`
  - missing or invalid annotation values are treated as zero
  - boost can cross priority class boundaries
- Add unit tests for the reference controller:
  - computes expected boost for representative Workload states
  - writes `kueue.x-k8s.io/priority-boost` annotation idempotently
  - handles update conflicts/retries

#### Integration/E2E tests

- Run a small integration scenario with two same-priority Workloads and
  different `kueue.x-k8s.io/priority-boost` values, and confirm preemption
  selection order.
- Run a scenario where boost crosses priority class boundaries and confirm that
  the boosted low-priority workload is ordered above a medium-priority workload.
- Add integration coverage where the reference controller updates Workload
  annotations and preemption order changes accordingly.

### Graduation Criteria

Alpha -> Beta:

- Re-evaluate user-facing API: consider whether to add a Job-level annotation
  that Kueue propagates to `Workload`, or keep it Workload-only.
- Gather feedback from real-world adoption on the `priority-boost` semantics.
- Evaluate whether an additional "preemption-cost" mechanism (affecting only
  preemption candidate ordering, not scheduling) is needed alongside
  `priority-boost`.
- Validate anti-flapping safeguards are sufficient.

## Implementation History

- 2026-01-12: Initial KEP drafted (as "preemption cost" design).
- 2026-02-20: KEP rewritten to "priority boost" approach based on community
  feedback in #7990 and PR #8551.

## Drawbacks

- Adds another signal that can make scheduling and preemption behavior more
  complex to reason about if set incorrectly.
- Crossing priority class boundaries via boost may surprise users who expect
  strict priority class separation.
- External controller bugs can cause preemption flapping or starvation; Kueue
  itself does not enforce anti-flapping policies.

## Alternatives

- **Preemption-cost-only for candidate ordering**: the original design in this
  KEP proposed a `kueue.x-k8s.io/preemption-cost` annotation that only affected
  preemption candidate ordering (not scheduling). This was rejected because:
  (1) it cannot prevent a preempted medium-priority workload from immediately
  preempting the low-priority workload that had a high cost, since candidate
  eligibility still depends solely on static priorities; (2) use cases like
  starvation prevention require crossing priority class boundaries, which a
  candidate-ordering-only mechanism cannot provide.
- **Encode cost purely via priority class mutation**: rejected, because it
  requires creating many WorkloadPriority classes for every combination of base
  priority and runtime state, lacks visibility into whether a priority was
  boosted, and is inconvenient when the controller needs to operate across
  multiple schedulable object types.
- **Include a Kueue-native cost/boost policy**: rejected to keep Kueue
  policy-neutral and allow external controllers to express organization-specific
  logic.
