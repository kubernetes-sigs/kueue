# KEP-13416: Uneven Slice Sizes for Topology Aware Scheduling

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Co-location Groups of Different Sizes](#story-1-co-location-groups-of-different-sizes)
    - [Story 2: Shaping Each Block the Same Way](#story-2-shaping-each-block-the-same-way)
  - [Semantics](#semantics)
  - [Notes, Constraints, and Caveats](#notes-constraints-and-caveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API Changes](#api-changes)
  - [Validation](#validation)
  - [Scheduling](#scheduling)
    - [Chunk Placement](#chunk-placement)
    - [Preemption](#preemption)
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
  - [Distinct Domains and Rank Ordering in Alpha](#distinct-domains-and-rank-ordering-in-alpha)
  - [Multiple JobSet ReplicatedJobs](#multiple-jobset-replicatedjobs)
  - [A Separate Annotation](#a-separate-annotation)
  - [Binding Counts to Named Domains](#binding-counts-to-named-domains)
  - [Pod Topology Spread Constraints](#pod-topology-spread-constraints)
<!-- /toc -->

## Summary

Topology Aware Scheduling can already cut a PodSet into equal chunks and keep
each chunk inside one topology domain, using `size` on a slice constraint. The
chunks must all be the same size.

This KEP adds `sizes`, an alternative to `size` on the same constraint layer,
that lists chunk sizes explicitly. `sizes: [1, 3, 4]` cuts eight pods into
chunks of one, three and four. Each chunk still has to fit inside one domain,
and chunks may still share a domain — the contract is the same as `size`, only
the chunk sizes differ.

## Motivation

A workload often has groups of pods that must stay close together, and those
groups are not always the same size. A training job might run four
tensor-parallel workers that have to share a rack, three pipeline stages that
have to share a rack, and one coordinator that can go anywhere.

`size` cannot express that. It cuts the PodSet into equal chunks, so the only
way to keep a group of four together is to set `size: 4`, which also forces the
other groups into fours. Users are left picking a chunk size that is wrong for
most of their groups, or splitting the work into separate PodSets and losing the
single pod index space their application depends on.

`sizes` is the smallest change that covers this: the same per-chunk co-location
guarantee `size` already provides, with the chunk sizes written out.

### Goals

- Let a PodSet request co-location groups of different sizes at a configured
  topology level.
- Keep the contract identical to `size`: no chunk is split across domains, and
  chunks may share a domain.
- Compose with the existing constraint layers, so an outer layer can shape the
  region that an inner `sizes` list then subdivides.
- Preserve existing scalar `size` behavior unchanged, and keep `sizes` opt-in.

### Non-Goals

- Guaranteeing that each chunk lands in its own domain. That is a separate
  field, discussed under
  [Alternatives](#distinct-domains-and-rank-ordering-in-alpha), and it should
  apply to `size` as well.
- Guaranteeing which pod ranks end up in which chunk.
- Selecting topology domains by explicit label value, such as `rack-a`.
- Expressing node counts. The values are pod counts.
- More than one `sizes` layer in a single constraints list.
- Partial admission or elastic changes to the PodSet count.

## Proposal

Add an optional `sizes` field to a slice constraint layer. Exactly one of `size`
and `sizes` must be set.

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: uneven-groups
  labels:
    kueue.x-k8s.io/queue-name: user-queue
spec:
  parallelism: 16
  completions: 16
  completionMode: Indexed
  template:
    metadata:
      annotations:
        kueue.x-k8s.io/podset-slice-required-topology-constraints: |
          [
            {"topology": "topology.example.com/block", "size": 8},
            {"topology": "topology.example.com/rack", "sizes": [1, 3, 4]}
          ]
    spec:
      containers:
      - name: worker
        image: registry.k8s.io/e2e-test-images/agnhost:2.53
        args: ["pause"]
      restartPolicy: Never
```

The outer layer cuts sixteen pods into two chunks of eight, each inside one
block. The inner layer cuts each block's eight pods into chunks of one, three
and four, each inside one rack. A chunk never straddles a rack, but two chunks
may land in the same rack.

### User Stories

#### Story 1: Co-location Groups of Different Sizes

As a machine learning engineer, my job has four tensor-parallel workers that
must share a rack, three pipeline stages that must share a rack, and one
coordinator with no locality requirement. I want to say
`sizes: [4, 3, 1]` and have Kueue keep each group whole, without forcing all
three groups to the same size or splitting my job into separate PodSets.

#### Story 2: Shaping Each Block the Same Way

As a cluster user with a 16-pod job, I want two blocks of eight, and inside each
block I want the eight pods cut into groups of one, three and four. I want to
write that once rather than enumerating every group across both blocks.

### Semantics

1. `sizes` lists the chunk sizes at its layer. `[1, 3, 4]` means three chunks,
   of one, three and four pods.
2. Each chunk must fit inside one domain at that layer's topology level. Chunks
   may share a domain. This is the same contract `size` has today.
3. The sum of `sizes` must equal the chunk size of the layer above, or the
   PodSet count when `sizes` is the first layer.
4. Order in the list carries no meaning. `[1, 3, 4]` and `[4, 3, 1]` are the
   same request.
5. Duplicate values are allowed, and describe separate chunks of the same size.
   `[2, 2, 4]` is three chunks, not two.
6. A constraints list may contain at most one layer using `sizes`. Layers above
   and below it use `size` as they do today.
7. Below the `sizes` layer, Kueue uses its existing capacity-based placement.

`sizes: [8]` for an eight-pod PodSet is a single chunk in one domain, which is
what `podset-required-topology` already gives. It is accepted rather than
special-cased, but required topology remains the clearer way to say it.

### Notes, Constraints, and Caveats

The values are pod counts, not node counts. A chunk of four occupies four nodes
only when resource requests or other constraints force one pod per node.

`sizes` does not pin the per-domain pod count. With chunks of one, three and
four and roomy racks, the pods may end up as `1/3/4` across three racks, as
`4/4` with two chunks sharing, or as `8` in one rack. All of those honour the
request, because the request is about keeping chunks whole, not about how many
domains get used. Pinning the per-domain count needs the distinctness field
described under [Alternatives](#distinct-domains-and-rank-ordering-in-alpha).

The constraint attaches to one PodSet. A workload with several PodSets validates
and schedules each one independently.

### Risks and Mitigations

**Packing quality:** chunks of mixed sizes are placed greedily, in the same way
`size` places equal chunks today. Greedy placement can fail to find a packing
that exists — three chunks of three into racks holding four and five pods fits,
but a greedy pass that puts two chunks in the five-rack does not find it. This
is existing behavior rather than something `sizes` introduces, and the failure
is a pending workload, not a wrong placement.

**Scheduling cost:** placing chunks costs the same as placing the equivalent
number of equal chunks. The list is capped at 128 entries, so a single layer
cannot expand the work without bound.

**Misreading the guarantee:** a user may expect `[1, 3, 4]` to mean three
domains. Documentation states plainly that chunks may share a domain and points
at the distinctness field for the stronger guarantee.

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

    // size indicates the number of pods in each equal chunk at this topology
    // level.
    //
    // +optional
    // +kubebuilder:validation:Minimum=1
    Size int32 `json:"size,omitempty"`

    // sizes lists the pod count of each chunk at this topology level, for
    // chunks that are not all the same size. Each chunk is placed within one
    // domain; chunks may share a domain. The sum must equal the chunk size of
    // the layer above, or the PodSet count for the first layer.
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
and `TopologyAssignment` needs no change. `v1beta1` has no multi-layer
constraint field, so `sizes` inherits the existing behavior of dropping those
constraints during conversion.

### Validation

Validation is split between job integration validation, the Workload webhook,
and scheduling-time validation once the selected `Topology` hierarchy is known.

Creation-time validation enforces:

- The structural schema above: exactly one of `size` and `sizes`, 1 to 128
  entries, every value greater than zero.
- At most one layer in the constraints list uses `sizes`.
- The sum, computed using `int64`, equals the `size` of the layer above, or
  `PodSet.Count` when the `sizes` layer is first.
- The PodSet does not use partial admission (`MinCount` is unset).
- The parent workload has not opted into elastic workload slicing with
  `kueue.x-k8s.io/elastic-job: "true"`, even when
  `ElasticJobsViaWorkloadSlicesWithTAS` is enabled.
- Existing rules for the constraints list continue to apply unchanged: at most
  three layers, strictly finer topology levels going down the list, and the
  mutual exclusions with the legacy slice annotations and `podset-group-name`.
- The `TASExactTopologyDistribution` feature gate is enabled.

The Workload webhook repeats the structural, numeric, sum, partial-admission and
elastic-workload checks, **including the feature gate check**. A Workload can be
created without going through job integration validation, so the gate has to be
enforced on both paths or a `sizes` request would be accepted while the gate is
disabled, contradicting the guarantee in [Feature Gate](#feature-gate).

The webhook's existing per-constraint check also rejects any entry whose `size`
is not positive, which a `sizes` layer never sets, so that check has to become
union-aware. Until it does, the webhook refuses every `sizes` request and the
job integration cannot create a Workload at all.

Update-time validation preserves the fixed-count invariant. Existing Workload
validation already makes the whole PodSet immutable once quota is reserved, so
`sizes` and the count inherit that. Two additions:

- Before quota reservation, `sizes` or the count may change only when the sums
  still line up.
- That immutability check has an exception letting elastic jobs shrink the
  count. The exception is keyed on the `ElasticJobsViaWorkloadSlices` gate, not
  on whether this particular workload is elastic, so it must be refused for
  `sizes` PodSets. Otherwise the sum stops matching.

Scheduling-time validation enforces that the topology key of each layer exists
in the selected ResourceFlavor's `Topology`, which is the existing check.

### Scheduling

A `sizes` layer behaves like a `size` layer with a per-chunk size that varies.
The level walk, the capacity roll-up and the choice of placement algorithm are
all unchanged. The only new behavior is at the layer itself, where chunks of
different sizes are placed instead of chunks of one size.

Two existing steps assume a scalar size and need to handle a `sizes` layer.
Slice-size resolution rejects a constraint whose size is not positive, and the
capacity roll-up divides a domain's pod count by the slice size to get a chunk
count. Neither has a single number to work with when chunk sizes differ; both
need a `sizes`-aware branch.

#### Chunk Placement

At the `sizes` layer, Kueue has a list of chunk sizes and the available pod
capacity of each candidate domain, already ordered by the active TAS placement
mode. It walks the chunks from largest to smallest and puts each one in the
first domain that still has room for it, reducing that domain's remaining
capacity as it goes.

Largest-first matters: placing a chunk of one before a chunk of four can leave
the four with nowhere to go even though a valid packing existed. It does not
make the placement optimal — this is bin packing, and greedy packing can miss a
valid arrangement — but it matches how `size` already behaves and keeps the cost
proportional to chunk count times domain count.

Because chunks may share a domain, the domain that ends up holding two chunks
records a single combined count in the `TopologyAssignment`, exactly as it would
with two equal chunks today. No new status is written.

#### Preemption

TAS recomputes a preempting workload's placement against a snapshot with the
candidate targets' usage removed, before preemptions are issued. Chunk placement
runs inside that recomputation, so a target set that does not leave room for
every chunk is rejected without evicting anything.

One existing gap is worth fixing alongside this. When the recomputation finds no
placement, the result is stored but the assignment mode is left at `Preempt`, so
the preemption goes ahead anyway. For equal chunks this rarely matters, because
freeing more capacity almost always means more chunks fit. It matters more here:
freeing room for eight pods spread thinly still cannot hold a chunk of four. A
missing assignment after that recomputation should become `NoFit` and requeue.

### Failed Node Replacement

Existing replacement decides whether a chunk is incomplete using modulo
arithmetic against the slice size, which has no meaning when chunk sizes differ.

The guarantee to preserve is the same one placement makes: no chunk is split
across domains. Replacement pods therefore go back into the domain that lost
them, which is what the existing per-domain replacement path already does once
it stops relying on modulo to decide where they belong.

If replacement cannot fit in the affected domain, the configured failure
behavior applies, including fail-fast or eviction. Moving a chunk to a different
domain is out of scope.

### Failure Reporting

A `sizes` request can stay pending while free capacity looks sufficient in
total, because the capacity is spread too thinly for a large chunk. Reported as
a generic topology fit failure that is indistinguishable from ordinary
exhaustion, so chunk placement adds one reason to the existing TAS
failure-reason plumbing:

- `topology slice sizes do not fit` when at least one chunk cannot be placed.
  The message names the chunk size that failed and the largest free capacity
  available at that level, so a user can tell an under-provisioned cluster from
  a fragmented one.

One metric is added:

```text
kueue_tas_slice_placement_failures_total
```

It counts failed chunk-placement attempts, labeled by `cluster_queue` and
`reason`. That pairing matches existing ClusterQueue-scoped metrics such as the
eviction counters and introduces no per-workload cardinality. The metric carries
the standard configurable ClusterQueue label suffix and a `+metricsdoc:labels`
marker, and is registered only when the feature gate is enabled.

### Feature Gate

Introduce the alpha feature gate `TASExactTopologyDistribution`, depending on
`TopologyAwareScheduling` and `TASMultiLayerTopology` in the feature gate
dependency table. The dependency on `TASMultiLayerTopology` is structural
rather than behavioral: `sizes` is carried on
`PodsetSliceRequiredTopologyConstraints`, which that gate owns.

A separate gate is used because `TASMultiLayerTopology` is already beta while
`sizes` is new. Disabling the gate preserves all existing scalar behavior and
rejects new requests containing `sizes`.

### Upgrade, Downgrade, and Backwards Compatibility

Adding `sizes` preserves existing manifests and Go clients using `Size`, and
existing scalar requests follow the unchanged path. The schema change relaxes
the existing beta field from unconditionally required to one optional arm of a
required union; no previously valid object becomes invalid. Upgrading the CRDs
adds the optional field before the feature is enabled, and while the gate is
disabled new `sizes` requests are rejected.

Downgrade is a hard incompatibility, not an older-controller fallback. An older
CRD schema has `required: [size, topology]` for every constraint item, so it
rejects an object containing `sizes` without `size` at API-server validation,
before the older controller can read it. Before installing the older CRD,
operators must complete or delete stored Workloads containing `sizes` and remove
the annotation from Job templates. Downgrade documentation and release notes
call out this prerequisite.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make the code solid enough before committing the changes
necessary to implement this enhancement.

#### Prerequisite testing updates

None. The existing TAS scheduler tests already cover the scalar paths this
feature branches from, and those tests must keep passing unchanged.

#### Unit tests

New code gets full unit coverage as usual, so the validation and schema cases
are not listed here. The packages this touches, and their coverage at the time
of writing:

- `apis/kueue/v1beta2`: TBD
- `pkg/controller/jobframework`: TBD
- `pkg/cache/scheduler`: TBD

A few cases are worth naming, because each is a behavioral claim made elsewhere
in this KEP rather than routine coverage:

- `sizes: [2, 2, 2, 2]` places identically to `size: 2` for the same PodSet.
- Chunks are placed largest-first, so `[1, 4]` into domains holding four and one
  pods succeeds rather than stranding the four.
- Two chunks sharing a domain produce one combined count in the assignment.
- The sum is checked against the layer above, not only against the PodSet count.
- A Workload created directly, without job integration validation, is rejected
  when it carries `sizes` and the feature gate is disabled, and gets the same
  structural and sum checks when the gate is enabled.
- The elastic shrink exception is refused for a `sizes` PodSet and still allowed
  for a scalar one.
- Capacity sufficient in total but no room for the largest chunk is reported as
  a placement failure and does not trigger preemption.

#### Integration tests

Single-cluster TAS integration tests verify:

- A 16-pod indexed Job with `[{block, size: 8}, {rack, sizes: [1,3,4]}]` places
  eight pods in each of two blocks, with every chunk inside one rack.
- An eight-pod Job with `sizes: [1, 3, 4]` keeps each chunk whole when racks are
  tight enough to force three racks, and still admits when one roomy rack takes
  two chunks.
- A workload stays pending when no domain can hold the largest chunk, and
  reports the slice-size failure reason.
- Disabling the feature gate rejects the annotation.
- Replacing an unhealthy node returns the pods to the same domain.

#### e2e tests

Add an extended TAS end-to-end test on a fixed test topology. It creates an
indexed Job with uneven chunk sizes, waits for admission, and checks that no
chunk is split across racks. Existing scalar TAS end-to-end tests run unchanged.

### Graduation Criteria

#### Alpha

- API field, feature gate, validation and chunk placement are implemented.
- Unit and integration tests cover placeable and unplaceable chunk lists.
- User documentation describes the guarantee, and states plainly that chunks may
  share a domain.

#### Beta

- Positive operational feedback from users running uneven chunk sizes.
- Scheduling latency measured for the maximum list length.
- Failure reasons are actionable and covered by tests, and
  `kueue_tas_slice_placement_failures_total` is confirmed useful for diagnosing
  pending workloads.
- At least one release of alpha usage without unresolved correctness issues.
- Whether a distinctness field is needed, and what it should be called, is
  decided from user feedback.

#### Stable

- At least two releases of beta usage without unresolved correctness issues.
- End-to-end tests are stable in periodic jobs.
- Upgrade and downgrade procedures are documented.

## Implementation History

- 2026-09-01: Initial draft, proposing distinct domains and ordered pod-rank
  blocks.
- 2026-09-02: Prototyped that proposal on a four-rack kind cluster. The matcher
  was straightforward; preserving order through assignment construction, merging
  and ungating was where the cost and the silent-failure risk sat.
- 2026-09-23: Rescoped on review feedback. `sizes` becomes a plain generalization
  of `size` that composes with the other layers, and distinctness and rank
  ordering move out to a separate field.

## Drawbacks

`sizes` adds a second way to describe chunking at a layer, so readers of a
constraints list have to check which arm is in use. The alternative — a repeated
`size` layer, or a count-and-size pair — was not obviously clearer.

A user who wants per-domain counts pinned will find that `sizes` alone does not
do it, and has to wait for the distinctness field. The naming risk is real:
"sizes" suggests a distribution across domains more strongly than it suggests
chunk sizes.

## Alternatives

### Distinct Domains and Rank Ordering in Alpha

The first draft of this KEP gave `sizes` two further guarantees: each entry in
its own domain, and list order binding entries to contiguous pod-rank blocks, so
`[1, 3, 4]` put rank 0 alone and ranks 1-3 together. That pins the per-domain
pod count, which is what a workload sensitive to collective communication cost
actually wants.

It was rescoped for two reasons.

The first is consistency. `size` and `sizes` are an exactly-one-of union in the
same struct, and a union should differ in shape, not in contract. Having `size`
mean "chunks may share a domain" while `sizes` meant "one domain per entry, in
rank order" put two different contracts behind one choice. Distinctness is
better expressed as its own field, which then also works for `size` — today a
user writing `size: 2` has no way to ask for each chunk in its own rack.

The second is cost, and a prototype supports it. Distinct domains by themselves
are cheap, because a matcher that assigns each chunk an unused domain is barely
more work than greedy packing. Rank ordering is not. It requires assignment
construction to stop sorting domains by label value, assignment merging to stop
re-sorting during node replacement, and the topology ungater to stop falling
back to greedy pod assignment — three separate changes, each of which fails
silently when it is wrong, because the per-domain counts still come out correct.

One consequence is worth recording for whoever picks up the follow-up. Once two
chunks may share a domain, the assignment stores their combined count and the
chunk boundary is gone, not merely reordered. A later ordering feature therefore
cannot recover chunk identity from the stored assignment, and will need either
the distinctness guarantee first or an explicit chunk index in the status.

### Multiple JobSet ReplicatedJobs

A JobSet can model uneven groups as multiple `ReplicatedJob`s, since Kueue
creates one PodSet per `ReplicatedJob`. Three single-replica entries with
`parallelism` of one, three and four, each annotated with
`podset-required-topology: rack`, produce three PodSets that each fit within
some rack.

That is enough when the application really is separate ReplicatedJobs. It
changes the application model otherwise: each ReplicatedJob has its own pod
index space starting at zero, so an application that derives ranks from one
contiguous index space cannot use it without changing how it assigns ranks. It
is also unavailable to workload types that cannot express the groups as separate
PodSets.

### A Separate Annotation

A new annotation such as `podset-chunk-sizes` could carry the list. This keeps
it away from the multi-layer constraints, but it duplicates the existing
topology-and-size API and would need its own containment rules. Extending the
existing constraint layer keeps one API family and composes with the layers
already there.

### Binding Counts to Named Domains

The API could map label values directly to counts (`rack-a: 1`, `rack-b: 3`).
This supports reproducing a physical layout but couples workloads to a specific
cluster and bypasses TAS domain selection. It is left for a separate feature if
users need explicit domain identity.

### Pod Topology Spread Constraints

The Kubernetes scheduler can spread pods with topology spread constraints, or
pin them to named domains with node affinity. Spread constraints bound the skew
between domains, so they push towards an even split and cannot keep a specific
group of four together. Node affinity can name domains but not have the
scheduler choose them. Both also decide pod by pod, whereas chunk co-location
has to be reserved for the whole PodSet at once, which is what Kueue's
group-level admission already does.
