# KEP-13416: Exact Topology Domain Distribution

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Reproducible Uneven Rack Placement](#story-1-reproducible-uneven-rack-placement)
    - [Story 2: Exact Distribution Across Blocks](#story-2-exact-distribution-across-blocks)
  - [Semantics](#semantics)
  - [Notes, Constraints, and Caveats](#notes-constraints-and-caveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API Changes](#api-changes)
  - [Validation](#validation)
  - [Scheduling](#scheduling)
    - [Scope Selection](#scope-selection)
    - [Exact Domain Matching](#exact-domain-matching)
    - [Parent Domain Feasibility](#parent-domain-feasibility)
    - [Assignment Construction](#assignment-construction)
  - [Failed Node Replacement](#failed-node-replacement)
  - [Feature Gate](#feature-gate)
  - [Upgrade, Downgrade, and Backwards Compatibility](#upgrade-downgrade-and-backwards-compatibility)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration Tests](#integration-tests)
    - [End-to-End Tests](#end-to-end-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [Stable](#stable)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [A Separate Annotation](#a-separate-annotation)
  - [Binding Counts to Named Domains](#binding-counts-to-named-domains)
  - [Repeating a Distribution Pattern](#repeating-a-distribution-pattern)
  - [Multiple Exact Levels](#multiple-exact-levels)
  - [Pod Topology Spread Constraints](#pod-topology-spread-constraints)
<!-- /toc -->

## Summary

This KEP extends Topology Aware Scheduling (TAS) so that a PodSet can request
an exact, potentially uneven distribution of pods across topology domains.
For example, a PodSet with eight pods can request that Kueue select three
distinct racks and assign exactly one pod to one rack, three pods to another,
and four pods to the third.

The existing multi-layer topology constraint API accepts one scalar `size` at
each topology level. A scalar size creates equal, fungible slices and therefore
only constrains assignments to multiples of that size. It cannot express an
uneven distribution such as `[1, 3, 4]`.

This KEP adds a `sizes` alternative to
`PodsetSliceRequiredTopologyConstraint`. `sizes` is an unordered multiset of
pod counts. Each entry is assigned to a distinct topology domain selected by
Kueue, and the sum of all entries must equal the fixed PodSet count.

The alpha scope supports one exact-distribution constraint at any configured
topology level. It does not support repeating patterns, multiple exact levels,
explicit physical domain names, or elastic PodSet counts.

## Motivation

Users running distributed training and high-performance computing workloads
often need reproducible topology shapes for performance measurement and
debugging. Communication behavior can differ substantially between an even
placement such as `[4, 4]` and an uneven placement such as `[1, 3, 4]`.

Kueue currently provides the following topology controls:

- Required topology places an entire PodSet within one domain at a requested
  level.
- Preferred topology attempts increasingly coarser domains before allowing a
  distributed assignment.
- Slice topology places equal-sized logical slices within domains at a
  requested level.
- Multi-layer slice topology applies an equal scalar slice size at each of up
  to three topology levels.

Equal slice sizes do not determine the final count assigned to each domain.
For an eight-pod PodSet with a rack slice size of two, the scheduler creates
four equal slices. Multiple slices can share a rack, so assignments such as
`[2, 2, 4]`, `[2, 6]`, and `[8]` are all possible. No scalar slice size can
require the final distribution `[1, 3, 4]`.

The default Kubernetes scheduler can balance pods using topology spread
constraints or bind pods to named topology domains using node affinity. It
cannot dynamically select topology domains and reserve an arbitrary exact
distribution for a complete PodSet. Kueue already performs group-level
admission and produces a `TopologyAssignment`, making it the appropriate layer
for this capability.

### Goals

- Allow a fixed-size PodSet to request an exact, uneven pod distribution at a
  configured topology level.
- Allow the exact level to be a rack, block, zone, hostname, or any other level
  represented in the selected TAS topology.
- Have Kueue select the physical topology domains based on feasibility and the
  configured TAS strategy.
- Assign each requested count to a distinct topology domain.
- Reject or leave pending a workload when the complete distribution cannot be
  satisfied.
- Preserve existing scalar `size` behavior without changes.
- Bound the alpha scheduling problem so feasibility can be determined with a
  deterministic matching algorithm.

### Non-Goals

- Selecting topology domains by explicit label value, such as `rack-a` or
  `rack-b`.
- Expressing exact physical node counts when multiple pods can share a node.
  The proposed values are pod counts per topology domain.
- Repeating a shorter distribution to fill a larger PodSet.
- Supporting more than one `sizes` constraint in a PodSet topology request.
- Expressing a nested exact distribution at multiple topology levels.
- Supporting partial admission or elastic changes to the PodSet count.
- Rebalancing an admitted and running PodSet when topology capacity changes.
- Extending topology spread constraints in the default Kubernetes scheduler.

## Proposal

Add an optional `sizes` field to each multi-layer topology constraint entry.
Exactly one of `size` and `sizes` must be specified.

For example:

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: uneven-rack-job
  labels:
    kueue.x-k8s.io/queue-name: user-queue
spec:
  parallelism: 8
  completions: 8
  completionMode: Indexed
  template:
    metadata:
      annotations:
        kueue.x-k8s.io/podset-required-topology: topology.example.com/block
        kueue.x-k8s.io/podset-slice-required-topology-constraints: |
          [
            {
              "topology": "topology.example.com/rack",
              "sizes": [1, 3, 4]
            }
          ]
    spec:
      containers:
      - name: worker
        image: registry.k8s.io/e2e-test-images/agnhost:2.53
        args: ["pause"]
      restartPolicy: Never
```

Kueue selects one feasible block and three distinct racks within that block.
The three selected racks receive one, three, and four pods respectively. The
list does not bind a particular count to a named rack.

### User Stories

#### Story 1: Reproducible Uneven Rack Placement

As a machine learning infrastructure engineer, I want to place eight workers
across three racks with exact counts `[1, 3, 4]` so that I can reproduce and
measure communication behavior associated with that topology shape.

#### Story 2: Exact Distribution Across Blocks

As a cluster user, I want to place 24 pods into two distinct blocks with exact
counts `[8, 16]` within one data center, without binding the workload to
cluster-specific block names.

For example:

```yaml
kueue.x-k8s.io/podset-required-topology: topology.example.com/datacenter
kueue.x-k8s.io/podset-slice-required-topology-constraints: |
  [
    {
      "topology": "topology.example.com/block",
      "sizes": [8, 16]
    }
  ]
```

### Semantics

The `sizes` field has the following semantics:

1. `sizes` is an unordered multiset. `[1, 3, 4]` and `[4, 1, 3]` request the
   same topology shape.
2. Each list entry is assigned to one distinct topology domain at the entry's
   `topology` level.
3. Kueue selects the topology domains. List positions do not refer to physical
   domain names or to an ordering of domains.
4. The value assigned to a domain is the exact number of pods from this PodSet
   that Kueue assigns to that domain.
5. Duplicate values are allowed. For example, `[2, 2, 4]` requests three
   distinct domains.
6. The sum of the list must equal the fixed PodSet count.
7. An exact distribution is a required constraint. Kueue does not fall back to
   scalar slicing or ordinary greedy placement when it cannot be satisfied.
8. At alpha, a topology request may contain only one constraint entry when
   that entry uses `sizes`. Containment is expressed using
   `podset-required-topology`.
9. The required topology level, when specified, must be strictly coarser than
   the exact-distribution level when `sizes` contains more than one entry.
10. Below the exact-distribution level, Kueue uses its existing capacity-based
    placement to assign pods to finer domains and hosts.

For example, the following request is contradictory and is rejected at
scheduling time because required topology asks for one rack while `sizes`
requires three distinct racks:

```yaml
kueue.x-k8s.io/podset-required-topology: topology.example.com/rack
kueue.x-k8s.io/podset-slice-required-topology-constraints: |
  [
    {
      "topology": "topology.example.com/rack",
      "sizes": [1, 3, 4]
    }
  ]
```

### Notes, Constraints, and Caveats

The values represent pod counts, not physical node counts. A distribution of
`[1, 3, 4]` corresponds to the same number of nodes only when workload
resource requests, affinity, or other scheduling constraints effectively
ensure one pod per node.

The complete distribution is attached to one PodSet. A workload with multiple
PodSets validates and schedules each PodSet independently, subject to existing
restrictions on PodSet groups and multi-layer topology constraints.

The alpha API intentionally requires users to provide the complete
distribution. A 16-pod PodSet cannot specify `[1, 3, 4]` and rely on Kueue to
repeat it. It can explicitly request `[1, 3, 4, 1, 3, 4]`, which requires six
distinct domains.

### Risks and Mitigations

**Scheduling cost:** Each candidate enclosing domain requires a feasibility
check against its descendant domains. The alpha API bounds `sizes` to 128
entries and supports only one exact level. Matching uses sorted scalar pod
capacities and is polynomial in the number of requested and available
domains.

**Fragmentation:** An exact request can leave unused capacity in selected
domains. The matching algorithm uses the smallest fitting domains under the
active TAS strategy to reduce avoidable waste.

**Starvation:** Exact distributions are stricter than aggregate capacity and
may remain pending even when the total number of free pod slots is sufficient.
The scheduler reports an explicit failure reason distinguishing exact-domain
infeasibility from insufficient aggregate capacity.

**Feature interaction:** Elastic admission, repeating slices, and multiple
exact levels introduce ambiguous grouping semantics. The alpha validation
rejects these combinations rather than attempting an implicit interpretation.

## Design Details

### API Changes

Extend `PodsetSliceRequiredTopologyConstraint` in the `kueue.x-k8s.io/v1beta2`
Workload API:

```go
// PodsetSliceRequiredTopologyConstraint defines a single slice topology
// constraint layer.
//
// Exactly one of size and sizes must be specified.
// +kubebuilder:validation:XValidation:rule="has(self.size) != has(self.sizes)",message="exactly one of size and sizes must be specified"
type PodsetSliceRequiredTopologyConstraint struct {
    // topology indicates the topology level required for this constraint.
    //
    // +required
    // +kubebuilder:validation:MinLength=1
    // +kubebuilder:validation:MaxLength=63
    Topology string `json:"topology,omitempty"`

    // size indicates the number of pods in each equal group at this topology
    // level.
    //
    // +optional
    // +kubebuilder:validation:Minimum=1
    Size int32 `json:"size,omitempty"`

    // sizes indicates the exact pod counts to assign to distinct domains at
    // this topology level. The list is an unordered multiset.
    //
    // +optional
    // +listType=atomic
    // +kubebuilder:validation:MinItems=1
    // +kubebuilder:validation:MaxItems=128
    // +kubebuilder:validation:items:Minimum=1
    Sizes []int32 `json:"sizes,omitempty"`
}
```

`Size` remains an `int32` rather than changing to a pointer to preserve Go
source compatibility for clients that construct the existing API type. An
omitted `size` is represented by zero, which is already invalid as a supplied
slice size. The generated CRD schema and admission validation enforce the
field union.

The existing annotation name remains unchanged:

```text
kueue.x-k8s.io/podset-slice-required-topology-constraints
```

No change is required to `TopologyAssignment`. It already records domain paths
and assigned counts, which can represent the result of exact matching.

The `v1beta1` Workload API has no multi-layer constraint field. Existing
conversion behavior that drops multi-layer constraints when converting from
`v1beta2` to `v1beta1` remains unchanged.

### Validation

Validation is split between job integration validation, the Workload webhook,
and scheduling-time validation when the selected `Topology` hierarchy is
known.

Creation-time validation enforces:

- Exactly one of `size` and `sizes` is present in every constraint entry.
- `sizes` has between 1 and 128 entries.
- Every value in `sizes` is greater than zero.
- At most one entry uses `sizes`.
- At alpha, if an entry uses `sizes`, it is the only entry in the constraints
  list.
- The sum is computed using `int64` and must equal `PodSet.Count`.
- The PodSet does not use partial admission (`MinCount` is unset).
- The PodSet does not combine `sizes` with preferred outer topology at alpha.
- Existing mutual exclusions with the legacy slice annotations and
  `podset-group-name` continue to apply.
- The `TASExactTopologyDistribution` feature gate is enabled.

The Workload webhook repeats structural and numeric checks as defense in depth
for direct Workload writes.

Scheduling-time validation enforces:

- The exact topology key exists in the selected ResourceFlavor's `Topology`.
- The exact level is below the required topology level when more than one
  distinct domain is requested.
- The selected topology contains enough eligible domains to satisfy the list.

### Scheduling

The scheduler continues to use the existing phase that calculates how many
pods can fit in each leaf and ancestor domain. The calculated `podCount` for a
domain is the scalar capacity used by exact matching.

The exact path is selected when the PodSet topology request contains `sizes`.
The scalar path remains unchanged.

#### Scope Selection

When `podset-required-topology` is present, Kueue evaluates candidate domains
at that level as enclosing scopes. For a required block and exact rack sizes,
each candidate block is evaluated using only racks descended from that block.

When no required topology is present, the eligible topology represented by
the selected ResourceFlavor is treated as the enclosing scope.

Preferred outer topology is not supported with exact distributions at alpha.
This avoids an implicit fallback changing the enclosing scope while retaining
a hard inner distribution.

#### Exact Domain Matching

For one candidate enclosing scope, Kueue obtains the available pod capacity of
every eligible domain at the exact level. It then matches the requested values
to distinct candidate domains.

A deterministic implementation can:

1. Sort requested sizes from largest to smallest.
2. For each requested size, choose the smallest unused domain whose pod
   capacity is at least that size.
3. Use the existing TAS ordering and domain identity as deterministic
   tie-breakers.
4. Fail the candidate scope if no unused domain fits a requested size.

For example:

```text
requested sizes: [1, 3, 4]
domain capacity: [1, 2, 3, 7, 10]

assignment:
4 -> capacity 7
3 -> capacity 3
1 -> capacity 1
```

The algorithm assigns at most one requested entry to each domain. This is a
threshold matching problem rather than general bin packing. Choosing the
smallest fitting domain while processing larger requests first preserves
larger domains for requirements that need them.

#### Parent Domain Feasibility

Aggregate capacity does not prove that an exact distribution fits. For
example, domain capacities `[2, 2, 4]` total eight pods but cannot satisfy
`[1, 3, 4]` because no domain can accommodate three pods after reserving a
distinct domain for each entry.

The scheduler therefore performs exact matching while evaluating candidate
enclosing domains. A candidate required domain is considered feasible only if
the complete requested multiset can be matched within it. The normal TAS
strategy is then used to choose among feasible enclosing domains.

This prevents the scheduler from selecting an aggregate-capacity fit that
fails during downward traversal when another enclosing domain could satisfy
the request.

#### Assignment Construction

After matching, Kueue sets each selected exact-level domain's assigned pod
count to its matched value. Existing downward traversal distributes that count
through finer topology levels to leaves. Existing assignment construction then
encodes the selected leaf domains and counts into `TopologyAssignment`.

The topology ungater already consumes domain counts from the assignment and
maps indexed pods to assigned domains. It does not need a new status format.

### Failed Node Replacement

Existing failed-node replacement detects incomplete uniform slices using
modulo arithmetic. That is insufficient for an uneven exact distribution.

For an exact assignment, replacement must preserve the assigned count of the
affected exact-level domain. Before removing the unhealthy leaf from the
existing assignment, Kueue derives and retains its ancestor domain at the
exact level. Replacement pods are constrained to that same domain.

If replacement cannot fit within the affected domain, Kueue does not place the
pods in another domain because doing so would change the exact distribution.
The existing configured failure behavior, including fail-fast or eviction,
applies.

Moving an entire exact group to a different domain is out of scope for alpha.

### Feature Gate

Introduce the alpha feature gate:

```text
TASExactTopologyDistribution
```

A separate gate is used because `TASMultiLayerTopology` is already beta while
the `sizes` semantics and matching behavior are new. Disabling the gate
preserves all existing scalar multi-layer behavior and rejects new requests
that contain `sizes`.

### Upgrade, Downgrade, and Backwards Compatibility

Adding `sizes` is backwards compatible for existing manifests and Go clients
using `Size`. Existing scalar requests follow the unchanged scheduling path.

Upgrading the CRDs adds the optional field before the feature is enabled. When
the gate is disabled, new `sizes` requests are rejected.

Before downgrading to a Kueue version whose CRD does not contain `sizes`,
operators must ensure that no Jobs, Workloads, or other integrated workload
templates contain the field. Older controllers cannot preserve or interpret
the exact distribution and may see the constraint as missing its required
scalar size.

### Test Plan

[x] I/we understand the owners of the involved components may require updates
to existing tests to make the code solid enough before committing the changes
necessary to implement this enhancement.

#### Unit Tests

API and job framework tests cover:

- Parsing valid `sizes` annotations.
- Rejecting both `size` and `sizes` in one entry.
- Rejecting entries with neither field.
- Rejecting empty lists, non-positive values, and more than 128 entries.
- Accepting duplicate values.
- Rejecting a sum different from the PodSet count.
- Rejecting partial admission and preferred outer topology.
- Rejecting multiple constraint entries when one uses `sizes` at alpha.
- Rejecting `sizes` when the feature gate is disabled.
- Workload webhook defense-in-depth validation.
- Generated deepcopy behavior for the `Sizes` slice.

Scheduler unit tests cover:

- Exact matching for `[1, 3, 4]` independent of input order.
- Deterministic selection when multiple domains have equal capacity.
- Aggregate capacity sufficient but exact matching infeasible.
- Too few distinct domains.
- Duplicate sizes such as `[2, 2, 4]`.
- Selecting a feasible enclosing block over an aggregate-only fit.
- Applying the active TAS strategy among multiple feasible scopes.
- Exact distributions at block, rack, and hostname levels.
- No regression in existing scalar slice and multi-layer tests.
- Failed-node replacement constrained to the affected exact domain.

#### Integration Tests

Single-cluster TAS integration tests verify:

- An eight-pod indexed Job obtains rack-level aggregate counts `[1, 3, 4]`.
- A 24-pod Job obtains block-level aggregate counts `[8, 16]`.
- A workload remains pending when exact matching is impossible even though
  aggregate capacity is sufficient.
- A workload selects a feasible enclosing domain when another candidate is
  infeasible.
- Disabling the feature gate rejects the annotation.
- Replacement of an unhealthy node preserves the exact-level distribution.

#### End-to-End Tests

Add an extended TAS end-to-end test using a fixed test topology. The test
creates an indexed Job, waits for admission, and verifies the aggregate
topology assignment and resulting pod placement. Existing scalar TAS end-to-end
tests continue to run unchanged.

### Graduation Criteria

#### Alpha

- API field, feature gate, validation, matching, and assignment are
  implemented.
- Unit and integration tests cover feasible and infeasible distributions.
- User documentation describes exact-distribution semantics and limitations.
- Failed-node replacement does not silently violate the distribution.

#### Beta

- Positive operational feedback from users running exact distributions.
- Scheduling latency and memory impact are measured for the maximum supported
  list length.
- Failure reasons are actionable and covered by tests.
- At least one release of alpha usage without unresolved correctness issues.
- The API limit and interactions with outer scalar constraints are revisited
  based on experience.

#### Stable

- At least two releases of beta usage without unresolved correctness issues.
- End-to-end tests are stable in periodic jobs.
- Upgrade and downgrade procedures are documented.
- Any expansion to elastic, repeating, or nested semantics is either completed
  under a separate KEP or explicitly retained as a non-goal.

## Implementation History

- 2026-08-24: Initial KEP draft.

## Drawbacks

Exact distributions can reduce schedulability and increase fragmentation
compared with ordinary TAS placement. A workload may remain pending even when
the cluster has enough aggregate capacity.

The proposal adds another placement path to a core scheduling component and
requires special handling in failure recovery. Restricting alpha to one fixed
distribution level limits this additional complexity.

The API describes pod counts rather than physical node counts, which users may
misinterpret when multiple pods can share a node.

## Alternatives

### A Separate Annotation

A new annotation such as `podset-domain-counts` could carry the distribution.
This separates the feature from multi-layer constraints but duplicates the
existing topology-and-size API. Extending the existing constraint entry keeps
topology grouping requests in one API family.

### Binding Counts to Named Domains

The API could map label values directly to counts:

```yaml
rack-a: 1
rack-b: 3
rack-c: 4
```

This supports physical-domain reproduction but couples workloads to a specific
cluster and bypasses TAS domain selection. It is left for a separate feature if
users require explicit domain identity.

### Repeating a Distribution Pattern

Kueue could allow a 16-pod PodSet to specify `[1, 3, 4]` and repeat the pattern
twice. This requires defining whether repeats share enclosing domains, whether
entries from different repeats may share exact-level domains, and how logical
pattern identity survives failure recovery. Alpha instead requires users to
provide the complete list.

### Multiple Exact Levels

A flat list cannot unambiguously describe different child distributions for
different parent sizes. For example, block sizes `[8, 16]` require separate
rack distributions for each block. Supporting this use case likely requires a
nested tree-shaped API rather than multiple independent `sizes` lists.

### Pod Topology Spread Constraints

Kubernetes topology spread constraints bound the skew between eligible
domains. They can encourage an even distribution but cannot require an
arbitrary vector such as `[1, 3, 4]` across dynamically selected domains.
