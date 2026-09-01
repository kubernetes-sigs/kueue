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
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
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

This KEP extends Topology Aware Scheduling (TAS) so that a PodSet can request an
exact, potentially uneven distribution of pods across topology domains. For
example, a PodSet with eight pods can request that Kueue select three distinct
racks and assign exactly one pod to one rack, three to another, and four to the
third.

It adds a `sizes` alternative to `PodsetSliceRequiredTopologyConstraint`.
`sizes` is an ordered list of pod counts. Each entry defines a contiguous
pod-rank block and is assigned to a distinct topology domain selected by Kueue.
The sum of all entries must equal the fixed PodSet count.

## Motivation

Distributed training and high-performance computing workloads can have
communication patterns whose runtime performance depends on how workers are
distributed across topology domains. Selecting the right *number* of racks or
blocks is not always sufficient: the number of pods placed in each selected
domain determines network contention, collective communication cost, and overall
job duration. An even placement such as `[4, 4]` can behave substantially
differently from an uneven `[1, 3, 4]`. Reproducing a benchmark configuration and
investigating a performance regression are important secondary use cases.

Kueue currently provides required topology (whole PodSet in one domain),
preferred topology (increasingly coarser domains before allowing a distributed
assignment), slice topology (equal-sized logical slices), and multi-layer slice
topology (an equal scalar slice size at each of up to three levels).

None of these determine the final count per domain. For an eight-pod PodSet with
a rack slice size of two, the scheduler creates four equal slices, and multiple
slices can share a rack — so `[2, 2, 4]`, `[2, 6]`, and `[8]` are all valid
outcomes. No scalar slice size can require `[1, 3, 4]`.

The default Kubernetes scheduler can balance pods using topology spread
constraints or bind them to named domains using node affinity, but it cannot
dynamically select domains and reserve an arbitrary exact distribution for a
complete PodSet. Kueue already performs group-level admission and produces a
`TopologyAssignment`, making it the right layer for this capability.

### Goals

- Allow a fixed-size PodSet to request an exact, uneven pod distribution at any
  configured topology level — rack, block, zone, hostname, or otherwise.
- Have Kueue select the physical domains based on feasibility and the configured
  TAS strategy, assigning each requested count to a distinct domain.
- Deterministically map each ordered count to a contiguous range of pod ranks,
  which requires a stable, unique pod-rank source.
- Reject or leave pending a workload when the complete distribution cannot be
  satisfied.
- Preserve existing scalar `size` behavior unchanged, and keep exact distribution
  opt-in.


### Non-Goals

- Selecting topology domains by explicit label value, such as `rack-a`.
- Expressing exact physical node counts when multiple pods can share a node. The
  values are pod counts per domain.
- Repeating a shorter distribution to fill a larger PodSet.
- More than one `sizes` constraint per request, or a nested exact distribution
  across multiple topology levels.
- Partial admission or elastic changes to the PodSet count.
- Exact distributions for PodSets without stable pod indexes at alpha.
- Rebalancing an admitted and running PodSet when topology capacity changes.

## Proposal

Add an optional `sizes` field to each multi-layer topology constraint entry.
Exactly one of `size` and `sizes` must be specified.

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

Kueue selects one feasible block and three distinct racks within it, receiving
one, three, and four pods respectively. The list does not bind a count to a named
rack. Its *order* binds counts to contiguous rank blocks: rank 0 is in the
one-pod group, ranks 1-3 in the three-pod group, and ranks 4-7 in the four-pod
group.

### User Stories

#### Story 1: Reproducible Uneven Rack Placement

As a machine learning infrastructure engineer, I want one PodSet of eight workers
placed across three distinct racks with exact counts `[1, 3, 4]`, so that I get
predictable communication behavior and can reproduce it when measuring
performance or investigating a regression. Kueue should select the rack
identities dynamically rather than requiring them in the workload.

#### Story 2: Exact Distribution Across Blocks

As a cluster user, I want to place 24 pods into two distinct blocks with exact
counts `[8, 16]` within one data center, without binding the workload to
cluster-specific block names. This is the same request shape at a coarser level:
`podset-required-topology` names the data center and the constraint entry names
the block level.

