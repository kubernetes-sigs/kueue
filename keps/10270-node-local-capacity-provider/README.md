# KEP-10270: Node-based Local Capacity Provider

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Quota tracks an autoscaling node pool](#story-1-quota-tracks-an-autoscaling-node-pool)
    - [Story 2: Rolling upgrade that moves nodes between clusters](#story-2-rolling-upgrade-that-moves-nodes-between-clusters)
  - [Notes](#notes)
    - [Relation to KEP-12382 (DQO)](#relation-to-kep-12382-dqo)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Overview](#overview)
  - [The `local-capacity` provider controller](#the-local-capacity-provider-controller)
  - [Node discovery and watch](#node-discovery-and-watch)
  - [Flavor bucketing via `ResourceFlavor.nodeLabels`](#flavor-bucketing-via-resourceflavornodelabels)
  - [Node label source of truth and prerequisites](#node-label-source-of-truth-and-prerequisites)
  - [Node eligibility filter](#node-eligibility-filter)
  - [Allocatable summation](#allocatable-summation)
  - [Writing `CapacityProvider.status`](#writing-capacityproviderstatus)
  - [Condition semantics](#condition-semantics)
  - [Capacity ownership and overlap](#capacity-ownership-and-overlap)
  - [Enablement and feature gate](#enablement-and-feature-gate)
  - [RBAC](#rbac)
  - [End-to-end example](#end-to-end-example)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration Tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [Stable](#stable)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Provider-side headroom field](#provider-side-headroom-field)
  - [A dedicated node-selector on the provider](#a-dedicated-node-selector-on-the-provider)
  - [A `nodeQuotaPolicy` field directly on ClusterQueue](#a-nodequotapolicy-field-directly-on-clusterqueue)
  - [Reuse the `DynamicQuotaOrchestration` feature gate](#reuse-the-dynamicquotaorchestration-feature-gate)
<!-- /toc -->

## Summary

This KEP introduces a built-in **Node-based Local Capacity Provider**: a controller
that discovers cluster node capacity and publishes it through the
[`CapacityProvider`](../12382-dynamic-quota-orchestration/README.md) API introduced
by Dynamic Quota Orchestration (DQO, KEP-12382).

The controller watches Nodes, buckets each eligible node into the `ResourceFlavor`
it belongs to (using the flavor's existing `nodeLabels`), sums the nodes'
allocatable resources per `(ResourceFlavor, resource)` pair, and writes the result
to `CapacityProvider.status.capacity` together with the `CapacitySynchronized`
condition. A `DynamicQuotaOrchestrator` then aggregates and distributes that
capacity into `ClusterQueue`/`Cohort` `status.effectiveQuotas`, which the scheduler
already consumes.

The provider reports **raw** node capacity. Operational headroom (reserving a
buffer for system daemons, fragmentation, or node failures) is applied downstream
via the DQO `effectiveCapacityMultiplier`, not by this controller.

## Motivation

Kueue's admission quota (`ClusterQueue.spec.resourceGroups[].flavors[].resources[].nominalQuota`)
is a static value that an administrator hand-maintains, while the cluster's real
capacity changes continuously through autoscaling, node failures, and rolling
upgrades. This forces a manual, error-prone reconciliation loop:

- quota set **above** real capacity makes Kueue admit workloads that cannot be
  scheduled — pods stay `Pending`, and gang-scheduled workloads can deadlock;
- quota set **below** real capacity leaves critical resources idle behind a
  needlessly full queue;
- every scale event demands a manual edit that is easy to forget, races GitOps
  reconciliation, and is especially painful during rolling upgrades when nodes
  migrate between clusters.

DQO (KEP-12382) provides the generic pipeline to derive `status.effectiveQuotas`
from a `CapacityProvider`, but it deliberately ships **no provider that reads node
capacity** — the concrete capacity plugins are left to individual KEPs
(DQO README, *Non-Goals*). This KEP delivers the node plugin, closing the loop so
Kueue quota tracks live node capacity with no manual edits.

### Goals

- Introduce a built-in `CapacityProvider` controller that derives capacity from
  Nodes and publishes it via the DQO `CapacityProvider` API.
- Bucket nodes into `ResourceFlavor`s using the flavor's existing `nodeLabels`, so
  there is a single source of truth for the node→flavor mapping.
- Keep the published capacity synchronized as nodes are added, removed, cordoned,
  or become (un)ready.
- Report raw node capacity, delegating headroom to the DQO
  `effectiveCapacityMultiplier`.

### Non-Goals

- Define a new quota field on `ClusterQueue` or `Cohort`. This KEP writes only to
  the existing `CapacityProvider.status`; distribution to queues is DQO's job.
- Discover capacity from Dynamic Resource Allocation (DRA) `ResourceSlice` objects.
  DRA discovery is a planned fast-follow (see [Beta](#beta)); alpha is Nodes-only.
- Apply operational headroom, resource transformations, or DRA resource mappings.
  Headroom is the DQO multiplier's responsibility; transformations/mappings remain
  at the scheduler boundary per the DQO KEP.
- Change how the scheduler consumes `status.effectiveQuotas` (owned by KEP-12382).

## Proposal

Add a controller, identified by the `CapacityProvider` controller name
`kueue.x-k8s.io/local-capacity`, that reconciles `CapacityProvider` objects it owns.
For each such object, it computes capacity for the flavors listed in
`spec.orchestratedFlavors` by summing the allocatable resources of the eligible
nodes that match each flavor's `nodeLabels`, and writes the result to
`status.capacity` with a `CapacitySynchronized` condition.

The administrator authors the `CapacityProvider` spec (immutable `controllerName`,
required `orchestratedFlavors`) and a `DynamicQuotaOrchestrator` that references the
provider. No new API types are introduced by this KEP.

### User Stories

#### Story 1: Quota tracks an autoscaling node pool

As a cluster administrator, I run a node pool backed by a critical resource with a
cluster autoscaler bounded by a max-nodes setting. I want my ClusterQueue's quota
for that resource to follow the pool's current size automatically, so that scaling
the pool up or down does not require me to edit the ClusterQueue. I create one
`CapacityProvider` (`controllerName: kueue.x-k8s.io/local-capacity`,
`orchestratedFlavors: [critical-flavor]`) and a `DynamicQuotaOrchestrator` that
distributes it to my Cohort. Quota now tracks the live pool size.

#### Story 2: Rolling upgrade that moves nodes between clusters

As an operator upgrading clusters by incrementally migrating nodes between two
clusters, I do not want to hand-edit quota several times as capacity shifts. With
the node-based provider, each cluster's Kueue quota shrinks and grows automatically
as nodes leave and join, without manual intervention.

### Notes

#### Relation to KEP-12382 (DQO)

This KEP is a capacity **provider**; DQO is the **consumer/orchestrator**. The
contract between them is entirely the `CapacityProvider` API:

- this controller writes `status.capacity` + the `CapacitySynchronized` condition;
- DQO reads them (read-only) when the provider's `CapacitySynchronized=True`,
  scales by `effectiveCapacityMultiplier`, aggregates across providers, and
  distributes to `status.effectiveQuotas`.

This controller does **not** read or write `DynamicQuotaOrchestrator` objects or
`status.effectiveQuotas`. Both the `DynamicQuotaOrchestration` and the new
`NodeCapacityProvider` feature gates must be enabled for the end-to-end flow to
have effect.

### Risks and Mitigations

- **Over-reporting from transient node readiness flaps.** A node briefly flapping
  `NotReady` could cause capacity churn. *Mitigation:* only eligible nodes
  (`!Unschedulable && NodeReady=True`) contribute, and reconciles are debounced /
  change-gated (see [Node discovery and watch](#node-discovery-and-watch)) so
  irrelevant kubelet heartbeats do not trigger recomputation.
- **Overcommit after a capacity drop.** When nodes disappear, effective quota
  shrinks but already-admitted workloads are not evicted by Kueue. This is the same
  intrinsic risk DQO documents for any capacity reduction; it is not made worse
  here. *Mitigation:* observability — the provider's `status.capacity` and the
  quota metrics reflect the reduced values so operators can detect overcommit.
- **Overlapping flavors double-count a node.** A node whose labels match more than
  one flavor's `nodeLabels` contributes to each matched flavor. *Mitigation:*
  flavors are expected to have disjoint `nodeLabels`, and DQO only distributes
  flavors present in `spec.resourceGroups`; see
  [Capacity ownership and overlap](#capacity-ownership-and-overlap).
- **Overcommit when one node pool feeds independent quota subtrees.** Two DQOs with
  disjoint subtree roots that both reference the same provider each receive its full
  reported capacity, over-committing the physical nodes — DQO has no automatic
  cross-DQO capacity accounting in alpha. *Mitigation:* partition the pool with a
  per-DQO `effectiveCapacityMultiplier` (DQO KEP-12382 Story 3); see
  [Capacity ownership and overlap](#capacity-ownership-and-overlap).

## Design Details

### Overview

```
Nodes ──watch──> local-capacity CapacityProvider controller
                   │  bucket by ResourceFlavor.nodeLabels
                   │  sum Status.Allocatable of eligible nodes
                   ▼
        CapacityProvider.status.capacity  +  CapacitySynchronized=True
                   │  (read-only)
                   ▼
        DynamicQuotaOrchestrator  ── × effectiveCapacityMultiplier ──> distribute
                   ▼
        ClusterQueue / Cohort .status.effectiveQuotas ──> Kueue scheduler
```

### The `local-capacity` provider controller

A new reconciler package (proposed: `pkg/controller/core/nodecapacity/`) with the
primary object `CapacityProvider`, filtered to the objects it owns:

```go
const LocalCapacityControllerName kueuealpha.CapacityProviderControllerName = "kueue.x-k8s.io/local-capacity"
```

The reconciler:

1. Gets the `CapacityProvider`; skips it unless
   `spec.controllerName == LocalCapacityControllerName`.
2. For each flavor in `spec.orchestratedFlavors`, resolves the flavor's
   `spec.nodeLabels`, selects eligible matching nodes, and sums their allocatable.
3. Writes `status.capacity.flavors[]` and upserts the `CapacitySynchronized`
   condition.

Registration mirrors the DQO controller: it is set up in
`pkg/controller/core/core.go` alongside `dqo.NewReconciler(...)`, gated by the new
feature gate (see [Enablement](#enablement-and-feature-gate)).

To dispatch by controller name efficiently, a field index on
`CapacityProvider.spec.controllerName` is added in
`pkg/controller/core/indexer/indexer.go` (mirroring the existing `AdmissionCheck`
`spec.controllerName` index pattern in `pkg/util/admissioncheck/`). Note the
existing DQO→provider index keys the *orchestrator* by referenced provider names
and cannot be reused to select providers by controller name.

### Node discovery and watch

The controller `Watches(&corev1.Node{})` and `Watches(&kueue.ResourceFlavor{})`,
modeled on the TAS node controller (`pkg/controller/tas/node_controller.go`,
`pkg/controller/tas/resource_flavor.go`):

- A Node event is mapped to every `CapacityProvider` this controller owns whose
  `orchestratedFlavors` include a flavor the node matches. On an **update**, the
  mapping is computed against **both the old and the new** Node labels, and the
  **union** of matched providers is enqueued — otherwise a Node that *loses* a flavor
  label (or whose label value changes) would never re-reconcile the provider it just
  left, leaving that provider's published capacity stale and too high. This mirrors
  the TAS node controller, which forwards both the old and new Node objects on update
  (`resource_flavor.go` `NotifyNodeUpdate`). Requests are enqueued with
  `AddAfter(req, constants.UpdatesBatchPeriod)` to batch bursts.
- A `ResourceFlavor` event re-enqueues providers referencing that flavor, so
  `nodeLabels` edits re-bucket capacity.
- Node updates are change-gated: only changes to `Status.Allocatable`, `Labels`,
  `Spec.Unschedulable`, or the `Ready` condition trigger work — kubelet heartbeats
  (which only bump `LastHeartbeatTime`) are ignored. This matches the TAS
  `checkNodeSchedulingPropertiesChanged` approach and avoids hot-looping.

### Flavor bucketing via `ResourceFlavor.nodeLabels`

A node belongs to a flavor when the node's labels are a superset of the flavor's
`spec.nodeLabels`. This reuses the exact TAS matcher:

```go
// pkg/util/tas/node.go
utiltas.NodeMatchesFlavor(node.Labels, flavor.Spec.NodeLabels, /*requiredLevels=*/ nil)
```

Passing `nil` for the topology levels reduces it to pure label-superset matching.
A node matching multiple flavors contributes to each (documented overlap
semantics). A flavor with empty `nodeLabels` matches all nodes — this is a valid
"whole cluster" configuration and is intentional.

### Node label source of truth and prerequisites

Two distinct sources of truth back the bucketing:

- **Which labels a flavor requires** — `ResourceFlavor.spec.nodeLabels`, authored by
  the administrator.
- **The node's actual labels** — `Node.metadata.labels`, the live runtime value the
  provider reads. These are set by kubelet at registration (restricted to well-known
  keys by the `NodeRestriction` admission plugin) and, for the custom labels a flavor
  typically keys on, by the provisioning layer (cloud provider, node-pool config,
  cluster autoscaler, or an admin/DaemonSet). kubelet cannot self-set arbitrary
  custom labels.

**Label trust boundary (security).** `NodeRestriction` only protects a specific set of
keys — notably the `node-restriction.kubernetes.io/` prefix and certain reserved
`*.kubernetes.io/` keys — it does **not** prevent a kubelet from setting *arbitrary
custom* label keys on its own Node. Because the same `nodeLabels` drive both capacity
attribution (this provider) and workload placement (the scheduler's flavor
assignment), a compromised or misbehaving kubelet that self-applies an unprotected
flavor label could cause its node's capacity to be counted toward a flavor and matching
workloads to be scheduled onto it (CWE-807 — relying on an untrusted input in a
security decision). Therefore, flavor `nodeLabels` used for capacity attribution
**SHOULD use an admission-protected key** — e.g. the `node-restriction.kubernetes.io/`
prefix enforced by the `NodeRestriction` plugin — or otherwise be applied only by a
trusted component (cloud provider, provisioner, or an admin-controlled DaemonSet),
never self-set by the kubelet. The KEP documentation will state this requirement and
the required admission configuration. This is not a trust assumption newly introduced
by this KEP — the scheduler already keys placement off these labels — but capacity
attribution broadens the blast radius, so it is called out explicitly here.

The provider attributes a node's capacity to a flavor only when the node's labels are
a superset of the flavor's `nodeLabels`, using the existing matcher
`utiltas.NodeMatchesFlavor` (`pkg/util/tas/node.go`), which checks every `(k, v)` in
`flavor.Spec.NodeLabels` against the node's labels.

**Consistency with pod placement.** These same `nodeLabels` are what Kueue injects as
the admitted workload's `nodeSelector` at unsuspend time: `podset.FromAssignment`
copies `flavor.Spec.NodeLabels` into the pod-set node selector
(`pkg/podset/podset.go`, `utilmaps.Copy(&info.NodeSelector, flv.Spec.NodeLabels)`),
which is applied to the pods via `RunWithPodSetsInfo`. So the exact labels that make
the provider *count* a node are the same labels that make admitted pods *land* on it —
capacity accounting and pod placement key off one source of truth. Note the resolved
labels are not persisted on the Workload: `Workload.Status.Admission.PodSetAssignments`
stores only the assigned `ResourceFlavorReference`s
(`apis/kueue/v1beta2/workload_types.go`), and the label map is re-resolved from the
live `ResourceFlavor` each time.

**Prerequisite and failure mode.** For capacity to be attributed, the labels in
`ResourceFlavor.nodeLabels` must actually be present on the Nodes. If the provisioning
layer does not label nodes (or labels them inconsistently), a flavor matches zero
nodes and reports zero capacity — which, under a distributing DQO, would drive the
effective quota to zero. This is the same labeling prerequisite the scheduler already
relies on for flavor assignment, but the blast radius here is larger: it zeroes a
queue's quota rather than blocking one workload's placement. Because a genuinely empty
pool also reports zero, the provider does not treat "zero matching nodes" as an error
condition; instead it surfaces an observable signal (event/log, and a human-readable
note in `status`) when an orchestrated flavor matches zero eligible nodes, so
operators can distinguish a labeling gap from a legitimately empty pool.

### Node eligibility filter

Only nodes that are schedulable and ready contribute, reusing the TAS rule
(`pkg/cache/scheduler/tas_nodes_cache.go`):

```go
eligible := !node.Spec.Unschedulable &&
    utiltas.IsNodeStatusConditionTrue(node.Status.Conditions, corev1.NodeReady)
```

Cordoned, draining, or `NotReady` nodes are excluded from the sum. Node taints are
**not** used to exclude a node from capacity here (taint tolerability is a
per-workload admission concern handled by the scheduler); this matches TAS, which
stores taints but does not exclude on them when computing capacity.

### Allocatable summation

Capacity is summed from `node.Status.Allocatable` (not `Capacity`), reusing the
`pkg/resources` primitives:

```go
acc := resources.NewRequests()
for _, n := range eligibleNodesForFlavor {
    acc.Add(resources.NewRequestsFromResourceList(n.Status.Allocatable))
}
resourceList := acc.ToResourceList(/*formatter*/) // -> corev1.ResourceList
```

The set of resource keys a flavor reports is a **fixed, configured set** per flavor —
sourced from the provider's `parameters` (defaulting to `cpu` and `memory`, extensible
to the specific extended/custom resources an operator tracks) — **not** merely
whichever keys the currently-matched nodes happen to expose. This is what makes the
**empty-pool** case well-defined: if the last eligible node for a flavor disappears,
the controller publishes those configured keys with **zero** quantities rather than an
empty list. An empty `ResourceList` would both fail the `CapacityProvider` CRD CEL
validation (which requires 1–64 entries per flavor) and cause the status update to be
rejected — leaving the previous *positive* capacity published and the quota
stale-high. Publishing explicit zeros overwrites the stale value and correctly drives
the effective quota to zero. The published list must have between 1 and 64 entries per
flavor. CPU is summed in milliCPU; other resources in their native units.

### Writing `CapacityProvider.status`

The controller writes the normalized capacity and condition via the status
subresource, replicating the pattern proven in the DQO integration test provider
(`setCapacityProviderCapacity`):

```go
cp.Status.Capacity = &kueuealpha.CapacityProviderNormalizedCapacity{
    Flavors: []kueuealpha.CapacityProviderNormalizedCapacityFlavor{
        {Name: "critical-flavor", Resources: corev1.ResourceList{"example.com/critical-resource": resource.MustParse("40")}},
    },
}
apimeta.SetStatusCondition(&cp.Status.Conditions, metav1.Condition{
    Type:               kueuealpha.CapacityProviderCapacitySynchronized,
    Status:             metav1.ConditionTrue,
    Reason:             kueuealpha.CapacityProviderReasonSynchronized,
    ObservedGeneration: cp.Generation,
})
// client.Status().Update(ctx, cp)
```

Only flavors listed in `spec.orchestratedFlavors` are written; DQO ignores entries
for unlisted flavors regardless.

### Condition semantics

The controller sets the single `CapacitySynchronized` condition using the reasons
already defined on the API:

| Status | Reason | When |
|---|---|---|
| `True` | `Synchronized` | Capacity successfully observed from Nodes and written to `status.capacity`. |
| `False` | `SourceUnavailable` | The Node list/watch could not be read. |
| `False` | `InvalidCapacity` | A node reported unparseable/negative allocatable quantities. |
| `False` | `Misconfigured` | `spec.orchestratedFlavors` references a non-existent `ResourceFlavor`, or the spec is otherwise invalid. |

When the condition is not `True`, DQO drops this provider from aggregation and
marks the orchestrator `EffectiveCapacityComputed=False`/`ProviderNotReady` — no
new behavior is needed here beyond setting the condition correctly.

### Capacity ownership and overlap

A node's physical capacity can, in principle, be counted toward more than one quota
subtree. Whether that is correct or an overcommit depends on the topology. Three
cases arise; DQO handles the first, and the other two are the operator's
responsibility with limitations this KEP documents explicitly. (All behavior below
was verified against `pkg/controller/core/dqo/distribution.go` and `discovery.go`.)

**Case 1 — multiple ClusterQueues/Cohorts in the same subtree (one DQO): handled.**
When one distributing DQO owns the subtree, the aggregated capacity for each
`(flavor, resource)` pair is split **proportionally to each participant's configured
`spec.resourceGroups` nominalQuota**, using a largest-remainder method (round down,
then distribute the leftover units by descending remainder, ties broken by object
UID) — `distribution.go` (`distributeCapacityProportionally`). The distributed shares
sum to the aggregated capacity, so there is no overcommit, and the queues can still
borrow within the cohort. This is the intended path for several queues backed by the
same node pool.

For example, a pool of 40 units of `critical-flavor` with `cq1` (nominalQuota 10) and
`cq2` (nominalQuota 30) under cohort `prod`, distributed by one DQO rooted at `prod`,
yields effective quotas `cq1 = 10`, `cq2 = 30`.

**Case 2 — disjoint subtrees / multiple DQOs sharing one provider: NOT prevented.**
Discovery is per-orchestrator and stateless with respect to other DQOs: each DQO
aggregates the *full* reported capacity of every `CapacityProvider` it references and
distributes it into its own subtree (`discovery.go`, `reconcileDiscovery`). DQO's
soft-validation only deactivates a DQO whose subtree root is a descendant of, or
identical to, another DQO's root, and only checks target ownership via
`status.effectiveQuotas.orchestratorRef` (`distribution.go`,
`findConflictingDistributingDQO` / `findOwnershipConflict`). None of these checks
examine which `CapacityProvider` a DQO references. Consequently, **two DQOs with
disjoint subtree roots that both reference the same node-based `CapacityProvider` each
receive the full capacity**, over-committing the physical nodes.

The supported way to share one pool across independent subtrees is to **partition it
with `effectiveCapacityMultiplier`** — e.g. `0.5` on each of two DQOs so each gets
half (this is DQO KEP-12382 Story 3, "Capacity partitioning"). It is a manual,
operator-set partition; there is no automatic cross-DQO capacity accounting today.
This KEP does not add such accounting in alpha; it is called out as a known
limitation and a possible future enhancement (e.g. a provider- or DQO-level guard that
detects one provider's capacity being claimed by multiple independent distributions
and surfaces a conflict condition).

**Case 3 — overlapping ResourceFlavors (a node matches two flavors): NOT detected.**
Because bucketing is a label-superset match, a node whose labels satisfy two flavors'
`nodeLabels` contributes its allocatable to *both* flavors. DQO aggregates capacity by
flavor name as a blind sum (`discovery.go`, `aggregateProviderCapacity` →
`MergeResourceListKeepSum`) and never inspects `ResourceFlavor.nodeLabels`, so it can
neither detect the double count nor tell that the capacity came from one shared node.
The expectation is therefore that **ResourceFlavors have disjoint `nodeLabels`** so
each node maps to exactly one flavor. To make violations visible rather than silent,
the node provider surfaces a signal (event/log and a `status` note) when it observes a
node matching more than one of its orchestrated flavors.

**Summary.** Within a single DQO-owned subtree, effective quota is assigned correctly
and proportionally. Sharing one physical pool across independent subtrees requires a
manual partition via the multiplier, and disjoint flavor `nodeLabels` are a
prerequisite. Neither cross-subtree double allocation nor flavor overlap is prevented
automatically in alpha; both are documented limitations.

### Enablement and feature gate

A dedicated feature gate `NodeCapacityProvider` (Alpha, default off) in
`pkg/features/kube_features.go` gates registration of the controller and its
indexer. It is kept separate from `DynamicQuotaOrchestration` so the node provider
can graduate on its own schedule; the end-to-end flow requires **both** gates on
(the provider writes `status.capacity`; DQO consumes it only when
`DynamicQuotaOrchestration` is enabled). With the gate off, the controller is not
registered and writes nothing.

### RBAC

New kubebuilder markers on the reconciler:

```
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=capacityproviders,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=capacityproviders/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch
```

Note this controller needs `capacityproviders/status` write access, which the DQO
reconciler deliberately does not have.

### End-to-end example

Administrator-authored `CapacityProvider` (spec only; status is filled by the
controller):

```yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: CapacityProvider
metadata:
  name: local-nodes
spec:
  controllerName: kueue.x-k8s.io/local-capacity
  orchestratedFlavors:
  - name: critical-flavor
```

Status written by the node-based provider:

```yaml
status:
  capacity:
    flavors:
    - name: critical-flavor
      resources:
        example.com/critical-resource: "40"   # raw sum of allocatable across eligible nodes
  conditions:
  - type: CapacitySynchronized
    status: "True"
    reason: Synchronized
```

`DynamicQuotaOrchestrator` applying 5% headroom and distributing to a Cohort:

```yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: DynamicQuotaOrchestrator
metadata:
  name: production-capacity
spec:
  capacityDiscovery:
    providers:
    - name: local-nodes
      effectiveCapacityMultiplier: "0.95"
  capacityDistribution:
    subtreeRootQuotaRef:
      kind: Cohort
      name: production
```

### Test Plan

#### Unit Tests

- flavor bucketing: superset match, empty `nodeLabels` (all nodes), node matching
  multiple flavors;
- node eligibility: cordoned / `NotReady` / tainted nodes;
- allocatable summation across nodes, per resource, milliCPU units;
- zero-capacity: an empty eligible-node set for a flavor yields the configured resource
  keys at zero, never an empty `ResourceList`;
- condition transitions: `Synchronized` ↔ `SourceUnavailable` / `Misconfigured`;
- change-gating: heartbeat-only node updates do not recompute.

#### Integration Tests

Under `test/integration/singlecluster/controller/` (new suite):

- create real Nodes + a `CapacityProvider` (`controllerName: kueue.x-k8s.io/local-capacity`)
  and assert `status.capacity` equals the summed allocatable and
  `CapacitySynchronized=True`;
- add/remove/cordon a node and assert `status.capacity` updates;
- edit a `ResourceFlavor.nodeLabels` and assert re-bucketing;
- **label removal:** a node that loses a flavor's label re-reconciles the affected
  provider and drops that node's capacity from the flavor (guards the old-and-new
  label union mapping);
- **empty pool:** draining the last eligible node for a flavor publishes the configured
  resource keys at zero — overwriting the prior positive capacity, not an empty list;
- end-to-end with DQO: create a `DynamicQuotaOrchestrator` referencing the provider
  and assert `status.effectiveQuotas` on the target `ClusterQueue`/`Cohort`,
  reusing the DQO suite helpers and `pkg/util/testing/v1alpha1` wrappers;
- feature-gate matrix: gate off ⇒ no controller, no status writes.

### Graduation Criteria

#### Alpha

- Introduce the `NodeCapacityProvider` feature gate (disabled by default).
- Implement the `local-capacity` `CapacityProvider` controller (Nodes-only).
- Reuse `ResourceFlavor.nodeLabels` for bucketing and the TAS eligibility rule.
- Unit and integration tests, including an end-to-end test with DQO distribution.

#### Beta

- Fix all known bugs; address user feedback.
- Add DRA (`ResourceSlice` device/counter) discovery as an additional capacity
  source for the same provider.
- Re-evaluate overlap semantics for nodes matching multiple flavors.
- Document operational behavior, observability, and upgrade expectations.
- Enable the feature gate by default.

#### Stable

- Fix all known bugs; address production feedback.
- Scalability tests for large node counts and frequent churn.
- Lock the feature gate to enabled.

## Implementation History

- 2026-09-21: Initial KEP draft (internal review).

## Drawbacks

Adds a controller that watches every Node, increasing the controller-manager's
watch and reconcile load in large clusters. Change-gating and batched requeues
mitigate this, but the base cost of a cluster-wide Node informer is real. As with
DQO, effective quota now derives partly from runtime cluster state, so inspecting
`spec` alone no longer tells the full quota story.

## Alternatives

### Provider-side headroom field

Add a headroom/reserve knob to this controller (e.g. via `spec.parameters`).
Rejected for alpha: DQO already provides `effectiveCapacityMultiplier` for exactly
this purpose, so a provider-side field would duplicate the concern and grow API
surface. Revisit only if per-flavor headroom (which the multiplier cannot express
per flavor within one provider) proves necessary.

### A dedicated node-selector on the provider

Give the provider its own node selector (via a parameters CRD) instead of reusing
`ResourceFlavor.nodeLabels`. Rejected: it creates a second place node→flavor
membership is defined, which can drift from the `nodeLabels` the scheduler actually
uses for admission. Reusing `nodeLabels` keeps a single source of truth.

### A `nodeQuotaPolicy` field directly on ClusterQueue

The original #10270 direction (see the closed PR #10745) added a `NodeQuotaPolicy`
CRD/field that wrote quota straight onto the ClusterQueue. Rejected upstream in
favor of DQO: it bypassed the generic capacity pipeline and would have written to
admin-owned spec. This KEP intentionally slots into DQO instead.

### Reuse the `DynamicQuotaOrchestration` feature gate

Gate this controller under the existing DQO gate rather than a new one. Rejected:
a dedicated `NodeCapacityProvider` gate lets the node plugin graduate independently
of the DQO framework and of other future providers (MultiKueue, DRA), matching how
Kueue gates other pluggable capabilities.
