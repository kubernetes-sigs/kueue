# KEP-13414: Scope Workload Priority Boost on Preemption to a ClusterQueue

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [ClusterQueue-local priority boost](#clusterqueue-local-priority-boost)
- [Design Details](#design-details)
  - [Priority model](#priority-model)
    - [Scheduling behavior](#scheduling-behavior)
    - [Preemption behavior](#preemption-behavior)
  - [API Changes](#api-changes)
  - [Reference controller changes](#reference-controller-changes)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [Context-dependent priority](#context-dependent-priority)
    - [Scope mistaken for isolation](#scope-mistaken-for-isolation)
    - [Removing the Alpha annotation](#removing-the-alpha-annotation)
  - [Test Plan](#test-plan)
    - [Unit tests](#unit-tests)
    - [Integration/E2E tests](#integratione2e-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [API change alternatives](#api-change-alternatives)
    - [Shorter whenCanBoost field](#shorter-whencanboost-field)
    - [Explicit preemption applicability object](#explicit-preemption-applicability-object)
    - [Annotation-based configuration](#annotation-based-configuration)
  - [Behavioral alternatives](#behavioral-alternatives)
<!-- /toc -->

## Summary

[KEP-7990](../7990-preemption-cost/README.md) defines Priority Boost as:

`effectivePriority = workload.priority + priorityBoost`

This KEP replaces KEP-7990 with a typed Workload API that adds the
`whenCanBoostOnPreemption` policy. The policy supports the existing `Always`
behavior and a new `PreempteeWithinClusterQueue` behavior. Starting with v0.20,
the structured API also replaces the Alpha `kueue.x-k8s.io/priority-boost`
annotation, which Kueue no longer reads.

## Motivation

A controller might boost one project's Workload only to rotate capacity among
that project's Workloads. In a Cohort where each ClusterQueue represents a
project, the unrestricted boost can also increase the Workload's ability to
reclaim or borrow quota by preempting another project's Workloads. The
controller needs to exclude the preemptor's boost from that cross-ClusterQueue
eligibility decision without changing scheduling or candidate ordering.

### Goals

- Preserve the existing behavior as the `Always` policy.
- Add a `PreempteeWithinClusterQueue` policy that lets a preemptor use its boost
  only when the candidate preemptee belongs to the same ClusterQueue.
- Keep scheduling order and preemption candidate ordering based on boosted
  priority for both policies.
- Define behavior for same-ClusterQueue, cross-ClusterQueue, and mixed-victim
  preemption attempts.
- Replace the Alpha annotation with a typed Workload API that publishes the
  value and applicability policy.

### Non-Goals

The non-goals from KEP-7990 remain unchanged. Additionally, this KEP does not:

- Guarantee that a Workload using `PreempteeWithinClusterQueue` can never
  preempt a Workload from another ClusterQueue. Cross-ClusterQueue preemption
  can still be allowed by base priority, hierarchical quota entitlement, Fair
  Sharing, or a priority-independent preemption policy.
- Introduce a new ClusterQueue preemption policy or alter existing quota
  entitlement.

## Proposal

This KEP adds a preemption applicability policy to the signed Priority Boost
value defined by KEP-7990. The policy controls only whether a Workload acting as
a preemptor can use its configured boost in a priority-based eligibility
comparison. Scheduling order and preemption candidate ordering continue to use
boosted priority for both policies.

The `whenCanBoostOnPreemption` field supports two policies:

- **`Always`**: preserve the KEP-7990 behavior by including the preemptor's
  boost for every candidate preemptee.
- **`PreempteeWithinClusterQueue`**: include the preemptor's boost only when the
  candidate preemptee belongs to the same ClusterQueue. For a candidate in a
  different ClusterQueue, use the preemptor's base priority. The candidate's
  own boosted priority remains unchanged.

### User Stories

[KEP-7990](../7990-preemption-cost/README.md#user-stories) contains the existing
Priority Boost user stories. This KEP adds the following story. The fragment
uses the proposed API and includes only relevant fields.

#### ClusterQueue-local priority boost

As a project administrator, I want a boost to reorder and preempt Workloads in
my ClusterQueue without the boost increasing my project's ability to preempt
Workloads in another ClusterQueue.

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: Workload
metadata:
  name: project-a-workload
  namespace: project-a
spec:
  queueName: project-a
  priorityBoost:
    value: 100
    whenCanBoostOnPreemption: PreempteeWithinClusterQueue
```

## Design Details

### Priority model

Base priority and configured boost retain the meanings defined by KEP-7990.
This KEP adds the concept of **applied boost**. When the feature gate is
enabled, it equals the configured boost for scheduling order, candidate
ordering, and a Workload evaluated as a preemptee. Kueue sets it to zero for a
`PreempteeWithinClusterQueue` Workload acting as a preemptor against a candidate
in another ClusterQueue. When the feature gate is disabled or `priorityBoost`
is unset, no boost is applied. Disabling the feature gate does not remove the
stored field, so re-enabling it restores the configured behavior.

`effectivePriority = basePriority + appliedBoost`

The table shows how each policy calculates priority. `basePriority +
appliedBoost` means the configured boost participates in the operation.
`basePriority` means that the preemptor's configured boost is ignored. In the
preemption eligibility rows, the policy column describes the preemptor. The
preemptee always uses its boosted priority.

| Priority-dependent operation | `Always` | `PreempteeWithinClusterQueue` |
|---|---|---|
| Order pending Workloads in the same ClusterQueue | `basePriority + appliedBoost` for each Workload | `basePriority + appliedBoost` for each Workload |
| Order heads from different ClusterQueues | `basePriority + appliedBoost` for each head | `basePriority + appliedBoost` for each head |
| Evaluate preemption eligibility, equality, or thresholds within the preemptor's ClusterQueue | `basePriority + appliedBoost` for the preemptor and preemptee | `basePriority + appliedBoost` for the preemptor and preemptee |
| Evaluate preemption eligibility, equality, or thresholds across ClusterQueues | `basePriority + appliedBoost` for the preemptor and preemptee | `basePriority` for the preemptor and `basePriority + appliedBoost` for the preemptee |
| Order a candidate in the preemptor's ClusterQueue | `basePriority + appliedBoost` for the candidate | `basePriority + appliedBoost` for the candidate |
| Order a candidate in a different ClusterQueue | `basePriority + appliedBoost` for the candidate | `basePriority + appliedBoost` for the candidate |

#### Scheduling behavior

Scheduling behavior is identical for `Always` and
`PreempteeWithinClusterQueue`. Kueue uses boosted priority to order pending
Workloads within a ClusterQueue and to order heads from different
ClusterQueues. The policy is not evaluated during these operations because no
candidate preemptee exists yet.

For an admission that does not require preemption, both policies therefore have
the same effect. Priority Boost does not change quota ownership, Cohort
entitlement, or DRS calculations.

#### Preemption behavior

Preemption candidate eligibility is evaluated separately for each candidate:

- For a candidate in the preemptor's ClusterQueue, both policies include the
  preemptor's boost.
- For a candidate in a different ClusterQueue, `Always` includes the
  preemptor's boost, while `PreempteeWithinClusterQueue` uses the preemptor's
  base priority.

In both cases, the candidate preemptee uses its boosted priority. Its own policy
does not suppress the boost while it is being evaluated as a preemptee.
Equality checks and `borrowWithinCohort.maxPriorityThreshold` logic use the same
preemptor and preemptee priorities as eligibility.

Candidate ordering is also identical for both policies. Every candidate uses
its boosted priority, whether it belongs to the preemptor's ClusterQueue or a
different ClusterQueue. Existing criteria that prefer candidates from another
ClusterQueue remain unchanged.

A preemption plan can contain both local and cross-ClusterQueue victims. Each
candidate must independently satisfy eligibility using the preemptor priority
applicable to that relationship. Consequently, a cross-ClusterQueue victim may
still be selected when the scoped preemptor's base priority is sufficient, but
the scoped preemptor's boost alone cannot make that victim eligible. Filling
the requested quota with a mixed set does not retroactively apply the
preemptor's boost to cross-ClusterQueue eligibility comparisons.

### API Changes

The structured field replaces the Alpha
`kueue.x-k8s.io/priority-boost` annotation. Starting with v0.20, Kueue retires
the `PriorityBoost` feature gate and no longer reads the annotation. The
`PriorityBoostWithScopedPreemption` feature gate controls the structured field.
External controllers must write the field to continue applying a boost.

The field is added to both served Workload API versions so conversion between
v1beta1 and v1beta2 preserves it. The following schematic additions use the
v1beta2 names:

```go
type WorkloadSpec struct {
	...

	// priorityBoost configures an adjustment to the Workload priority and when
	// the Workload can use that adjustment while acting as a preemptor.
	// The field can be updated throughout the Workload's lifetime. If unset, no
	// adjustment is applied.
	// This field is alpha-level and is honored only when the
	// PriorityBoostWithScopedPreemption feature gate is enabled.
	//
	// +optional
	PriorityBoost *PriorityBoost `json:"priorityBoost,omitempty"`
}

// PriorityBoost configures a signed adjustment to a Workload's base priority.
type PriorityBoost struct {
	// value is added to the Workload's base priority. When the Workload acts as
	// a preemptor, whenCanBoostOnPreemption determines whether value is used.
	// Positive values increase priority and negative values decrease it.
	//
	// +required
	Value int32 `json:"value"`

	// whenCanBoostOnPreemption determines whether the Workload can use value
	// while acting as a preemptor. It does not affect scheduling order,
	// candidate ordering, or the Workload's priority when it is a preemptee.
	// The possible values are:
	//
	// - `Always`: include the preemptor's boost for every candidate preemptee.
	// - `PreempteeWithinClusterQueue`: include the preemptor's boost only when
	//   the candidate preemptee belongs to the same ClusterQueue.
	//
	// Defaults to Always.
	//
	// +kubebuilder:validation:Enum=Always;PreempteeWithinClusterQueue
	// +kubebuilder:default=Always
	// +optional
	WhenCanBoostOnPreemption PriorityBoostPolicy `json:"whenCanBoostOnPreemption,omitempty"`
}

// PriorityBoostPolicy determines when a Workload can use its priority boost
// while acting as a preemptor.
// +enum
type PriorityBoostPolicy string

const (
	// PriorityBoostAlways includes the preemptor's boost for every candidate.
	PriorityBoostAlways PriorityBoostPolicy = "Always"

	// PriorityBoostPreempteeWithinClusterQueue includes the preemptor's boost
	// only when the candidate belongs to the same ClusterQueue.
	PriorityBoostPreempteeWithinClusterQueue PriorityBoostPolicy = "PreempteeWithinClusterQueue"
)
```

### Reference controller changes

The reference controller patches only `.spec.priorityBoost`, writes its value
and `whenCanBoostOnPreemption`, and stops writing the retired annotation. It
defaults to `Always`. Operators must explicitly select
`PreempteeWithinClusterQueue`.

Kueue job integrations preserve `priorityBoost` when rebuilding a Workload
specification. Changes to the field refresh pending queue and admitted cache
state and requeue associated inadmissible Workloads so the next scheduling
cycle observes the new value.

### Risks and Mitigations

[KEP-7990](../7990-preemption-cost/README.md#risks-and-mitigations) covers the
risks inherent to Priority Boost itself. The scoped policy adds the following
risks.

#### Context-dependent priority

Context-dependent preemptor priority is harder to reason about if eligibility
checks use different rules. Scheduling and candidate ordering always use
boosted priority. During eligibility evaluation, a
`PreempteeWithinClusterQueue` preemptor uses boosted priority for a candidate in
its ClusterQueue and base priority for a candidate in another ClusterQueue. The
candidate always uses its boosted priority. Because the value is signed,
suppressing a negative boost raises the cross-ClusterQueue preemptor priority
back to its base priority. This can make a candidate eligible when the base
priority is sufficient, because the policy scopes the adjustment rather than
the base priority. Preemption decision logs include the policy, base priority,
applied boost, and whether the candidate is in the preemptor's ClusterQueue.

#### Scope mistaken for isolation

Users might interpret `PreempteeWithinClusterQueue` as a guarantee that no other
project's Workload can be preempted. Documentation distinguishes a suppressed
preemptor boost from other reasons that authorize cross-ClusterQueue
preemption. Operators that need isolation must also configure the relevant
ClusterQueue and Cohort preemption policies.

#### Removing the Alpha annotation

The annotation is part of an Alpha feature, so Kueue can remove it without
preserving backward compatibility. After upgrading to v0.20, a Workload that
relies only on the retired annotation loses its configured boost. Kueue does
not provide an automatic mixed-version migration because older binaries read
only the annotation and v0.20 binaries read only the field. Operators that need
continuous behavior must coordinate a CRD-first rollout with a controller that
temporarily writes both sources and replace `PriorityBoost` with
`PriorityBoostWithScopedPreemption` in the feature gate configuration as v0.20
binaries roll out. After all Kueue components run v0.20, they can verify the
field values and stop writing the annotation.

### Test Plan

#### Unit tests

- Add focused coverage for the preemptor-aware priority evaluator. Confirm that
  scheduling and candidate ordering use boosted priority for both policies and
  that the preemptee's boost remains applied. Cover positive, zero, and negative
  values. Confirm that `PreempteeWithinClusterQueue` suppresses only the
  preemptor's signed adjustment for cross-ClusterQueue eligibility and that
  `Always` remains unrestricted.
- Confirm that disabling `PriorityBoostWithScopedPreemption` ignores the field
  and that the retired annotation does not affect priority.
- Confirm that the API requires `value`, defaults the policy to `Always`, and
  rejects unknown policies. Confirm that v1beta1 and v1beta2 conversion
  preserves the field, Kueue job integrations preserve it when rebuilding a
  Workload specification, and the reference controller patches only the field
  it owns.

#### Integration/E2E tests

- Run a Cohort scenario confirming that `PreempteeWithinClusterQueue` retains
  boosted scheduling and candidate ordering but does not use the preemptor's
  boost to make a Workload in another ClusterQueue eligible as a victim.
- Confirm that an annotation-only Workload has zero configured boost after
  upgrade and that writing the structured field activates the boost.

### Graduation Criteria

Alpha to Beta:

- Complete API review for the proposed Workload field.
- Gather feedback on `Always` and `PreempteeWithinClusterQueue` semantics.
- Demonstrate that a `PreempteeWithinClusterQueue` preemptor's boost cannot
  expand cross-ClusterQueue priority-based eligibility.
- Validate the documented Alpha upgrade behavior with external controllers
  that previously wrote the annotation.
- Validate interaction with `LowerOrNewerEqualPriority`, cross-ClusterQueue
  priority thresholds, hierarchical Cohorts, and Fair Sharing.

Beta to GA:

- Graduate after the API, upgrade behavior, and scoped preemption semantics
  have remained stable through Beta.

## Implementation History

- 2026-08-09: KEP-13414 was split from KEP-7990 to define preemptor boost
  applicability and replace the Alpha annotation with a typed Workload field.

## Drawbacks

- Context-dependent preemptor priority is more complex to explain and inspect
  than one global numeric priority.

## Alternatives

### API change alternatives

The structured `priorityBoost.value` and
`priorityBoost.whenCanBoostOnPreemption` API is the proposal in this KEP. The
following representations implement the same preemptor behavior but differ
from that proposal. Their YAML fragments show only the changed fields. Most
alternatives use typed data in the Workload specification; the final
alternative keeps the configuration in annotations.

#### Shorter whenCanBoost field

Name the policy field `whenCanBoost` while retaining the same structured object
and enum values. The shorter name is less explicit and can suggest that the
policy also governs scheduling or candidate ordering.
`whenCanBoostOnPreemption` makes the preemptor-preemptee relationship and the
preemption-only effect explicit.

```yaml
spec:
  priorityBoost:
    value: 100
    whenCanBoost: PreempteeWithinClusterQueue
```

#### Explicit preemption applicability object

Place an explicit `preemptionApplicability` object under `priorityBoost` and
represent the preemptee ClusterQueue relationship in which the preemptor can
use its boost. This makes the comparison boundary explicit instead of assigning
a broad meaning to a generic scope enum. A discriminator could be added if
future policies need their own configuration.

The additional nesting is verbose for a two-policy API. It can also imply that
more relationship dimensions or policy-specific configuration will be added,
even when no such extension is planned.

The relationship-oriented form is:

```yaml
spec:
  priorityBoost:
    value: 100
    preemptionApplicability:
      preempteeClusterQueueRelationship: Same
```

The discriminated union form is:

```yaml
spec:
  priorityBoost:
    value: 100
    preemptionApplicability:
      type: PreempteeWithinClusterQueue
      preempteeWithinClusterQueue: {}
```

#### Annotation-based configuration

Keep the existing numeric annotation and add a second annotation describing its
preemption policy. This minimizes CRD schema changes and lets existing writers
continue to publish the value. Alternatively, one self-contained annotation
could encode both the value and policy, making their update atomic.

Although one patch can update both annotations atomically, separate annotation
keys permit independent field ownership and partial writers. Schema discovery
cannot express their relationship as clearly, string validation is less
discoverable, and this design would retain the Alpha annotation instead of
removing its support. A self-contained annotation avoids partial combinations
but makes the value opaque to generic Kubernetes tooling and requires custom
parsing and validation.

The separate-annotation form is:

```yaml
metadata:
  annotations:
    kueue.x-k8s.io/priority-boost: "100"
    kueue.x-k8s.io/priority-boost-when-can-boost-on-preemption: PreempteeWithinClusterQueue
```

The self-contained JSON annotation form is:

```yaml
metadata:
  annotations:
    kueue.x-k8s.io/priority-boost: '{"value": 100, "whenCanBoostOnPreemption": "PreempteeWithinClusterQueue"}'
```

### Behavioral alternatives

KEP-7990 evaluates a preemption-cost-only signal, priority-class mutation, and a
Kueue-native cost or boost policy. Preemption applicability does not change the
limitations of those designs, so they are not repeated here.