### Semantics

1. `sizes` is ordered. Entry `i` defines the next `sizes[i]` contiguous pod ranks.
   For `[1, 3, 4]`, the groups are rank 0, ranks 1-3, and ranks 4-7.
2. Each entry specifies the exact pod count for one distinct domain at the named
   topology level, so the list length is the number of domains used at that
   level. Kueue selects the physical domains; a list position does not name a
   domain or correspond to any ordering of the topology itself.
3. Thus, `[1, 3, 4]` and `[4, 3, 1]` require the same three domain sizes but group
   ranks differently.
4. Duplicate values are allowed. `[2, 2, 4]` requests three distinct domains.
5. The sum of the list must equal the fixed PodSet count.
6. An exact distribution is a required constraint. Kueue does not fall back to
   scalar slicing or ordinary greedy placement when it cannot be satisfied.
7. At alpha, an entry using `sizes` must be the only entry in the constraints
   list. Containment is expressed using `podset-required-topology`, which must be
   strictly coarser than the exact level when `sizes` has more than one entry. A
   request naming the same level in both is contradictory: the identical-string
   case is rejected at creation time, and the general ordering check happens at
   scheduling time, once the topology hierarchy is known.
8. Below the exact level, Kueue uses its existing capacity-based placement to
   assign pods to finer domains and hosts.

Ordered rank-block semantics require every pod to have a stable, unique rank in
`[0, PodSet.Count)`. At alpha, `sizes` is accepted only when the job integration
supplies a `PodIndexLabel` and guarantees those indexes, such as an Indexed Job.
Integrations using subgroup indexes must also provide `SubGroupIndexLabel` and
`SubGroupCount`, which are what make one unique rank space derivable for the
complete PodSet.

If any pod is missing its rank label, has a duplicate rank, or has an
out-of-range rank, ranks cannot be derived for the PodSet at all, so the topology
ungater keeps every gated pod in that PodSet gated and reports an error. It must
not fall back to greedy domain assignment for an exact-distribution PodSet.
Greedy assignment still satisfies the aggregate per-domain counts, so the
violation would be silent — the workload would run with the wrong ranks
co-located and nothing would report a failure.

### Notes, Constraints, and Caveats

The values are pod counts, not node counts. `[1, 3, 4]` corresponds to the same
number of nodes only when resource requests, affinity, or other constraints
effectively ensure one pod per node.

The distribution is attached to one PodSet. A workload with multiple PodSets
validates and schedules each independently, subject to existing restrictions on
PodSet groups and multi-layer constraints.

