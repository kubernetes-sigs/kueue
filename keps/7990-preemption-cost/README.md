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
  - [Validation](#validation)
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

and uses it wherever workload priority is currently used within a ClusterQueue,
including scheduling order, preemption candidate ordering, and preemption
eligibility evaluation.

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
- Expose Job-level APIs (annotations) for priority boost; the mechanism stays
  Workload-only so that batch users cannot set boost on their Jobs.

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
  - For Alpha, Kueue does not implement admission cooldown. This will be
    evaluated for Beta based on real-world feedback.

- **Risk**: Malicious or misconfigured users set extreme boost values on their
  workloads to avoid preemption.
  - **Mitigation**: The annotation is on the `Workload` object, which is not
    directly user-editable in typical deployments (users submit Jobs; Kueue
    creates Workloads). We do not plan to expose Job-level annotations for
    priority boost at beta or GA, to avoid giving batch users direct control of
    boost. In typical setups, `Workload` is used internally by Kueue and
    cluster admins; giving batch users writable access to `Workload` is not
    recommended. Security in practice relies on: (a) restricting who can patch
    `Workload` (RBAC so only trusted controllers can patch it) and optionally
    admin-configured validation (e.g. ValidationAdmissionPolicies), and (b)
    FairSharing can help mitigate excessive priority bumping across teams.

- **Risk**: Users/platform automation set unexpected values, causing undesirable
  ordering.
  - **Mitigation**: Document semantics clearly; recommend bounded ranges;
    preserve deterministic ordering.

- **Risk**: Annotation value is invalid or out of expected range.
  - **Mitigation**: Reject Workload creation or update when the value is
    invalid or unparsable.

## Design Details

### API changes

Add a new optional annotation on `Workload`:

- `kueue.x-k8s.io/priority-boost: "<int>"`
  - **Semantics**: signed integer that adjusts effective priority. Positive
    values increase priority (harder to preempt, scheduled sooner); negative
    values decrease it.
  - **Unset**: equivalent to zero.
  - **Applicability**: Priority boost applies regardless of how the Workload's
    base priority is derived (Pod PriorityClass, WorkloadPriorityClass, or
    custom logic in an integration helper). Boost also applies when no
    PriorityClass is specified (base priority 0); effective priority is then
    `0 + priorityBoost`.
  - **Alpha behavior**: if the value is invalid/unparsable, the Workload
    creation or update is rejected.

The annotation is set directly on the `Workload` object by an external
controller (not propagated from Jobs).

YAML example (`Workload`):

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
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

### Validation

When the `kueue.x-k8s.io/priority-boost` annotation is set or updated on a
Workload, Kueue validates that the value is a valid signed integer. Invalid or
unparsable values cause the Workload creation or update to be rejected (see API
changes above).

### Scheduler changes

Kueue-scheduler uses **effective priority** for all mechanics where **base
priority** is currently used. In particular, it is used for:

1. Ordering of heads for scheduling.
2. Selection of preemption candidates.

`effectivePriority = workload.priority + priorityBoost`

where `priorityBoost` is read from `Workload` annotation
`kueue.x-k8s.io/priority-boost` (default `0`). For preemption policies such as
`LowerPriority` and `LowerOrNewerEqualPriority`, Kueue uses effective priority
(instead of raw priority) when evaluating eligibility. It does not change DRS
calculations.

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
  extra preemption can occur but the cycle does not recur. Note: this approach
  bounds the frequency of flapping but does not eliminate it entirely.
- Optionally add a bounded phase-based increment (for example, running
  workloads can receive a higher boost than initializing workloads).
- The controller can combine multiple factors (preemption count, job phase,
  checkpoint status, crash count) into a single boost value using configurable
  weights.

The reference implementation is intentionally simple and demonstrates how
platform teams can plug in richer, organization-specific logic.

### Observability

No new scheduler metrics are required initially.

- When a Workload create or update is rejected due to an invalid
  `kueue.x-k8s.io/priority-boost` value, the admission response (or status)
  should indicate the reason; optional low-verbosity log.
- When a preemption happens, Kueue should log the effective priority (base +
  boost) of both the preemptor and the preemptees.
- The reference controller should log annotation updates and expose
  reconciliation errors.

### Security, Privacy, and RBAC

Scheduler side: no additional RBAC is required beyond existing permissions for
reading `Workload` objects.

RBAC on the `Workload` resource should be configured so that only trusted
controllers can patch it.

Reference controller requires permissions to:

- `workloads.kueue.x-k8s.io`:
  - `get`, `list`, `watch`
- `workloads.kueue.x-k8s.io`:
  - `patch`

Cluster administrators should ensure that only trusted controllers have `patch`
access to `Workload` objects to prevent abuse of the `priority-boost`
annotation.

### Test Plan

#### Unit tests

- Extend existing unit tests for workload ordering and preemption candidate
  ordering to validate:
  - effective priority uses `priority + priorityBoost`
  - missing annotation is treated as zero; invalid annotation value causes
    create/update to be rejected
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
- With `LowerPriority` policy, confirm that a low-priority workload with boost
  giving it effective priority above medium cannot be preempted by
  medium-priority workloads.
- Add integration coverage where the reference controller updates Workload
  annotations and preemption order changes accordingly.

### Graduation Criteria

Alpha -> Beta:

- Keep the mechanism Workload-only: no Job-level annotation is planned for beta
  or GA, to avoid exposing boost to batch users.
- Gather feedback from real-world adoption on the `priority-boost` semantics.
- Evaluate whether an additional "preemption-cost" mechanism (affecting only
  preemption candidate ordering, not scheduling) is needed alongside
  `priority-boost`.
- Validate anti-flapping safeguards are sufficient.
- Validate interaction with `LowerOrNewerEqualPriority`: if two workloads have
  different base priorities but the same effective priority after boost, confirm
  the policy treats them as equal-priority.
- Evaluate whether a `maxPriorityBoost` cap on ClusterQueue is needed as a
  safety valve against runaway controllers.

Interaction of DRS (Dominant Resource Share) with priority-boost is out of scope
for this KEP; it may be re-evaluated for GA or handled in a separate KEP/Issue.

Beta -> GA:

- After Beta validation, graduate to GA per standard criteria. The Alpha -> Beta
  items above are the main gates; DRS interaction may be re-evaluated at GA.

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
