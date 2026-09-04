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

1. `sizes` is ordered. Entry `i` defines the next `sizes[i]` contiguous pod
   ranks: for `[1, 3, 4]` the groups are rank 0, ranks 1-3, and ranks 4-7, so
   `[4, 3, 1]` asks for the same domain counts but a different rank layout.
2. Each entry specifies the exact pod count for one distinct domain at the named
   topology level, so the list length is the number of domains used at that
   level. Kueue selects the physical domains; a list position does not name a
   domain or correspond to any ordering of the topology itself.
3. Duplicate values are allowed. `[2, 2, 4]` requests three distinct domains.
4. The sum of the list must equal the fixed PodSet count.
5. An exact distribution is a required constraint. Kueue does not fall back to
   scalar slicing or ordinary greedy placement when it cannot be satisfied.
6. At alpha, an entry using `sizes` must be the only entry in the constraints
   list. Containment is expressed using `podset-required-topology`, which must be
   strictly coarser than the exact level when `sizes` has more than one entry. A
   request naming the same level in both is contradictory: the identical-string
   case is rejected at creation time, and the general ordering check happens at
   scheduling time, once the topology hierarchy is known.
7. Below the exact level, Kueue uses its existing capacity-based placement to
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

The list is always the complete distribution; Kueue does not repeat a shorter
pattern to fill a larger PodSet. A 16-pod PodSet cannot write `[1, 3, 4]` and
have it applied twice — write `[1, 3, 4, 1, 3, 4]`, which asks for six distinct
domains. Note that is a different request from `[2, 6, 8]`, which asks for three
domains holding twice as much. Nesting is out of scope for the same reason: a
flat list cannot say which rack split belongs to which block, so block counts of
`[8, 16]` with a per-block rack distribution would need a tree-shaped API.