A single-entry list such as `sizes: [8]` is permitted: it places the whole
PodSet in one domain, the same shape `podset-required-topology` produces at that
level. The two are still not interchangeable. A request carrying only slice
constraints counts as unconstrained, whereas `podset-required-topology` makes it
a required request, and those two classes order candidate domains differently
(see [Exact Domain Matching](#exact-domain-matching)) — so Kueue may pick a
different domain for each. Use `podset-required-topology` for the single-domain
case: it needs neither this feature gate nor a stable rank source.

### Risks and Mitigations

**Scheduling cost:** Each candidate enclosing domain requires a feasibility check
against its descendant domains. The alpha API bounds `sizes` to 128 entries and
supports only one exact level. Matching uses sorted scalar pod capacities and is
polynomial in the number of requested and available domains.

**Fragmentation and starvation:** An exact request is stricter than aggregate
capacity. It can leave capacity unused in selected domains and remain pending
even when the total number of free pod slots would suffice. This is inherent to
making the distribution a hard requirement. The feature is opt-in, documentation
directs users to ordinary TAS controls when exactness is unnecessary, domain
selection follows the active TAS placement strategy, and the scheduler reports an
explicit failure reason distinguishing exact-domain infeasibility from
insufficient aggregate capacity.

**Feature interaction:** Partial admission, elastic workload slices, repeating
slices, and multiple exact levels introduce ambiguous grouping semantics. Alpha
validation rejects these combinations rather than guessing.

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

`Size` changes from `+required` to `+optional`, so the generated CRD stops
listing it in each item's `required` set and the CEL union enforces exactly one
arm instead. It stays an `int32` rather than a pointer, to preserve Go source
compatibility for clients constructing the existing type — and with `omitempty`,
a client setting `Size: 0` serializes it as absent, so the union correctly
rejects an object that sets neither field.

The `podset-slice-required-topology-constraints` annotation name is unchanged,
and `TopologyAssignment` already records domain paths and assigned counts, so
neither needs a change. `v1beta1` has no multi-layer constraint field, so `sizes`
inherits the existing behavior of dropping those constraints during conversion.

### Validation

Validation is split between job integration validation, the Workload webhook, and
scheduling-time validation once the selected `Topology` hierarchy is known.

Creation-time validation enforces:

- The structural schema above: exactly one of `size` and `sizes`, 1 to 128
  entries, every value greater than zero.
- At alpha, an entry using `sizes` is the only entry in the constraints list.
- The exact topology key is not the same string as `podset-required-topology`.
  This catches the contradictory same-level request without needing the topology
  hierarchy; the coarser-than ordering check happens at scheduling time.
- The sum, computed using `int64`, equals `PodSet.Count`.
- The integration provides a stable `PodIndexLabel`; for Kubernetes Jobs,
  `completionMode` must be `Indexed`. Subgroup-based integrations must also
  provide valid subgroup index and count metadata.
- The PodSet does not use partial admission (`MinCount` is unset).
- The parent workload has not opted into elastic workload slicing with
  `kueue.x-k8s.io/elastic-job: "true"`, even when
  `ElasticJobsViaWorkloadSlicesWithTAS` is enabled.
- `sizes` is not combined with preferred outer topology at alpha.
- Existing mutual exclusions with the legacy slice annotations and
  `podset-group-name` continue to apply.
- The `TASExactTopologyDistribution` feature gate is enabled.

The Workload webhook repeats the structural, numeric, partial-admission, and
elastic-workload checks, so that a Workload written directly is checked too.

Update-time validation preserves the fixed-count invariant. Existing Workload
validation already makes the whole PodSet immutable once quota is reserved, so
`sizes` and the count inherit that. Two additions:

- Before quota reservation, `sizes` or the count may change only when the new sum
  equals the new count.
- That immutability check has an exception letting elastic jobs shrink the
  count. The exception is keyed on the `ElasticJobsViaWorkloadSlices` gate, not
  on whether this particular workload is elastic, so it must be refused for
  `sizes` PodSets. Otherwise the sum stops matching the count.

Job integration webhooks also reject `parallelism`, `completions`, or `replicas`
updates that would change an exact-distribution PodSet's count. This is a
behavior change scoped to those PodSets: today the edit is accepted and the
workload is later finished as out of sync.

Scheduling-time validation enforces that the exact topology key exists in the
selected ResourceFlavor's `Topology`, that the exact level is below the required
topology level when more than one distinct domain is requested, and that the
topology contains enough eligible domains to satisfy the list.

### Scheduling

The scheduler continues to use the existing phase that calculates how many pods
fit in each leaf and ancestor domain; the resulting `podCount` is the scalar
capacity used by exact matching. The exact path is selected when the topology
request contains `sizes`. The scalar path is unchanged.

#### Scope Selection

When `podset-required-topology` is present, Kueue evaluates candidate domains at
that level as enclosing scopes — for a required block and exact rack sizes, each
candidate block is evaluated using only its descendant racks. When no required
topology is present, the eligible topology of the selected ResourceFlavor is the
enclosing scope.

Preferred outer topology is not supported with exact distributions at alpha, to
avoid an implicit fallback changing the enclosing scope while a hard inner
distribution is retained.

#### Exact Domain Matching

For one candidate enclosing scope, Kueue obtains the available pod capacity of
every eligible domain at the exact level, then matches the requested values to
distinct domains, separating feasibility from policy-driven selection:

1. Tag every requested size with its original list index.
2. For feasibility, sort the tagged entries largest to smallest, breaking
   equal-size ties by original index, and match each to the smallest unused
   capacity that fits. Fail the candidate scope if any entry cannot be matched.
3. Build the selected matching by processing entries largest-first and choosing
   an unused fitting domain according to the active TAS placement mode. Existing
   affinity tiers are considered before capacity ordering, and domain identity is
   the final deterministic tie-breaker.
4. Retain the original list index on every match so assignment construction can
   restore rank-block order.

At the exact level the placement modes are interpreted as follows:

- `BestFit` starts with fitting domains that have the most free pod capacity. It
  applies to any exact request carrying `podset-required-topology`, and to every
  exact request under a BestFit-only profile.
- `LeastFreeCapacity` starts with the fitting domain that has the least free pod
  capacity. It applies to unconstrained requests under the default TAS profile
  (`TASProfileMixed`, default since v0.15), so an exact-only slice request uses
  it unless a cluster overrides the profile.
- `BalancedPlacement` never runs at alpha. It only runs when a request is
  neither required nor unconstrained, and every `sizes` request alpha allows is
  one of those two: a `sizes`-only request counts as unconstrained, and adding
  `podset-required-topology` makes it required. So there is nothing to turn off
  here. If preferred topology is ever allowed with `sizes`, then `sizes` must
  win, because balanced placement spreads pods evenly while `sizes` asks for an
  uneven split. Both cannot be true at once.

The request is classified using the existing TAS profile rules: an exact-only
slice request is unconstrained, while a request with `podset-required-topology`
is required. Exact matching does not override the resulting placement mode.

Following the profile has one side effect worth calling out. By default a
`sizes`-only request gets `LeastFreeCapacity`, which picks the domains that
barely fit, leaving almost no spare room in them. If a node then fails,
replacement has to stay inside that same domain and is not allowed to move the
group elsewhere, so there may be nowhere to put the replacement pods.

`BestFit` would leave spare room, but it uses roomier domains and so wastes more
capacity. Alpha keeps following whichever profile the cluster is set to, rather
than picking one for the user: forcing a different algorithm for just this
feature is a big change to make without data. Beta decides it, based on how
often replacement actually fails.

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

Each domain takes at most one entry from the list, so this is a simpler problem
than bin packing: nothing is ever split across domains. Working from the largest
entry down matters, because otherwise a small entry could take the only domain
big enough for a large one.

#### Parent Domain Feasibility

Aggregate capacity does not prove that an exact distribution fits. Domain
capacities `[2, 2, 4]` total eight pods but cannot satisfy `[1, 3, 4]`, because
no domain can accommodate three pods once a distinct domain is reserved for each
entry.

The scheduler therefore performs exact matching while evaluating candidate
enclosing domains, treating a candidate as feasible only if the complete list can
be matched within it, and then uses the normal TAS strategy to choose among
feasible candidates.

#### Preemption

TAS already validates a preempting workload's placement twice, and exact
matching plugs into both without new code:

1. **Upper bound.** Before preemption is contemplated, the assignment is
   recomputed against a snapshot simulating the whole cluster as empty. If the
   PodSet does not fit even then, the mode becomes `NoFit` and no preemption is
   issued. For an exact distribution this catches the case where the topology
   simply lacks enough eligible domains.
2. **Candidate set.** Once specific preemption targets are chosen, the
   assignment is recomputed against a snapshot with exactly those targets'
   usage removed. Exact matching therefore runs against the capacity those
   targets would actually release, rather than against aggregate quota.

Step 2 is what stops this feature from preempting for nothing, and it needs one
fix. Today the recomputed result is saved but the assignment mode is left alone,
so even when no placement was found the mode stays at `Preempt` and the
preemption goes ahead.

For ordinary placement this rarely matters, because freeing more capacity almost
always means more pods fit. Exact matching does not work that way. Freeing room
for eight pods spread as `[2, 2, 4]` still cannot hold `[1, 3, 4]`, because
nothing has room for three. So alpha treats a missing assignment after step 2 as
`NoFit` and puts the workload back in the queue, instead of evicting other
workloads for a shape that will not fit anyway.

One limitation is accepted for alpha: Kueue does not *choose* preemption targets
to make the distribution matchable. Target selection remains driven by quota and
priority, and the checks above only reject unusable candidate sets rather than
searching for a usable one. Selecting the target set that makes `[1, 3, 4]`
feasible while evicting as little as possible means solving both problems at
once, which is a candidate beta improvement.

#### Assignment Construction

After matching, Kueue sets each selected exact-level domain's assigned pod count
to its matched value. Existing downward traversal distributes that count through
finer levels to leaves, and existing assignment construction encodes the selected
leaves and counts into `TopologyAssignment`.

Assignment construction emits exact groups in original `sizes` order. All leaf
entries descended from one selected exact-level domain are contiguous and their
counts sum to that entry's requested size; within a group, existing TAS ordering
determines leaf order. Construction must not reorder leaf entries across groups.

The topology ungater consumes domain counts in `TopologyAssignment` order and
maps consecutive indexed-pod ranks to those domains. Preserving group order is
therefore what gives each `sizes` entry its contiguous rank block, without
requiring a new status format.

### Failed Node Replacement

Existing failed-node replacement detects incomplete uniform slices using modulo
arithmetic, which is insufficient for an uneven distribution.

Replacement must preserve the assigned count of the affected exact-level domain.
The ancestor is derived from the assignment rather than stored separately:
`TopologyAssignment.Levels` records the level keys and each entry records its
`Values`, so truncating an unhealthy leaf's `Values` at the exact level yields
the ancestor's identifying path. Kueue computes that path *before* removing the
leaf — otherwise, when the removed leaf is the only one in its group, removal
would erase the only record of the ancestor. The group keeps its position with a
temporarily unsatisfied count while replacement is pending. If the ancestor
domain no longer exists in the current snapshot, replacement fails and the
configured failure behavior applies.

Replacement updates leaf entries within the affected group's existing position
and does not move that group relative to another, so each `sizes` entry's rank
block stays stable.

Preserving that position requires a change to assignment merging. Replacement
currently merges the replacement assignment with the existing one, and that merge
re-sorts every entry lexicographically by level values — which would reorder
groups relative to `sizes` order and silently reassign rank blocks. Merging
therefore needs an exact-aware path, gated by
`TASExactTopologyDistribution`, that merges replacement leaves into their
existing group and preserves group order; the scalar path keeps its current
behavior. The same requirement applies to any other path that rebuilds a
`TopologyAssignment` from parts. Leader and worker PodSets use the same merge
routine, but PodSet groups are mutually exclusive with `sizes`, so that path is
unreachable at alpha — validation must keep enforcing that exclusion, and a test
asserts it.

If replacement cannot fit within the affected domain, Kueue does not place the
pods elsewhere, because that would change the distribution; the configured
failure behavior, including fail-fast or eviction, applies. Moving an entire
exact group to a different domain is out of scope for alpha.

### Failure Reporting

The distinguishing failure mode of this feature is a workload staying pending
while aggregate free capacity looks sufficient. Reported as a generic topology
fit failure it would be indistinguishable from ordinary capacity exhaustion, so
exact matching reuses the existing TAS failure-reason plumbing — which already
carries a per-PodSet reason into the workload's pending condition — with two new
reasons:

- `topology exact distribution infeasible` when no candidate enclosing scope can
  match the complete list. The message names the requested list, the number of
  eligible domains at the exact level, and the largest entry that could not be
  matched, so a user can distinguish an under-provisioned cluster from a
  fragmented one.
- `topology exact distribution needs more domains` when there are fewer eligible
  domains than entries. This is the common misconfiguration of requesting more
  distinct domains than the topology contains, and waiting will not fix it.

One metric is added:

```text
kueue_tas_exact_distribution_failures_total
```

It counts failed matching attempts, labeled by `cluster_queue` and `reason`. That
pairing matches existing ClusterQueue-scoped metrics such as the eviction
counters and introduces no per-workload cardinality. The metric carries the
standard configurable ClusterQueue label suffix and a `+metricsdoc:labels`
marker, and is registered only when the feature gate is enabled.

### Feature Gate

Introduce the alpha feature gate `TASExactTopologyDistribution`, depending on
`TopologyAwareScheduling` and `TASMultiLayerTopology` in the feature gate
dependency table. The dependency on `TASMultiLayerTopology` is structural rather
than behavioral: `sizes` is carried on `PodsetSliceRequiredTopologyConstraints`,
which that gate owns.

A separate gate is used because `TASMultiLayerTopology` is already beta while the
`sizes` semantics and matching behavior are new. Disabling the gate preserves all
existing scalar multi-layer behavior and rejects new requests containing `sizes`.

### Upgrade, Downgrade, and Backwards Compatibility

Adding `sizes` preserves existing manifests and Go clients using `Size`, and
existing scalar requests follow the unchanged scheduling path. The schema change
relaxes the existing beta field from unconditionally required to one optional arm
of a required union; no previously valid object becomes invalid. Upgrading the
CRDs adds the optional field before the feature is enabled, and while the gate is
disabled new `sizes` requests are rejected.

Downgrade is a hard incompatibility, not an older-controller fallback. An older
CRD schema has `required: [size, topology]` for every constraint item, so it
rejects an object containing `sizes` without `size` at API-server validation,
before the older controller can read it. Before installing the older CRD,
operators must complete or delete stored Workloads containing `sizes` and remove
the annotation from Job templates. Downgrade documentation and release notes call
out this prerequisite.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make the code solid enough before committing the changes
necessary to implement this enhancement.

#### Prerequisite testing updates

None. The existing TAS scheduler and ungater tests already cover the scalar
paths this feature branches from, and those tests must keep passing unchanged.

#### Unit tests

New code gets full unit coverage as usual, so the validation and schema cases
are not listed here. The packages this touches, and their coverage at the time
of writing:

- `apis/kueue/v1beta2`: TBD
- `pkg/controller/jobframework`: TBD
- `pkg/cache/scheduler`: TBD
- `pkg/controller/tas`: TBD

A handful of cases are worth naming, because each one is a behavioral claim made
elsewhere in this KEP rather than routine coverage:

- `[1, 3, 4]` and `[4, 3, 1]` produce different rank blocks from the same
  aggregate domain counts.
- Merging a replacement assignment keeps exact groups in `sizes` order instead
  of re-sorting them by label value, while scalar assignments still re-sort.
- A pod with a missing, duplicate, or out-of-range rank leaves the whole PodSet
  gated, rather than falling back to greedy assignment.
- A `LeastFreeCapacity` placement that exactly fills its domains fails
  replacement, while the same placement under `BestFit` has room to absorb it.
- The elastic shrink exception is refused for a `sizes` PodSet and still allowed
  for a scalar one.
- Enough free capacity in total, but no matchable distribution, is reported as
  infeasible and does not trigger preemption.

#### Integration tests

Single-cluster TAS integration tests verify:

- An eight-pod indexed Job obtains rack-level counts `[1, 3, 4]`, with rank 0 in
  the first selected rack, ranks 1-3 in the second, and ranks 4-7 in the third.
- Reordering to `[4, 3, 1]` changes those rank ranges while keeping the same
  aggregate counts.
- A 24-pod Job obtains block-level counts `[8, 16]`.
- A workload stays pending when no matching is possible even though total free
  capacity would be enough, and reports the exact-distribution failure reason.
- A workload picks an enclosing domain that works when another candidate does
  not.
- Disabling the feature gate rejects the annotation.
- Replacing an unhealthy node keeps both the distribution and the original
  rank-to-domain mapping, including when the replaced leaf was the only one in
  its group.
- A workload admitted by preempting a lower-priority workload gets the full
  ordered distribution.

#### e2e tests

Add an extended TAS end-to-end test on a fixed test topology. It creates an
indexed Job, waits for admission, and checks the topology assignment, the rank
range given to each exact domain, and where the pods actually land. Existing
scalar TAS end-to-end tests run unchanged.

### Graduation Criteria

#### Alpha

- API field, feature gate, validation, matching, and assignment are implemented.
- Unit and integration tests cover feasible and infeasible distributions.
- User documentation describes semantics and limitations.
- Failed-node replacement does not silently violate the distribution.

#### Beta

- Positive operational feedback from users running exact distributions.
- Scheduling latency and memory impact measured for the maximum list length.
- Failure reasons are actionable and covered by tests, and
  `kueue_tas_exact_distribution_failures_total` is confirmed useful for
  diagnosing pending exact workloads.
- At least one release of alpha usage without unresolved correctness issues.
- The API limit and interactions with outer scalar constraints are revisited
  based on experience.
- Whether preemption should become exact-domain aware is decided from observed
  workloads that had enough preemptible aggregate capacity but no matchable
  distribution.
- Whether exact requests should always use `BestFit` instead of following the
  configured profile is decided from observed failed node replacements on
  tightly packed exact placements.

#### Stable

- At least two releases of beta usage without unresolved correctness issues.
- End-to-end tests are stable in periodic jobs.
- Upgrade and downgrade procedures are documented.
- Any expansion to elastic, repeating, or nested semantics is either completed
  under a separate KEP or explicitly retained as a non-goal.

## Implementation History

- 2026-08-24: Initial draft.

## Drawbacks

Exact placement trades cluster efficiency for predictability. It can reject
otherwise usable placements, leave capacity unused across domains, and keep a
workload pending even when aggregate capacity would suffice. That cost is
justified only when a predictable per-domain distribution materially affects
workload performance or behavior, so workloads that only need locality should
keep using required, preferred, or scalar slice topology, which give Kueue more
freedom to place efficiently.

The proposal also adds another placement path to a core scheduling component and
requires special handling in failure recovery. Restricting alpha to one fixed
distribution level limits that complexity.

## Alternatives

### Multiple JobSet ReplicatedJobs

A JobSet can model an uneven distribution as multiple `ReplicatedJob`s, since
Kueue creates one PodSet per `ReplicatedJob`. Three single-replica entries with
`parallelism` of one, three, and four, each annotated with
`kueue.x-k8s.io/podset-required-topology: topology.example.com/rack`, produce
three PodSets that each fit within some rack. No slice size is needed, because
each ReplicatedJob is already one PodSet confined to one domain.

This is sufficient when the application genuinely consists of separate
ReplicatedJobs and only needs each group rack-local. It does not provide the
semantics proposed here: the PodSets are placed independently, so their racks are
not required to be distinct or to descend from a common block. It is also
unavailable to workload types that cannot express the groups as separate PodSets.

Most importantly, splitting the PodSet discards the ordered rank blocks that
motivate this proposal. Each ReplicatedJob has its own index space starting at
zero, so there is no single rank ordering over the eight workers and no way to
require that ranks 1-3 are co-located while rank 0 is not. Applications deriving
collective communicator ranks from one contiguous index space cannot use this
alternative without changing how they assign ranks.

### A Separate Annotation

A new annotation such as `podset-domain-counts` could carry the distribution.
This separates the feature from multi-layer constraints but duplicates the
existing topology-and-size API. Extending the existing constraint entry keeps
topology grouping requests in one API family.

### Binding Counts to Named Domains

The API could map label values directly to counts (`rack-a: 1`, `rack-b: 3`,
`rack-c: 4`). This supports physical-domain reproduction but couples workloads to
a specific cluster and bypasses TAS domain selection. It is left for a separate
feature if users require explicit domain identity.

### Repeating a Distribution Pattern

Kueue could allow a 16-pod PodSet to specify `[1, 3, 4]` and repeat it twice.
This requires defining whether repeats share enclosing domains, whether entries
from different repeats may share exact-level domains, and how pattern identity
survives failure recovery. Alpha instead requires the complete list — a 16-pod
PodSet can explicitly request `[1, 3, 4, 1, 3, 4]`, which needs six distinct
domains and defines six rank blocks in that order.

### Multiple Exact Levels

A flat list cannot unambiguously describe different child distributions for
different parent sizes. Block sizes `[8, 16]` would require separate rack
distributions per block. Supporting this likely requires a nested tree-shaped API
rather than multiple independent `sizes` lists.

### Pod Topology Spread Constraints

Kubernetes topology spread constraints bound the skew between eligible domains.
They can encourage an even distribution but cannot require an arbitrary vector
such as `[1, 3, 4]` across dynamically selected domains.
