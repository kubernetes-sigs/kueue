# KEP-13746 topology spreading: the four user stories

Each folder is a **self-contained** example of one
[KEP-13746](../../../../../keps/13746-tas-topology-spreading/README.md) user
story: its own kind cluster, Topology, ResourceFlavor, ClusterQueue, LocalQueue,
namespace, Kueue config fragment, and workload. Nothing is shared between them
and nothing is inherited from the parent directory, so any one folder can be
applied on a fresh cluster on its own.

| Folder | Story | Workload | Rule |
|---|---|---|---|
| [small-model-deployment](small-model-deployment/) | 1 — small model, many replicas | Deployment, 1 Pod per Workload | zone, `0.45`, Required |
| [large-model-lws](large-model-lws/) | 2 — large model, many replicas | LeaderWorkerSet, leader+worker per replica | rack, `0.45`, Required |
| [large-model-podgroup](large-model-podgroup/) | 3 — large model via PodGroups | plain Pod groups | rack, `0.34`, Required |
| [soft-spreading-preferred](soft-spreading-preferred/) | 4 — soft spreading | Deployment, 1 Pod per Workload | zone, `0.45`, **Preferred** |

Every folder holds the same five files:

* `kind-cluster.yaml` — topology-labelled nodes for that story's levels
* `sample-queues.yaml` — Namespace, Topology, ResourceFlavor, ClusterQueue, LocalQueue
* `kueue-config-patch.yaml` — `featureGates` + `integrations.labelKeysToCopy`
* the workload manifest
* `README.md` — the fix applied vs. the KEP, how to run it, what to expect

## The stories do not run as the KEP writes them

Each README states its own deviation; the pattern across all four:

* **`workloadLabelSelectors` is mandatory.** The KEP has a Workload mutating
  webhook inject a `kueue.x-k8s.io/job-uid` default; the implementation has no
  such injection and rejects a missing selector. Stories 2 and 4 omit it.
* **`podset-required-topology` is mandatory.** Spreading counts a PodSet group as
  occupying one domain per rule level, which only holds for required placement.
  Stories 1 and 4 omit it.
* **`podset-group-name` must be shared across replicas, not per-replica.** Story 3
  makes it per-replica, which gives each replica its own spreading key and
  switches spreading off with no error, no condition and no log. Story 2 gets this
  right, and needs it, to fuse leader and worker into one unit.

## Verified behaviour (kind)

Every story was run on kind with Kueue built from this working tree, each on its
own cluster, exactly as written here — single-level Topologies, nothing added.

| Story | Result |
|---|---|
| 1 — Deployment, zone `0.45` Required | **PASS** — 6 replicas `3/2/1` over the three zones |
| 2 — LWS, rack `0.45` Required | **PASS** — 6 groups `1/2/3` over the three racks; leader+worker always co-located |
| 3 — Pod groups, rack `0.34` Required | **PASS** — 3 groups, one rack each (`2/2/2` pods) |
| 4 — Deployment, zone `0.45` **Preferred** | **PASS** — 6 replicas `3/3` over the two zones, all admitted, no condition set |

The shipped [../sample-lws-topology-spreading.yaml](../sample-lws-topology-spreading.yaml)
was run unmodified on its own zone-only Topology too: 6 groups, **2 per zone**,
4 pods per zone.

Story 1's `3/2/1` is the algorithm's exact output, not capacity round-robin
(which would give `2/2/2`): a rule caps a domain's share of what is already
placed, it does not balance. Story 2's `1/2/3` is the same distribution - which
of the tied racks wins is decided by capacity, and either assignment satisfies
the rule. Reproducing the KEP's Story 3 verbatim (per-replica `podset-group-name`)
was also confirmed to collapse all three groups into one rack, admitted with no
error, no condition and no log — the silent no-op described in
[large-model-podgroup](large-model-podgroup/).

### Two defects these stories surfaced, both fixed

#### Spreading ignored every Workload on a hostname-less Topology

Before the fix, all four stories and the shipped sample spread nothing at all.
With `TASNodeFeasibilityForAllLevels` (Beta, default on since 0.20) and a
Topology whose lowest level is not `kubernetes.io/hostname`, Kueue appends a
*virtual* hostname leaf to the tree, while `buildAssignment` publishes the
assignment rolled up to the declared levels. Spreading's own
`fullTopologyValues` accepted only a full-depth or hostname-only assignment, so
the rolled-up form resolved to nothing, every Workload was skipped, the total
came out `0` — which the rule reads as "cold start, any domain is fine" — and
everything piled into the first domain. Admitted, no condition, no event, no log.

Fix: `occupiedDomainsForGroup` now resolves the assignment with the pre-existing
`domainForAssignmentValues`, which asks the topology tree's own index instead of
rebuilding a level-values path, and walks up to the rule's level with
`ancestorAtLevel`. `fullTopologyValues` is deleted. That also corrects a second
bug in the same code: a rule naming the hostname level keyed its count by the
full path while a leaf's domain ID is the hostname alone, so it never matched.

Two regression tests cover both shapes
(`pkg/cache/scheduler/tas_spread_tree_count_test.go`); both fail on the old code.
No test caught this before, because every test Topology declares hostname —
`test/e2e/tas/extended/leaderworkerset_test.go:67` uses
`MakeDefaultThreeLevelTopology` (block, rack, `kubernetes.io/hostname`).

#### `Preferred` never spread

Independent of the above, and still present once counting worked: all 6 replicas
of Story 4 landed in one zone. A `Required` rule is enforced by elimination -
`filterBannedDomains` drops the forbidden domains before best-fit runs, so
whichever domain best-fit then picks is legal. A `Preferred` rule is enforced
only by the order `sortedBySpreadPriority` establishes, and
`findBestFitDomainForSlices` re-picked across the whole sorted list by tightest
sufficient capacity - the *most* loaded domain, the exact inverse of the
spreading order.

Fix: `findLevelWithFitDomains` narrows the candidates to the leading run of
domains the rules rank equally best before optimizing capacity, mirroring the
existing `topAffinityTierDomains`, so best-fit minimizes capacity within a
spreading tier instead of across tiers. This also makes the KEP's "among
over-threshold domains, the least-loaded is chosen first" reachable, since the
comparator already ranks that tier by occupancy.

Covered by two unit cases in `pkg/cache/scheduler/tas_cache_test.go` over a
fixture whose domains differ in free capacity - the earlier cases all use
identically sized nodes, which makes best-fit's comparison a tie and cannot
tell an honoured ordering from a discarded one. See
[soft-spreading-preferred](soft-spreading-preferred/) for the measured
before/after.

## Prerequisites common to all four

* Kueue installed, and `TopologyAwareScheduling` + `TASTopologySpreading` enabled
  via each folder's `kueue-config-patch.yaml`. The spreading gate is alpha and
  **off by default**; while it is off the annotation is ignored entirely and is
  not even validated.
* Story 2 additionally needs the
  [LeaderWorkerSet controller](https://github.com/kubernetes-sigs/lws) and the
  `lws` integration enabled.
