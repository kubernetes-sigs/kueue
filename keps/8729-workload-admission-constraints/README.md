# KEP-8729: Workload Admission-Time Constraints for Preemption and Borrowing

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
    - [Story 3](#story-3)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API](#api)
  - [Semantics](#semantics)
  - [Admission Flow Changes](#admission-flow-changes)
  - [Interaction with Existing Features](#interaction-with-existing-features)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

This KEP proposes introducing **admission-time constraints** that allow workloads to restrict whether **preemption** and/or **quota borrowing** may be used during admission in Kueue.

These constraints apply **only during admission** and do not affect runtime preemption behavior, cluster-wide policy, or fairness semantics.

## Motivation

Kueue admission may rely on preemption and borrowing to admit workloads that exceed the nominal quota. While this behavior is desirable by default, some workloads have stricter requirements, for example:

* they must not preempt other workloads during admission, or
* they must not borrow quota during admission.

Today users cannot express these constraints without overloading unrelated mechanisms such as priority or queue configuration. Doing so leads to imprecise semantics and unintended side effects, particularly because priority-based mechanisms are inherently bidirectional.

This proposal enables workloads to express *how they may be admitted* without changing *how they are treated after admission*.

## Goals

* Allow workloads to express **admission-time-only** constraints on:

  * preemption
  * borrowing
* Preserve cluster-wide ownership of scheduling, fairness, and quota policy.
* Avoid changes to runtime preemption semantics.
* Maintain backward compatibility and default behavior.

## Non-Goals

* Allowing workloads to opt out of being preempted after admission.
* Allowing workloads to override ClusterQueue fairness or quota policy.
* Introducing general per-workload scheduling policies beyond admission.

## Proposal

### User Stories

#### Story 1
**Conservative admission for long-running or expensive workloads**

As a Kueue user running long-lived or expensive workloads, I want to be conservative about how my workload is admitted.

For these workloads, the cost of eviction or restart is high. I am willing to wait longer for admission if that avoids borrowing or preempting other workloads, reducing the risk of disruption once the workload is running.

#### Story 2
**Avoid admission-time preemption of peer workloads**

As a Kueue user, I want my workload to be admitted only if it can fit without preempting other workloads during admission.

In some environments, preempting peer workloads to admit my own job is undesirable, even if allowed by policy. 
I want to express this constraint explicitly, without changing priority or affecting how my workload may be treated after admission.

#### Story 3
**Improve multi-cluster workload placement**

As a MultiKueue developer, I want to improve multi-cluster workload placement by avoiding race-based admission that can lead to unnecessary preemption across clusters.

Today MultiKueue dispatching is race-driven: workloads are sent to multiple clusters, and the first cluster to admit the workload wins, regardless of placement quality. This can result in workloads being admitted using borrowing or preemption on one cluster, even when another cluster could admit the same workload without either. In some cases, workloads may even be preempted on clusters that ultimately do not run them.

By expressing scheduling preferences as admission constraints, a dispatcher can prefer higher-quality placements first (for example, no borrowing, no preemption), reducing unnecessary preemption and making placement decisions more deterministic.

### Risks and Mitigations

* **Policy fragmentation**: mitigated by limiting scope strictly to admission time.
* **User confusion**: mitigated through explicit naming and documentation emphasizing non-runtime semantics.

## Design Details

### Feature Gate

Admission-time constraints are introduced behind a feature gate, tentatively named `WorkloadAdmissionConstraints`.

* When the feature gate is **disabled**:

  * Admission constraint annotations (`kueue.x-k8s.io/cannot-preempt`, `kueue.x-k8s.io/cannot-borrow`) are ignored.
  * Kueue preserves existing admission behavior, allowing both preemption and borrowing as today.

* When the feature gate is **enabled**:

  * Admission constraint annotations are evaluated and enforced during admission.
  * Disallowed mechanisms (preemption and/or borrowing) are excluded from admission feasibility evaluation.

### API

Introduce workload-level admission constraints expressed via annotations:

* `kueue.x-k8s.io/cannot-preempt`
* `kueue.x-k8s.io/cannot-borrow`

When set to `"true"`, these annotations restrict the mechanisms Kueue may use during admission:

* `cannot-preempt=true`: admission must not rely on preempting other workloads.
* `cannot-borrow=true`: admission must not rely on borrowing quota.

If these annotations are absent or unset, the default behavior is preserved, and Kueue may use both preemption and borrowing during admission as it does today.

### Workload Field-Based API (Future Evolution)

While the initial API is annotation-based at the Job level, the long-term intent is to represent admission constraints as **structured fields on the Workload API**.

During Alpha, admission constraints are propagated from Job annotations to the corresponding Workload (initially as annotations) to allow rapid iteration and validation of semantics. As the feature matures, these constraints are expected to graduate to a field-based API on the Workload spec.

A possible shape for such an API could be:

```yaml
spec:
  admissionConstraints:
    preemption: Never
    borrowing: Never
```

The exact structure and naming are intentionally left open at this stage and will be refined based on usage feedback and consistency with existing Workload APIs (for example, `TopologyRequest`). The key requirement is that these fields capture **admission-time-only constraints** and remain unidirectional, governing how the Workload may be admitted without affecting runtime behavior or cluster-wide policy.

This KEP does not propose introducing per-PodSet scheduling constraints. Admission constraints are assumed to apply at the **Workload level**, since they govern how the workload as a whole is admitted. Supporting PodSet-level constraints would significantly expand the policy surface and is considered out of scope for this proposal, but may be revisited in the future if concrete use cases emerge.

### Semantics

* Constraints are evaluated **only during admission**.
* Constraints are **unidirectional**: they restrict what the workload is allowed to do during admission but do not affect how other workloads may interact with it.
* If constraints cannot be satisfied, the workload remains pending.

### Admission Flow Changes

During admission, Kueue:

* evaluates admission feasibility without using disallowed mechanisms,
* excludes preemption and/or borrowing when computing flavor assignments and admission decisions,
* leaves runtime scheduling and preemption behavior unchanged.

### Interaction with Existing Features

* **Preemption**: unchanged after admission.
* **Borrowing**: unchanged after admission.
* **Fair Sharing**: unaffected.
* **Workload Slices / Elasticity**: constraints apply per admitted workload or slice during admission only
  * Admission constraints apply only to the scale-up decision itself.
  * If the additional capacity required for the scale-up cannot be satisfied under these constraints, the scale-up request remains pending until sufficient resources become available.

### Test Plan

#### Unit Tests

* Validate parsing and defaulting of admission constraint annotations.
* Verify admission feasibility logic for all combinations of constraints:

  * allow preemption / disallow preemption
  * allow borrowing / disallow borrowing
* Ensure admission constraints are enforced only during admission and do not affect post-admission behavior.
* Cover interactions with fair sharing and workload slicing where applicable.

#### Integration Tests

* Verify that workloads with `cannot-preempt=true` are not admitted when admission would require preempting other workloads.
* Verify that workloads with `cannot-borrow=true` are not admitted when admission would require borrowing quota.
* Verify that workloads without admission constraints preserve existing behavior.
* Confirm that constrained workloads remain pending rather than being partially admitted.
* Ensure runtime preemption behavior is unchanged after admission.

### Graduation Criteria

#### Alpha

* Feature gated and disabled by default.
* Admission-time constraints for preemption and borrowing are enforced only when explicitly requested by workload annotations.
* Unit and integration tests cover all combinations of admission constraints.
* Events and/or metrics indicate when a workload is not admitted due to admission constraints.

#### Beta

* Semantics are considered stable and no longer subject to breaking changes.
* Documentation clearly describes:
  * the admission-time-only scope of the constraints,
  * the difference between admission constraints and runtime preemption behavior.
* A field-based representation on the Workload spec is defined or prototyped, informed by feedback from the annotation-based API.
* Backward compatibility is preserved for existing Job-level annotations.
* Observability (events and metrics) is sufficient to diagnose admission failures caused by constraints.
* Usage feedback from real workloads confirms the feature addresses concrete scheduling needs without causing confusion.

#### GA

* Field-based admission constraints on the Workload API are the primary and documented interface.
* Feature gate is enabled by default.
* No significant user confusion or misuse was reported.
* No need for additional admission-time constraint knobs beyond those defined.
* Backward compatibility and scheduling fairness properties are preserved.

## Implementation History

* Initial proposal

## Drawbacks

* Adds additional admission-time policy surface.
* Requires careful documentation to avoid confusion with runtime preemption semantics.

## Alternatives

### Extending WorkloadPriorityClass

An alternative is to extend `WorkloadPriorityClass` with a mode that disables preemption during admission.

While this approach aligns naturally with preemption semantics, it has limitations:

* Priority semantics are bidirectional, affecting both outbound and inbound preemption.
* Borrowing decisions are not priority-driven.
* Using priority to suppress preemption during admission can unintentionally make workloads more vulnerable to being preempted later.

As a result, this approach is not enough to express the full set of admission-time constraints on its own.

### ClusterQueue-only Configuration

Encoding all behavior at the ClusterQueue level was considered but rejected, as it requires over-partitioning queues and does not support workload-specific admission intent.

### Annotations vs. Labels

The current proposal supports expressing admission constraints via annotations:

* `kueue.x-k8s.io/cannot-preempt`
* `kueue.x-k8s.io/cannot-borrow`

While labels and annotations are nearly identical constructs from an implementation standpoint, they serve distinct semantic purposes in Kubernetes. Labels are primarily intended for selection, grouping, and indexing, while annotations are designed to carry non-identifying metadata that influences behavior but is not meant to participate in object selection or lifecycle management.

Admission-time constraints for preemption and borrowing fall more naturally into the latter category. These constraints do not define workload identity, grouping, or affinity. Instead, they express intent about *how* a workload may be admitted into the cluster, specifically which admission mechanisms are allowed or disallowed. Using annotations better aligns with this intent-driven, behavioral nature of the configuration and avoids overloading labels with semantics unrelated to selection or indexing.

We are aware that labels and annotations have different propagation semantics from parent objects to Pods. 
However, this difference is not considered a blocker for this proposal. 
Admission constraints are evaluated at the Workload level and do not generally require automatic propagation to Pods. 

For workloads that do require pod-level integration, job owners can explicitly define the corresponding annotations on the Pod template, as is already common practice for other pod-scoped behaviors.

In summary, annotations provide a clearer semantic signal for admission constraints, avoid unintended coupling with label-based mechanisms, and remain flexible enough to support pod-level use cases when explicitly needed.