A single-entry list such as `sizes: [8]` is permitted and puts the whole PodSet
in one domain, the same shape `podset-required-topology` gives. The two are not
interchangeable: they fall into different request classes and so may pick
different domains (see [Exact Domain Matching](#exact-domain-matching)). Use
`podset-required-topology` for the single-domain case — it needs neither this
feature gate nor a stable rank source.

### Risks and Mitigations

**Scheduling cost:** free capacity alone cannot tell you whether a request fits,
because the shape matters — racks with room for 2, 2, and 4 pods have eight
slots free but cannot hold `[1, 3, 4]`. Kueue therefore runs the match separately
inside each candidate enclosing domain. Two limits keep that cheap. Allowing only
one exact level keeps it a flat problem — sort the capacities, place each entry,
done — instead of a search that tries layouts and backs out of dead ends. The
128-entry cap bounds a single match. Cost then grows in step with the number of
domains rather than exploding.

**Starvation:** an exact request can stay pending even when the cluster has
plenty of free pod slots, because those slots are in the wrong shape. This is
inherent to making the distribution a hard requirement; see
[Drawbacks](#drawbacks) for when that cost is worth paying, and
[Failure Reporting](#failure-reporting) for how it is surfaced.

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
- `sizes` is not combined with `podset-preferred-topology` at alpha.
  `podset-required-topology` is accepted, and `podset-unconstrained-topology` is
  accepted as a no-op.
- Existing mutual exclusions with the legacy slice annotations and
  `podset-group-name` continue to apply.
- The `TASExactTopologyDistribution` feature gate is enabled.

The Workload webhook repeats the structural, numeric, partial-admission, and
elastic-workload checks, **including the feature gate check**, so that a Workload
written directly is checked too. A Workload can be created without going through
job integration validation, so the gate has to be enforced on both paths or a
`sizes` request would be accepted while `TASExactTopologyDistribution` is
disabled, contradicting the guarantee in [Feature Gate](#feature-gate).

The webhook's existing per-constraint check also rejects any entry whose `size`
is not positive, which an exact constraint never sets, so that check has to
become union-aware. Until it does, the webhook refuses every `sizes` request and
the job integration cannot create a Workload at all.

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

One existing step runs before that choice is made and assumes a scalar size.
Slice-size resolution rejects a constraint whose size is not positive, and must
instead report a size of one for an exact constraint: an exact distribution does
not slice, since each entry is placed whole in its own domain and pods below that
level are assigned individually.

#### Scope Selection

The outer annotations are already mutually exclusive, so there are three cases:

- **`podset-required-topology`.** Kueue evaluates each domain at that level as a
  separate enclosing scope — for a required block and exact rack sizes, each
  candidate block is evaluated using only its descendant racks. This is the only
  way to require that the selected domains share a parent.
- **No outer annotation, or `podset-unconstrained-topology`.** The eligible
  topology of the selected ResourceFlavor is the scope. Both forms behave the
  same, because a request carrying only slice constraints is already classified
  as unconstrained; the annotation is accepted as a no-op rather than rejected.
  Note that this gives **no containment at all**: `sizes: [1, 3, 4]` picks three
  distinct racks, but nothing requires them to sit in the same block.
- **`podset-preferred-topology`.** Rejected at alpha. Preferred widens the scope
  on its own when a level does not fit, so a soft outer scope would shift
  underneath a hard inner distribution. It is also the only class in which
  balanced placement runs, and balanced placement spreads pods evenly, which
  `sizes` contradicts.

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

Step 3 orders candidate domains by the active placement mode:

- `BestFit` starts with the fitting domains that have the most free capacity.
- `LeastFreeCapacity` starts with the fitting domain that has the least.
- `BalancedPlacement` never runs at alpha, because it applies only to a request
  that is neither required nor unconstrained, and every `sizes` request alpha
  allows is one of those two.

Which mode applies follows the existing TAS profile rules, and exact matching
does not override them. A `sizes`-only request counts as unconstrained, so under
the default profile it gets `LeastFreeCapacity`; adding `podset-required-topology`
makes it required, which gets `BestFit`.

If preferred topology is ever allowed with `sizes`, then `sizes` must win:
balanced placement spreads pods evenly while `sizes` asks for an uneven split,
and both cannot be true at once.

This has one side effect worth calling out. `LeastFreeCapacity` picks the
domains that barely fit, so if a node later fails, replacement must stay in that
same domain and may have nowhere to go. `BestFit` would leave spare room but
waste more capacity. Alpha follows whichever profile the cluster is set to
rather than picking for the user, and beta decides based on how often
replacement actually fails.

```text
requested sizes: [1, 3, 4]
domain capacity: [1, 2, 3, 7, 10]

BestFit (with podset-required-topology):
4 -> capacity 10
3 -> capacity 7
1 -> capacity 3

LeastFreeCapacity (sizes only, default profile):
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

Matching has already decided which domain gets which entry. Kueue sets each
selected domain's pod count to its matched value, and the existing code that
walks down the tree spreads that count over the hosts inside it. Those hosts and
their counts are what gets written into `TopologyAssignment`.

The one new rule is about **order**. Hosts belonging to the same selected domain
must stay together in the list, and the groups must appear in the order the
entries were written in `sizes`:

```text
sizes: [1, 3, 4]  matched to  rack-z, rack-m, rack-a

written as:  z1:1  |  m1:2, m2:1  |  a1:4
ranks:       0     |  1, 2, 3     |  4, 5, 6, 7
```

Order matters because of what the ungater does with this list. It reads the
domains in the order they are stored and hands out pod ranks in blocks: the
first domain takes ranks from 0, the next carries on from there. So the order of
the list *is* the rank mapping, and nothing new has to be added to the API to
record it.

The catch is that today's code sorts hosts by name before writing them out. That
would produce `a1, m1, m2, z1`, putting rank 0 in the four-pod group and turning
`[1, 3, 4]` into `[4, 3, 1]`.


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

Keeping that position needs a change to how assignments are merged. Kueue does
not rebuild the whole assignment on replacement — it merges the small
replacement piece into the existing one, and that merge sorts everything by name
as it goes. For ordinary placement that is harmless. Here it is not:

```text
placed:        z1:1  |  m1:2, m2:1  |  a1:4      ranks 0 | 1,2,3 | 4,5,6,7
m2 dies, m3 replaces it

merged by name:  a1:4, m1:2, m3:1, z1:1          ranks 0,1,2,3 | 4,5 | 6 | 7
```

Rank 0 has moved into the four-pod group and `[1, 3, 4]` has become `[4, 3, 1]`,
purely because a node was replaced. Every count is still correct and nothing
reports an error.

Merging therefore needs a second version, turned on by
`TASExactTopologyDistribution`, that puts the replacement host back into its own
group and leaves the group order alone — giving `z1:1 | m1:2, m3:1 | a1:4`.
Scalar workloads keep the existing merge unchanged.

The same merge is also used for workloads with a leader pod and worker pods, so
the bug could in principle arrive that way too. It cannot today, because `sizes`
may not be combined with `podset-group-name`, which those workloads use. That
rule is now load-bearing rather than incidental: if it is ever relaxed without
revisiting merging, the reordering returns through a different door. A test
asserts the exclusion so that it fails loudly instead.

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
- A Workload created directly, without going through job integration validation,
  is rejected when it carries `sizes` and the feature gate is disabled, and is
  subject to the same structural and sum checks when the gate is enabled.
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

- 2026-09-01: Initial draft.
- 2026-09-02: Prototyped on a four-rack kind cluster. Reordering `[1, 3, 4]` to
  `[4, 3, 1]` kept the same domains and counts but swapped the rank blocks, and
  infeasible requests stayed pending.

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

### Pod Topology Spread Constraints

The Kubernetes scheduler can already spread pods with topology spread
constraints, or pin them to named domains with node affinity. Neither does what
this KEP needs. Spread constraints bound the *skew* between domains, so they can
push towards an even split but cannot require an arbitrary shape such as
`[1, 3, 4]`. Node affinity can name domains but not have the scheduler choose
them. Both also decide pod by pod, whereas an exact distribution has to be
reserved for the whole PodSet at once — which is what Kueue's group-level
admission already does.
