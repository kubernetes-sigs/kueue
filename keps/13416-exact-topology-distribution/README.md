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
    - [Preemption](#preemption)
    - [Assignment Construction](#assignment-construction)
  - [Failed Node Replacement](#failed-node-replacement)
  - [Failure Reporting](#failure-reporting)
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
  - [Multiple JobSet ReplicatedJobs](#multiple-jobset-replicatedjobs)
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
`PodsetSliceRequiredTopologyConstraint`. `sizes` is an ordered list of pod
counts. Each entry defines a contiguous pod-rank block and is assigned to a
distinct topology domain selected by Kueue. The sum of all entries must equal
the fixed PodSet count.

The alpha scope supports one exact-distribution constraint at any configured
topology level. It does not support repeating patterns, multiple exact levels,
explicit physical domain names, or elastic PodSet counts.

Exact distribution is an opt-in, required placement constraint for workloads
whose performance or application behavior depends on a predictable topology
shape. It is not intended to replace ordinary TAS placement: requesting an
exact shape reduces Kueue's placement flexibility and can increase
fragmentation and admission latency.

## Motivation

Distributed training and high-performance computing workloads can have
communication patterns whose runtime performance depends on how workers are
distributed across topology domains. For these workloads, selecting the right
number of racks or blocks is not always sufficient: the number of pods placed
in each selected domain can determine network contention, collective
communication cost, and overall job duration. For example, an even placement
such as `[4, 4]` can behave substantially differently from an uneven placement
such as `[1, 3, 4]`.

Some users therefore need a predictable topology shape as an application-level
placement requirement. Reproducing a benchmark configuration, investigating a
performance regression, and debugging topology-dependent behavior are
important secondary use cases, but the primary purpose of this proposal is to
provide stable placement semantics for workloads whose performance depends on
the distribution.

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

Exact placement necessarily trades cluster efficiency for predictability. It
can reject otherwise usable placements, leave capacity unused across topology
domains, and keep a workload pending even when sufficient aggregate capacity
exists. Consequently, this capability is explicitly requested by the user and
is treated as a hard constraint. Workloads that only need locality should
continue to use existing required, preferred, or scalar slice topology
controls, which give Kueue more freedom to find an efficient placement.

The default Kubernetes scheduler can balance pods using topology spread
constraints or bind pods to named topology domains using node affinity. It
cannot dynamically select topology domains and reserve an arbitrary exact
distribution for a complete PodSet. Kueue already performs group-level
admission and produces a `TopologyAssignment`, making it the appropriate layer
for this capability.

### Goals

- Allow a fixed-size PodSet to request an exact, uneven pod distribution at a
  configured topology level.
- Provide predictable topology shapes for workloads whose performance or
  application behavior depends on their per-domain pod distribution.
- Allow the exact level to be a rack, block, zone, hostname, or any other level
  represented in the selected TAS topology.
- Have Kueue select the physical topology domains based on feasibility and the
  configured TAS strategy.
- Assign each requested count to a distinct topology domain.
- Deterministically map each ordered count to a contiguous range of pod ranks.
- Require a stable, unique pod-rank source for exact-distribution PodSets.
- Reject or leave pending a workload when the complete distribution cannot be
  satisfied.
- Preserve existing scalar `size` behavior without changes.
- Keep exact distribution opt-in so ordinary TAS workloads retain existing
  placement flexibility and schedulability.
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
- Supporting exact distributions for PodSets without stable pod indexes at
  alpha.
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
list does not bind a particular count to a named rack. Its order binds the
counts to contiguous rank blocks: rank 0 is in the one-pod group, ranks 1-3 are
in the three-pod group, and ranks 4-7 are in the four-pod group.

### User Stories

#### Story 1: Reproducible Uneven Rack Placement

As a machine learning infrastructure engineer, I want one PodSet of eight
workers to be placed across three distinct racks with exact counts `[1, 3, 4]`
so that I can obtain predictable communication behavior and reproduce that
behavior when measuring performance or investigating a regression. I want
Kueue to select the rack identities dynamically rather than encoding them in
the workload.

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

1. `sizes` is ordered. `[1, 3, 4]` and `[4, 3, 1]` request the same aggregate
   domain counts but different pod-rank mappings.
2. Each list entry is assigned to one distinct topology domain at the entry's
   `topology` level.
3. Entry `i` owns the next `sizes[i]` contiguous pod ranks after all preceding
   entries. For `[1, 3, 4]`, the rank blocks are rank 0, ranks 1-3, and ranks
   4-7.
4. Kueue selects the physical topology domain for each entry. A list position
   identifies a rank block, not a topology label value such as `rack-a`.
5. The value assigned to a domain is the exact number of pods from this PodSet
   that Kueue assigns to that domain.
6. Duplicate values are allowed. For example, `[2, 2, 4]` requests three
   distinct domains.
7. The sum of the list must equal the fixed PodSet count.
8. An exact distribution is a required constraint. Kueue does not fall back to
   scalar slicing or ordinary greedy placement when it cannot be satisfied.
9. At alpha, a topology request may contain only one constraint entry when
   that entry uses `sizes`. Containment is expressed using
   `podset-required-topology`.
10. The required topology level, when specified, must be strictly coarser than
   the exact-distribution level when `sizes` contains more than one entry.
11. Below the exact-distribution level, Kueue uses its existing capacity-based
    placement to assign pods to finer domains and hosts.

Ordered rank-block semantics require every pod in the PodSet to have a stable,
unique rank in `[0, PodSet.Count)`. At alpha, `sizes` is accepted only when the
job integration supplies a `PodIndexLabel` and guarantees those indexes, such
as an Indexed Job. Integrations using subgroup indexes must provide the
existing `SubGroupIndexLabel` and `SubGroupCount` information needed to derive
one unique rank space for the complete PodSet.

If a pod is missing its expected rank label, has a duplicate rank, or has an
out-of-range rank, the topology ungater keeps the affected pods gated and
reports an error. It must not fall back to greedy domain assignment for an
exact-distribution PodSet, because that could silently violate the ordered
rank-block contract.

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

The alpha API intentionally requires users to provide the complete ordered
distribution. A 16-pod PodSet cannot specify `[1, 3, 4]` and rely on Kueue to
repeat it. It can explicitly request `[1, 3, 4, 1, 3, 4]`, which requires six
distinct domains and defines six rank blocks in that order.

A single-entry list such as `sizes: [8]` is permitted and is equivalent to
requiring the whole PodSet in one domain at that level. It is allowed for
uniformity rather than because it adds expressiveness, and
`podset-required-topology` remains the clearer way to express it. Validation
therefore accepts it instead of special-casing `MinItems=2`, and user
documentation directs single-domain users to required topology.

Users should request `sizes` only when the exact per-domain distribution is
material to workload performance or behavior. If locality alone is sufficient,
the existing TAS controls are more appropriate because they allow Kueue to use
available capacity more efficiently.

### Risks and Mitigations

**Scheduling cost:** Each candidate enclosing domain requires a feasibility
check against its descendant domains. The alpha API bounds `sizes` to 128
entries and supports only one exact level. Matching uses sorted scalar pod
capacities and is polynomial in the number of requested and available
domains.

**Fragmentation and admission latency:** An exact request can leave unused
capacity in selected domains and can remain pending when a less constrained
placement would fit. This is an inherent tradeoff of making the distribution a
hard requirement. The feature is opt-in, documentation directs users to
ordinary TAS controls when exactness is unnecessary, and domain selection
continues to follow the active TAS placement strategy.

**Starvation:** Exact distributions are stricter than aggregate capacity and
may remain pending even when the total number of free pod slots is sufficient.
The scheduler reports an explicit failure reason distinguishing exact-domain
infeasibility from insufficient aggregate capacity.

**Feature interaction:** Partial admission, elastic workload slices, repeating
slices, and multiple exact levels introduce ambiguous grouping semantics. The
alpha validation rejects these combinations rather than attempting an implicit
interpretation.

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
    // this topology level. List order defines contiguous pod-rank blocks.
    //
    // +optional
    // +listType=atomic
    // +kubebuilder:validation:MinItems=1
    // +kubebuilder:validation:MaxItems=128
    // +kubebuilder:validation:items:Minimum=1
    Sizes []int32 `json:"sizes,omitempty"`
}
```

The existing beta `Size` field changes from `+required` to `+optional` so that
the generated CRD no longer unconditionally includes `size` in each item's
`required` list. The CEL union instead requires exactly one of `size` and
`sizes`. Existing objects containing `size` remain valid.

`Size` remains an `int32` rather than changing to a pointer to preserve Go
source compatibility for clients that construct the existing API type. With
`omitempty`, a Go client setting `Size: 0` serializes `size` as absent. If
`Sizes` is also absent, the CEL union rejects the object as specifying neither
alternative. The generated CRD schema and admission validation enforce the
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
- The integration provides a stable `PodIndexLabel`; for Kubernetes Jobs,
  `completionMode` must be `Indexed`. Subgroup-based integrations must also
  provide valid subgroup index and count metadata.
- The PodSet does not use partial admission (`MinCount` is unset).
- The parent Job or other integrated workload has not opted into elastic
  workload slicing with `kueue.x-k8s.io/elastic-job: "true"`. This restriction
  applies even when `ElasticJobsViaWorkloadSlicesWithTAS` is enabled.
- The PodSet does not combine `sizes` with preferred outer topology at alpha.
- Existing mutual exclusions with the legacy slice annotations and
  `podset-group-name` continue to apply.
- The `TASExactTopologyDistribution` feature gate is enabled.

The Workload webhook repeats structural, numeric, partial-admission, and
elastic-workload checks as defense in depth for direct Workload writes.

Update-time validation preserves the fixed-count invariant:

- Before quota reservation, `sizes` or the PodSet count may change only when
  the resulting ordered list remains valid and its sum equals the new count.
- After quota reservation, the PodSet count and the value and order of `sizes`
  are immutable.
- Job integration webhooks reject updates to `parallelism`, `completions`,
  `replicas`, or another integration-specific field when the update would
  change the effective PodSet count of an exact-distribution request.
- Direct Workload updates that change the count or `sizes` after reservation
  are rejected.

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

A deterministic implementation separates feasibility from policy-driven
selection:

1. Tag every requested size with its original list index.
2. For feasibility, sort the tagged entries from largest to smallest, breaking
   equal-size ties by original index. Match each entry to the smallest unused
   capacity that fits. Fail the candidate scope if any entry cannot be matched.
3. After feasibility is established, build the selected matching by processing
   entries largest-first and choosing an unused fitting domain according to the
   active TAS placement mode. Existing affinity tiers are considered before
   capacity ordering, and topology domain identity is the final deterministic
   tie-breaker.
4. Retain the original list index on every match so assignment construction can
   restore rank-block order.

At the exact level, the existing placement modes are interpreted as follows:

- `BestFit` starts with fitting domains that have the most free pod capacity.
  This is the default mode and applies to every exact request unless
  `LeastFreeCapacity` is selected below.
- `LeastFreeCapacity` starts with the fitting domain that has the least free pod
  capacity. It applies only when the request is classified as unconstrained
  **and** the `TASProfileMixed` feature gate is enabled. With `TASProfileMixed`
  disabled, which is the default, exact requests always use `BestFit`.
- `BalancedPlacement` is not applicable to exact distributions at alpha.
  Balanced placement requires preferred topology, while preferred outer
  topology is rejected for `sizes` at alpha.

The request is classified using the existing TAS profile rules. In particular,
an exact-only slice request is an unconstrained TAS request, while a request
with `podset-required-topology` is a required TAS request. Exact matching does
not override the placement mode selected for that request.

For example:

```text
requested sizes: [1, 3, 4]
domain capacity: [1, 2, 3, 7, 10]

BestFit matching (default):
4 -> capacity 10
3 -> capacity 7
1 -> capacity 3

LeastFreeCapacity matching (unconstrained request, TASProfileMixed enabled):
4 -> capacity 7
3 -> capacity 3
1 -> capacity 1
```

The algorithm assigns at most one requested entry to each domain. This is a
threshold matching problem rather than general bin packing. Largest-first
processing ensures a smaller entry does not consume the only domain capable of
satisfying a larger entry. The feasibility result is independent of placement
policy; the selected feasible matching follows the active policy.

Both greedy variants above are in fact optimal for threshold matching by the
usual exchange argument, so the policy-driven pass in step 3 cannot fail on a
scope that step 2 declared feasible. The passes are kept separate so that
feasibility remains a property of capacity alone. A future placement mode that
is not capacity-monotonic can then be added without silently making previously
admissible workloads unschedulable, and step 2 continues to answer parent domain
feasibility with one well-understood rule.

#### Parent Domain Feasibility

Aggregate capacity does not prove that an exact distribution fits. For
example, domain capacities `[2, 2, 4]` total eight pods but cannot satisfy
`[1, 3, 4]` because no domain can accommodate three pods after reserving a
distinct domain for each entry.

The scheduler therefore performs exact matching while evaluating candidate
enclosing domains. A candidate required domain is considered feasible only if
the complete requested list can be matched within it. The normal TAS
strategy is then used to choose among feasible enclosing domains.

This prevents the scheduler from selecting an aggregate-capacity fit that
fails during downward traversal when another enclosing domain could satisfy
the request.

#### Preemption

TAS already evaluates assignments twice: once against current free capacity,
and once against a snapshot that simulates the capacity that preemption would
release. Exact distributions reuse that mechanism unchanged at alpha. Exact
matching runs against whichever snapshot the scheduler is currently evaluating,
so a preempting workload becomes admissible exactly when the complete list can
be matched within the simulated capacity.

Two consequences follow and are accepted for alpha:

- Preemption targets are still chosen by the existing preemption logic, which
  reasons about quota and priority rather than about exact-domain feasibility.
  It is therefore possible to preempt enough aggregate capacity without making
  the distribution matchable, in which case the exact request remains pending
  and the released capacity is available to other workloads. This mirrors the
  existing behavior for required topology, which has the same property.
- Kueue does not attempt to minimize preemptions with respect to the exact
  matching. Choosing the target set that makes `[1, 3, 4]` feasible at the
  lowest preemption cost is an optimization over both preemption and matching
  simultaneously, and is deliberately out of scope for alpha.

Making preemption exact-domain aware is a candidate beta improvement and is
listed under graduation criteria rather than implemented here.

#### Assignment Construction

After matching, Kueue sets each selected exact-level domain's assigned pod
count to its matched value. Existing downward traversal distributes that count
through finer topology levels to leaves. Existing assignment construction then
encodes the selected leaf domains and counts into `TopologyAssignment`.

Assignment construction emits exact groups in original `sizes` order. All leaf
domain entries descended from one selected exact-level domain are contiguous,
and their counts sum to that entry's requested size. Within an exact group,
existing TAS ordering determines leaf order. Assignment construction must not
reorder leaf entries across exact groups.

The topology ungater consumes domain counts in `TopologyAssignment` order and
maps consecutive indexed-pod ranks to those domains. Therefore, preserving the
ordered exact groups gives each `sizes` entry its specified contiguous rank
block without requiring a new status format. For `[1, 3, 4]`, rank 0 maps to
the first selected exact domain, ranks 1-3 to the second, and ranks 4-7 to the
third.

### Failed Node Replacement

Existing failed-node replacement detects incomplete uniform slices using
modulo arithmetic. That is insufficient for an uneven exact distribution.

For an exact assignment, replacement must preserve the assigned count of the
affected exact-level domain. The ancestor domain is derived from the assignment
itself rather than stored separately. `TopologyAssignment.Levels` records the
level keys, and each domain entry records its `Values`, so truncating an
unhealthy leaf's `Values` at the exact level yields the identifying path of its
exact-level ancestor. Kueue computes that path before removing the unhealthy
leaf, and constrains replacement pods to the domain it identifies.

Two cases need explicit handling:

- **The removed leaf is one of several in its exact group.** The remaining
  leaves still carry the ancestor path, so the derivation above is confirmed by
  the surviving entries.
- **The removed leaf is the only leaf of its exact group.** Removing it first
  would erase the only record of that group's ancestor. Kueue therefore derives
  and retains the path before mutating the assignment, and the exact group keeps
  its position in the assignment with a count that is temporarily unsatisfied
  while replacement is pending. If the ancestor domain no longer exists in the
  current topology snapshot, replacement fails and the configured failure
  behavior applies.

Replacement updates leaf entries within the affected exact group's existing
position in `TopologyAssignment`. It does not move that group before or after
another ordered group. Thus, the contiguous rank block owned by each `sizes`
entry remains stable when a finer-level leaf is replaced.

Preserving that position requires a change to assignment merging. Replacement
currently builds its result by merging the replacement assignment with the
existing one, and that merge re-sorts every domain entry lexicographically by
level values. Applied to an exact assignment it would reorder groups relative
to the requested `sizes` order and silently reassign rank blocks to different
domains, which is the same hazard that ordinary assignment construction must
avoid. Merging therefore needs an exact-aware path, enabled by the
`TASExactTopologyDistribution` feature gate, that merges replacement leaves into
their existing exact group and preserves group order. The scalar path keeps its
current lexicographic behavior unchanged.

The same requirement applies to any other code path that rebuilds a
`TopologyAssignment` from parts. Leader and worker PodSets are merged by the
same routine, and PodSet groups are already mutually exclusive with `sizes`, so
that path is not reachable for exact distributions at alpha. Validation must
keep enforcing that exclusion for the merge invariant to hold, and a test
asserts it.

If replacement cannot fit within the affected domain, Kueue does not place the
pods in another domain because doing so would change the exact distribution.
The existing configured failure behavior, including fail-fast or eviction,
applies.

Moving an entire exact group to a different domain is out of scope for alpha.

### Failure Reporting

The distinguishing failure mode of this feature is that a workload stays
pending while aggregate free capacity looks sufficient. Reporting it as a
generic topology fit failure would make that indistinguishable from ordinary
capacity exhaustion, so exact matching reports its own reason.

Exact matching reuses the existing TAS failure-reason plumbing, which already
carries a per-PodSet reason string into the workload's pending condition
message. Two new reasons are added:

- `topology exact distribution infeasible` when no candidate enclosing scope
  can match the complete list. The message names the requested list, the number
  of eligible domains at the exact level, and the largest entry that could not
  be matched, so that a user can tell an under-provisioned cluster from a
  fragmented one.
- `topology exact distribution needs more domains` when the number of eligible
  domains at the exact level is smaller than the number of entries. This is the
  common misconfiguration of requesting more distinct domains than the topology
  contains, and it is not fixable by waiting.

One metric is added, following the existing Kueue naming convention:

```text
kueue_tas_exact_distribution_failures_total
```

It is a counter of failed exact-matching attempts, labeled by
`cluster_queue` and `reason`, where `reason` takes the values above. The
`cluster_queue` and `reason` pairing matches existing ClusterQueue-scoped
metrics such as the eviction counters, and introduces no per-workload
cardinality. The metric carries the standard configurable ClusterQueue label
suffix and a `+metricsdoc:labels` marker, as required for new metrics. It is
registered only when the `TASExactTopologyDistribution` feature gate is enabled,
following the existing pattern for gated metrics.

### Feature Gate

Introduce the alpha feature gate:

```text
TASExactTopologyDistribution
```

The gate depends on `TopologyAwareScheduling` and `TASMultiLayerTopology`, and
is declared as such in the feature gate dependency table. The dependency on
`TASMultiLayerTopology` is structural rather than behavioral: `sizes` is carried
on `PodsetSliceRequiredTopologyConstraints`, which that gate owns, so enabling
exact distribution without it would expose a field that is otherwise inert.

A separate gate is used because `TASMultiLayerTopology` is already beta while
the `sizes` semantics and matching behavior are new. Disabling the gate
preserves all existing scalar multi-layer behavior and rejects new requests
that contain `sizes`.

### Upgrade, Downgrade, and Backwards Compatibility

Adding `sizes` preserves existing manifests and Go clients using `Size`.
Existing scalar requests follow the unchanged scheduling path. The schema
change relaxes the existing beta field from unconditionally required to one
optional arm of a required union; it does not make a previously valid scalar
object invalid.

Upgrading the CRDs adds the optional field before the feature is enabled. When
the gate is disabled, new `sizes` requests are rejected.

Before downgrading to a Kueue version whose CRD does not contain `sizes`,
operators must ensure that no Jobs, Workloads, or other integrated workload
templates contain the field. An older CRD schema has `required: [size,
topology]` for every constraint item. It rejects an object containing `sizes`
without `size` at API-server validation, before the older controller can read
or reconcile the object. This is a hard downgrade incompatibility rather than
an older-controller fallback behavior.

Stored Workloads containing `sizes` must be completed or deleted, and Job
templates containing the annotation must be removed, before installing the
older CRD. Downgrade documentation and release notes call out this prerequisite.

### Test Plan

[x] I/we understand the owners of the involved components may require updates
to existing tests to make the code solid enough before committing the changes
necessary to implement this enhancement.

#### Unit Tests

API and job framework tests cover:

- Parsing valid `sizes` annotations.
- Preserving `sizes` list order through annotation parsing, JSON round trips,
  and deepcopy.
- Rejecting both `size` and `sizes` in one entry.
- Rejecting entries with neither field.
- Rejecting a Go/API object for which `Size: 0` is omitted and `Sizes` is also
  absent.
- Rejecting empty lists, non-positive values, and more than 128 entries.
- Accepting duplicate values.
- Rejecting a sum different from the PodSet count.
- Rejecting non-indexed Jobs and integrations without a stable rank source.
- Rejecting partial admission and preferred outer topology.
- Rejecting `sizes` for an elastic Job, including when
  `ElasticJobsViaWorkloadSlicesWithTAS` is enabled.
- Rejecting scale-up, scale-down, and other post-reservation changes to the
  effective PodSet count.
- Allowing a pre-reservation count or `sizes` update only when the new sum and
  count match.
- Rejecting multiple constraint entries when one uses `sizes` at alpha.
- Rejecting `sizes` when the feature gate is disabled.
- Workload webhook defense-in-depth validation.
- Generated deepcopy behavior for the `Sizes` slice.
- Generated CRD validation accepts the existing scalar form and the new exact
  form, but rejects both and neither.
- An older CRD schema rejects an exact constraint that omits `size`.

Scheduler unit tests cover:

- Distinct rank-block semantics for `[1, 3, 4]` and `[4, 3, 1]`.
- Largest-first internal matching retains each entry's original list index.
- Missing, duplicate, or out-of-range pod ranks remain gated rather than
  falling back to greedy assignment.
- Deterministic selection when multiple domains have equal capacity.
- `BestFit` and `LeastFreeCapacity` select their documented exact-level
  matchings for the same feasible capacities.
- Exact distributions do not enter `BalancedPlacement` at alpha.
- Aggregate capacity sufficient but exact matching infeasible.
- Too few distinct domains.
- Duplicate sizes such as `[2, 2, 4]`.
- Selecting a feasible enclosing block over an aggregate-only fit.
- Applying the active TAS strategy among multiple feasible scopes.
- Exact distributions at block, rack, and hostname levels.
- `LeastFreeCapacity` is used only for an unconstrained exact request with
  `TASProfileMixed` enabled, and `BestFit` is used otherwise.
- A single-entry list such as `[8]` behaves like required topology at that
  level.
- No regression in existing scalar slice and multi-layer tests.
- Failed-node replacement constrained to the affected exact domain without
  changing ordered rank blocks.
- Merging a replacement assignment preserves exact group order rather than
  re-sorting entries lexicographically by level values.
- Replacement derives the exact-level ancestor when the unhealthy leaf is the
  only leaf of its exact group, and fails cleanly when that ancestor no longer
  exists in the topology snapshot.
- Scalar assignments still merge in lexicographic order when the feature gate
  is enabled, so the exact-aware merge path does not change existing behavior.
- Exact matching against a preemption-simulated snapshot admits the workload
  only when the complete list is matchable within simulated capacity.
- Failure reasons distinguish exact-domain infeasibility from too few eligible
  domains, and the failure counter increments with the matching `reason` label.

#### Integration Tests

Single-cluster TAS integration tests verify:

- An eight-pod indexed Job obtains rack-level aggregate counts `[1, 3, 4]`,
  with rank 0 in the first selected rack, ranks 1-3 in the second, and ranks
  4-7 in the third.
- Reordering the request to `[4, 3, 1]` changes those rank ranges while
  retaining the same aggregate multiset of domain counts.
- A 24-pod Job obtains block-level aggregate counts `[8, 16]`.
- A workload remains pending when exact matching is impossible even though
  aggregate capacity is sufficient, and reports the exact-distribution failure
  reason rather than a generic topology fit failure.
- A workload selects a feasible enclosing domain when another candidate is
  infeasible.
- Disabling the feature gate rejects the annotation.
- Replacement of an unhealthy node preserves both the exact-level distribution
  and the original rank-to-exact-domain mapping, including when the replaced
  leaf was the only leaf of its exact group.
- A workload admitted by preempting a lower-priority workload obtains the full
  ordered distribution.

#### End-to-End Tests

Add an extended TAS end-to-end test using a fixed test topology. The test
creates an indexed Job, waits for admission, and verifies the aggregate
topology assignment, the contiguous rank range assigned to each exact domain,
and the resulting pod placement. Existing scalar TAS end-to-end tests continue
to run unchanged.

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
- Failure reasons are actionable and covered by tests, and
  `kueue_tas_exact_distribution_failures_total` is confirmed useful for
  diagnosing pending exact workloads in practice.
- At least one release of alpha usage without unresolved correctness issues.
- The API limit and interactions with outer scalar constraints are revisited
  based on experience.
- Whether preemption should become exact-domain aware is decided based on
  observed pending workloads that had enough preemptible aggregate capacity but
  no matchable distribution.

#### Stable

- At least two releases of beta usage without unresolved correctness issues.
- End-to-end tests are stable in periodic jobs.
- Upgrade and downgrade procedures are documented.
- Any expansion to elastic, repeating, or nested semantics is either completed
  under a separate KEP or explicitly retained as a non-goal.

## Implementation History

- 2026-08-24: Initial KEP draft.
- 2026-08-28: Defined ordered rank mapping, placement-policy interaction, API
  compatibility, and fixed-count lifecycle validation.
- 2026-08-28: Added preemption behavior, failure reporting and metric, exact
  group ordering requirements for assignment merging, and exact-level ancestor
  derivation during failed node replacement.

## Drawbacks

Exact distributions can reduce schedulability and increase fragmentation
compared with ordinary TAS placement. A workload may remain pending even when
the cluster has enough aggregate capacity. This cost is justified only when a
predictable per-domain distribution materially affects workload performance or
behavior; testing and debugging are supported use cases, but exact placement is
not intended to be the default.

The proposal adds another placement path to a core scheduling component and
requires special handling in failure recovery. Restricting alpha to one fixed
distribution level limits this additional complexity.

The API describes pod counts rather than physical node counts, which users may
misinterpret when multiple pods can share a node.

## Alternatives

### Multiple JobSet ReplicatedJobs

A JobSet can model an uneven distribution as multiple `ReplicatedJob`s. Kueue
creates one PodSet for each `ReplicatedJob`, so a JobSet could define three
single-replica entries whose PodSet counts are one, three, and four. Each entry
can independently require rack topology:

```yaml
spec:
  replicatedJobs:
  - name: group-1
    replicas: 1
    template:
      spec:
        parallelism: 1
        completions: 1
        template:
          metadata:
            annotations:
              kueue.x-k8s.io/podset-required-topology: topology.example.com/rack
  - name: group-3
    replicas: 1
    template:
      spec:
        parallelism: 3
        completions: 3
        template:
          metadata:
            annotations:
              kueue.x-k8s.io/podset-required-topology: topology.example.com/rack
  - name: group-4
    replicas: 1
    template:
      spec:
        parallelism: 4
        completions: 4
        template:
          metadata:
            annotations:
              kueue.x-k8s.io/podset-required-topology: topology.example.com/rack
```

This is sufficient when the application already consists of separate
ReplicatedJobs and only requires each group to fit within some rack. A slice
size is unnecessary when each single-replica ReplicatedJob is already one
PodSet that must fit within one topology domain.

It does not provide the semantics proposed by this KEP. The PodSets are placed
independently, so their selected racks are not required to be distinct and are
not necessarily descendants of the same enclosing block. It also changes the
application model from one PodSet and one pod index space into multiple
ReplicatedJobs, and it is unavailable to workload types that cannot express
the groups as separate PodSets.

Splitting the PodSet also discards the ordered rank blocks that motivate this
proposal. Each ReplicatedJob has its own index space starting at zero, so there
is no single rank ordering over the eight workers and no way to state that ranks
1-3 must be co-located while rank 0 is not. Applications that derive collective
communicator ranks from one contiguous index space cannot use this alternative
without changing how they assign ranks.

This KEP retains `sizes` for workloads that require one PodSet to be divided
across dynamically selected distinct domains, optionally within one enclosing
domain. Users whose application can use independent ReplicatedJobs without
distinct-domain or shared-parent guarantees should use the existing JobSet
functionality instead.

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
