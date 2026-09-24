# KEP-10270: Local Capacity Provider for Dynamic Quota Orchestration

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Who creates what](#who-creates-what)
  - [User Stories](#user-stories)
    - [Story 1: Quota follows a fixed GPU pool](#story-1-quota-follows-a-fixed-gpu-pool)
    - [Story 2: One shared pool for several teams](#story-2-one-shared-pool-for-several-teams)
    - [Story 3: Guaranteed shares that scale with the cluster](#story-3-guaranteed-shares-that-scale-with-the-cluster)
    - [Story 4: Several GPU types in one cluster](#story-4-several-gpu-types-in-one-cluster)
    - [Story 5: Leaving room for DaemonSets](#story-5-leaving-room-for-daemonsets)
    - [Story 6: Moving nodes between clusters](#story-6-moving-nodes-between-clusters)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Example](#example)
  - [Which nodes count](#which-nodes-count)
  - [Calculation](#calculation)
  - [Controller](#controller)
  - [DQO change](#dqo-change)
  - [Conditions and observability](#conditions-and-observability)
  - [Worked example](#worked-example)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [A LocalCapacity configuration API](#a-localcapacity-configuration-api)
  - [Write quota into spec](#write-quota-into-spec)
  - [External controller only](#external-controller-only)
  - [Remember reported resources instead of changing DQO](#remember-reported-resources-instead-of-changing-dqo)
<!-- /toc -->

## Summary

This KEP adds a built-in capacity provider for Dynamic Quota Orchestration (DQO,
[KEP-12382](../12382-dynamic-quota-orchestration/README.md)) that derives
capacity from the Nodes in the cluster.

An administrator creates a `CapacityProvider` with
`controllerName: kueue.x-k8s.io/local-capacity` and lists the ResourceFlavors to
track. For each flavor, a new controller in kueue-controller-manager sums the
allocatable resources of the healthy Nodes that match the flavor's `nodeLabels`,
and publishes the totals in `CapacityProvider.status.capacity`. The existing DQO
controller then distributes that capacity as quota across a Cohort/ClusterQueue
tree through `status.effectiveQuotas`.

No new API types or fields are introduced. The feature uses only the existing
`CapacityProvider`, `DynamicQuotaOrchestrator` and `ResourceFlavor` APIs, plus a
small change to how DQO treats resources that a provider does not report.

## Motivation

Today `nominalQuota` is a static number, so it drifts from the real capacity of
the cluster:

- when a node pool is resized, or new nodes fail to join;
- when nodes break, are cordoned, or lose their accelerators;
- when nodes are moved between clusters, for example during an upgrade.

If quota is higher than the real capacity, workloads are admitted but stay
Pending. If it is lower, capacity sits idle. Administrators have to keep quota in
sync by hand or with custom scripts.

DQO (alpha in v0.20) provides the machinery to turn reported capacity into quota,
but it deliberately leaves concrete providers, such as a node-based one, to
separate KEPs. This is that KEP for
[#10270](https://github.com/kubernetes-sigs/kueue/issues/10270). It replaces the
earlier proposal in [#10745](https://github.com/kubernetes-sigs/kueue/pull/10745),
which was closed in favour of building on DQO.

### Goals

- Quota tracks the actual, healthy node capacity of each ResourceFlavor.
- Reuse DQO for aggregation, headroom (`effectiveCapacityMultiplier`) and
  distribution. The provider only reports capacity.
- Add no new API objects; configuration comes from existing ResourceFlavors and
  CapacityProviders.
- Never count a node twice, and never report a fake zero when the controller
  cannot observe Nodes.

### Non-Goals

- Writing quota into ClusterQueue or Cohort `spec`.
- A provider-specific configuration API. Possible knobs are listed under
  [Alternatives](#alternatives), to be revisited for Beta.
- DRA / ResourceSlice capacity, tracked separately in
  [#14977](https://github.com/kubernetes-sigs/kueue/issues/14977).
- Counting capacity that does not exist yet, such as autoscaler maximums or
  ProvisioningRequests.
- Evicting running workloads when capacity drops.

## Proposal

1. A new controller in kueue-controller-manager serves every `CapacityProvider`
   whose `spec.controllerName` is `kueue.x-k8s.io/local-capacity`, the name
   already used as an example in KEP-12382. It ignores providers addressed to
   other controllers.
2. For each flavor in the provider's `spec.orchestratedFlavors`, the controller
   finds the matching Nodes using the ResourceFlavor's `nodeLabels`,
   `nodeTaints` and `tolerations`, and reports the sum of their allocatable
   resources.
3. A new feature gate, `LocalCapacityProvider` (alpha, disabled by default),
   which requires `DynamicQuotaOrchestration`.
4. It relies on a small DQO change, proposed separately in
   [#16168](https://github.com/kubernetes-sigs/kueue/pull/16168) and needed for
   correct zeros (see [Notes](#notesconstraintscaveats)): a provider orchestrates
   all resources of the flavors in its `orchestratedFlavors`, so capacity it does
   not report for such a flavor counts as `0` instead of falling back to the spec
   value.

### Who creates what

Kueue never creates these objects on its own. Spec and status have separate
owners, just as for a Deployment:

| Object | `spec` written by | `status` written by |
|---|---|---|
| `ResourceFlavor` | Administrator | – |
| `CapacityProvider` | Administrator | Local-capacity controller |
| `DynamicQuotaOrchestrator` | Administrator | DQO controller |
| `Cohort` / `ClusterQueue` | Administrator (quota structure and proportions) | DQO controller (`effectiveQuotas`) |

The administrator creates these objects once, with `kubectl`, GitOps or Helm.
After that, changes in the set of Nodes only change status: Nodes, then
`CapacityProvider.status`, then `effectiveQuotas`. No spec is rewritten, so the
configuration can live in Git while the frequently changing numbers stay in
status.

Opt-in is explicit, as in KEP-12382: nothing changes in a cluster until an
administrator creates a `CapacityProvider` and a `DynamicQuotaOrchestrator`.

### User Stories

All stories assume Kueue is installed with the alpha APIs and both feature gates
enabled. With Helm:

```yaml
enableAlphaAPIs: true
controllerManager:
  featureGates:
  - name: DynamicQuotaOrchestration
    enabled: true
  - name: LocalCapacityProvider
    enabled: true
```

#### Story 1: Quota follows a fixed GPU pool

*As a cluster administrator with a pool of H100 nodes and a single team, I want
the team's quota to equal the GPUs that are actually usable, with no YAML edits
when nodes fail or are replaced.*

1. Describe the nodes with a ResourceFlavor, as today:

   ```yaml
   apiVersion: kueue.x-k8s.io/v1beta2
   kind: ResourceFlavor
   metadata:
     name: h100
   spec:
     nodeLabels:
       example.com/gpu-type: h100
   ```

2. Keep the ClusterQueue as it is. Its `nominalQuota` now acts as a distribution
   weight and as the fallback, so leave it at today's value:

   ```yaml
   apiVersion: kueue.x-k8s.io/v1beta2
   kind: ClusterQueue
   metadata:
     name: research
   spec:
     namespaceSelector: {}
     resourceGroups:
     - coveredResources: [cpu, memory, nvidia.com/gpu]
       flavors:
       - name: h100
         resources:
         - {name: cpu, nominalQuota: "1200"}
         - {name: memory, nominalQuota: 9000Gi}
         - {name: nvidia.com/gpu, nominalQuota: "80"}
   ```

3. Ask Kueue to track the flavor's nodes, and distribute the result to the
   ClusterQueue:

   ```yaml
   apiVersion: kueue.x-k8s.io/v1alpha1
   kind: CapacityProvider
   metadata:
     name: nodes
   spec:
     controllerName: kueue.x-k8s.io/local-capacity
     orchestratedFlavors:
     - name: h100
   ---
   apiVersion: kueue.x-k8s.io/v1alpha1
   kind: DynamicQuotaOrchestrator
   metadata:
     name: research
   spec:
     capacityDiscovery:
       providers:
       - name: nodes
     capacityDistribution:
       subtreeRootQuotaRef: {kind: ClusterQueue, name: research}
   ```

4. Check what Kueue sees:

   ```sh
   kubectl get cp nodes -o yaml   # status.capacity and the "h100: 10 nodes" message
   kubectl get clusterqueue research -o jsonpath='{.status.effectiveQuotas}'
   ```

When a node goes NotReady or is cordoned, `research` loses that node's GPUs
within seconds. When the node is repaired or replaced, the GPUs come back.
Running workloads are not evicted; only new admissions see the lower quota.

#### Story 2: One shared pool for several teams

*As a cluster administrator, I want all discovered capacity to land in one
shared pool that every team borrows from, and I want to check the numbers
before switching anything on.*

1. Create a Cohort that holds all the capacity, and give each team ClusterQueue
   `0` for the same flavor and resources. The Cohort is the only participant
   with a non-zero value, so DQO gives it 100% of the capacity:

   ```yaml
   apiVersion: kueue.x-k8s.io/v1beta2
   kind: Cohort
   metadata:
     name: shared-pool
   spec:
     resourceGroups:
     - coveredResources: [nvidia.com/gpu]
       flavors:
       - name: h100
         resources:
         - {name: nvidia.com/gpu, nominalQuota: "80"}   # today's static quota: the fallback
   ---
   apiVersion: kueue.x-k8s.io/v1beta2
   kind: ClusterQueue
   metadata:
     name: team-a        # the same for team-b, team-c
   spec:
     cohortName: shared-pool
     namespaceSelector: {}
     resourceGroups:
     - coveredResources: [nvidia.com/gpu]
       flavors:
       - name: h100
         resources:
         - {name: nvidia.com/gpu, nominalQuota: "0"}
   ```

2. **Dry run.** Create the CapacityProvider from Story 1 and a DQO *without*
   `capacityDistribution`. DQO only reports what it found; quota is unchanged:

   ```yaml
   apiVersion: kueue.x-k8s.io/v1alpha1
   kind: DynamicQuotaOrchestrator
   metadata:
     name: shared-pool
   spec:
     capacityDiscovery:
       providers:
       - name: nodes
   ```

   Compare `kubectl get dqo shared-pool -o jsonpath='{.status.effectiveCapacity}'`
   with the current quota.

3. **Switch on.** Add distribution to the same DQO:

   ```yaml
     capacityDistribution:
       subtreeRootQuotaRef: {kind: Cohort, name: shared-pool}
   ```

4. **Switch off.** Remove `capacityDistribution` and clear
   `status.effectiveQuotas` on the Cohort and ClusterQueues. Kueue returns to
   the spec values from step 1.

The pool always equals the healthy nodes, and teams compete for it through
borrowing, with fair sharing or priorities as configured today.

#### Story 3: Guaranteed shares that scale with the cluster

*As a cluster administrator, I want team-a to always own 60% of the GPUs and
team-b 40%, whether the pool has 40 nodes or 60.*

Use the setup from Story 2, but set the team ClusterQueues' spec `nominalQuota`
to the ratio (for example `6` and `4`) and the Cohort's to `0`. DQO splits the
discovered capacity in that proportion: with 10 nodes (80 GPUs), team-a gets 48
and team-b 32; with 5 nodes left (40 GPUs), they get 24 and 16. Unused quota can
still be borrowed through the Cohort.

#### Story 4: Several GPU types in one cluster

*As a cluster administrator with H100 and A100 pools, I want the quota of each
pool tracked separately.*

Create one ResourceFlavor per pool, with non-overlapping `nodeLabels`, and list
both in the same CapacityProvider:

```yaml
spec:
  controllerName: kueue.x-k8s.io/local-capacity
  orchestratedFlavors:
  - name: h100
  - name: a100
```

Each flavor's quota follows only its own nodes. If the labels overlap, for
example because a label-less `default` flavor matches every node, the provider
reports `Misconfigured` and names the node. DQO keeps the last good quota until
the flavors are fixed, so nothing is double-counted.

#### Story 5: Leaving room for DaemonSets

*As a cluster administrator, my nodes run monitoring and networking DaemonSets
that use about 5% of each node, and Kueue should not hand that capacity out.*

Set a multiplier on the provider in the DQO:

```yaml
  capacityDiscovery:
    providers:
    - name: nodes
      effectiveCapacityMultiplier: "0.95"
```

80 discovered GPUs become 76 of quota, and 1200 CPUs become 1140.

#### Story 6: Moving nodes between clusters

*As an administrator replacing cluster A with cluster B, I move nodes over a few
at a time and want quota on both clusters to follow without edits.*

1. Set up Story 1 or 2 on both clusters, with the same flavor labels.
2. On cluster A, cordon a node before draining it. Cordoning removes the node
   from A's quota immediately, so A stops admitting work onto it.
3. When the node joins cluster B and becomes Ready, B's quota grows
   automatically.

At every step, each cluster's quota matches the nodes it actually has.

### Notes/Constraints/Caveats

- **The ResourceFlavor is the node selector.** Which nodes count for a flavor is
  decided entirely by its `nodeLabels`. These are the same labels Kueue uses to
  place workloads, so counting and placement always agree.
- **Zeros need a DQO change.** Before #16168, if no provider reported a
  (flavor, resource) pair, DQO kept the spec value for it. Without a
  configuration API, the provider only knows the resources that the nodes
  currently advertise. If every GPU node disappears, `nvidia.com/gpu` would
  simply be missing from the report, and quota would silently fall back to the
  spec value instead of dropping to 0. With #16168, DQO adds every orchestrated
  flavor to `status.effectiveCapacity` (with `resources: {}` when nothing is
  reported) and distributes 0 for any declared pair missing from it. So when a
  flavor has no eligible nodes, the provider omits it and all of its resources
  count as 0; when a resource disappears from all nodes of a flavor, that
  resource counts as 0.
- **Only node resources belong on an orchestrated flavor.** Because the provider
  orchestrates all resources of its flavors, a resource declared for such a
  flavor in a ClusterQueue or Cohort that nodes do not advertise (for example a
  license token) is distributed as 0. Keep such resources on a separate flavor
  that is not orchestrated.
- **How DQO distributes.** DQO splits capacity in proportion to the
  `nominalQuota` values in spec. For a shared pool, the Cohort has a positive
  spec value and its ClusterQueues have 0, so the Cohort receives 100%.
  ClusterQueues must still list the same flavor/resource pairs (with 0) in order
  to borrow.
- **Only the resources used in quota matter.** The provider reports everything
  in `allocatable` (cpu, memory, pods, extended resources and so on). DQO ignores
  pairs that are not declared in the ClusterQueue/Cohort specs.
- **Spec is the fallback.** Before DQO first writes `effectiveQuotas`, and
  whenever the feature gates are disabled, the scheduler uses spec. We recommend
  setting the spec quota of the shared Cohort to today's static quota, so that
  the fallback stays sensible.

### Risks and Mitigations

| Risk | Mitigation |
|---|---|
| **Autoscaling.** Kueue's ProvisioningRequest flow needs quota above the current capacity to trigger scale-up; quota based on current nodes blocks that. | Document that this provider is for fixed or externally scaled pools. Emit a warning event when a tracked flavor is used by a ClusterQueue with a ProvisioningRequest admission check. |
| **Double counting.** Flavors with overlapping `nodeLabels` match the same node, for example a label-less `default` flavor next to a GPU flavor. | The controller detects the overlap across all local-capacity providers. The provider is marked `Misconfigured`, so DQO keeps the last good quota. |
| **Stale quota.** The controller is down, so quota stops updating. | DQO keeps the last value, which is its existing behavior. A last-sync metric together with an alert informs operators. |
| **Capacity drops below usage.** | Running workloads continue and new admissions wait. This matches lowering quota by hand today. |
| **Non-Kueue pods use node resources** (DaemonSets, agents). | Use DQO's `effectiveCapacityMultiplier` (for example `0.95`) as headroom. |
| **GPU node counted before its GPUs appear.** Its CPU and memory count while the device plugin is still starting. | This lasts only a short time and GPU quota itself is correct. If needed, add a required-resources option in Beta. |
| **Scalability.** Clusters with thousands of nodes produce many Node events. | Node updates that cannot change capacity, such as kubelet heartbeats, are filtered out, and no-op status writes are skipped. Beta requires a scale test. |

## Design Details

### Example

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ResourceFlavor            # existing; its nodeLabels select the nodes
metadata:
  name: h100
spec:
  nodeLabels:
    example.com/gpu-type: h100
---
apiVersion: kueue.x-k8s.io/v1alpha1
kind: CapacityProvider          # existing DQO API, no parameters needed
metadata:
  name: nodes
spec:
  controllerName: kueue.x-k8s.io/local-capacity
  orchestratedFlavors:
  - name: h100
---
apiVersion: kueue.x-k8s.io/v1alpha1
kind: DynamicQuotaOrchestrator  # existing DQO API
metadata:
  name: nodes
spec:
  capacityDiscovery:
    providers:
    - name: nodes
      effectiveCapacityMultiplier: "0.95"   # 5% headroom for non-Kueue pods
  capacityDistribution:
    subtreeRootQuotaRef:
      kind: Cohort
      name: shared-pool
```

The controller then writes:

```yaml
status:
  capacity:
    flavors:
    - name: h100
      resources:
        nvidia.com/gpu: "80"
        cpu: "1200"
        memory: 9000Gi
  conditions:
  - type: CapacitySynchronized
    status: "True"
    reason: Synchronized
    message: "h100: 10 nodes; excluded: NotReady=1"
```

### Which nodes count

A node contributes to a flavor only if all of the following hold:

- it matches all of the flavor's `nodeLabels`;
- it is `Ready`, not `unschedulable`, and not being deleted;
- it has no NoSchedule/NoExecute taint that the flavor neither tolerates
  (`tolerations`) nor declares (`nodeTaints`).

A node that stops meeting these conditions is removed on the next sync.

### Calculation

```
per flavor, per resource: sum of node.status.allocatable over eligible nodes
```

- `status.allocatable` already excludes kubelet and system reservations.
- Running workloads are not subtracted; Kueue already tracks usage against quota.
- DQO applies `effectiveCapacityMultiplier` afterwards.

### Controller

The controller watches `CapacityProvider`, `Node` and `ResourceFlavor` objects.
Any change to a Node or ResourceFlavor enqueues all local-capacity providers,
because it may affect any of them through overlap detection. Node updates that
cannot affect capacity are filtered out: only changes to labels, taints,
`unschedulable`, deletion, `allocatable` or readiness are considered.

For each provider, the reconcile loop:

1. Reads all local-capacity CapacityProviders, ResourceFlavors and Nodes from the
   informer cache.
2. Checks that every flavor in `orchestratedFlavors` exists, and that no flavor
   is orchestrated by more than one local-capacity provider.
3. For each node, determines which orchestrated flavors (across all
   local-capacity providers) it matches. A node matching more than one flavor
   makes the provider `Misconfigured`.
4. Sums `allocatable` over the eligible nodes of each of the provider's flavors.
   Flavors without eligible nodes are omitted; DQO treats them as having zero
   capacity.
5. Writes `status.capacity` and the `CapacitySynchronized` condition, skipping
   writes that change nothing.

The controller needs `get/list/watch` on Nodes, ResourceFlavors and
CapacityProviders, which Kueue already has, and `get/update/patch` on
`capacityproviders/status`, which is new.

### DQO change

Proposed separately in [#16168](https://github.com/kubernetes-sigs/kueue/pull/16168):

- A provider orchestrates all resources of the flavors in its
  `spec.orchestratedFlavors`; partial orchestration of a flavor for a subset of
  its resources is not supported. Empty `orchestratedFlavors` remains rejected.
- During discovery, DQO adds every orchestrated flavor of the referenced,
  synchronized providers to `status.effectiveCapacity`, with an empty
  `resources` map when no capacity is reported for it. The CEL rule on
  `resources` is relaxed from 1–64 to at most 64 entries to allow this.
- During distribution, which reads only `status.effectiveCapacity`, every
  (flavor, resource) pair declared in the subtree for a flavor present there but
  missing from its `resources` is distributed as zero capacity. Pairs of flavors
  that no provider orchestrates keep their spec value, as before.

The zero is therefore visible in DQO status. The DQO API is alpha and the DQO
feature gate is disabled by default.

### Conditions and observability

| State | `CapacitySynchronized` condition |
|---|---|
| Normal, including zero nodes | `True`, `Synchronized`, with a message summarizing counted and excluded nodes per flavor |
| Nodes cannot be read | `False`, `SourceUnavailable`; the last capacity is kept |
| Missing flavor, flavor claimed twice, or node matching several flavors | `False`, `Misconfigured`, with a message naming the flavor or node; the last capacity is kept |

Because DQO requires `CapacitySynchronized=True`, a provider in either `False`
state makes DQO stop redistributing and keep the last effective quotas.

For Beta we plan to add events for excluded nodes, and metrics for the number of
eligible and excluded nodes per flavor and the time of the last successful sync.

### Worked example

10 nodes, each with 8 GPUs, 120 CPUs and 900Gi of allocatable memory. The DQO
multiplier is 1.

| Event | Reported (GPU / CPU / memory) |
|---|---|
| 10 nodes | 80 / 1200 / 9000Gi |
| Pool target raised to 14, only 2 nodes join | 96 / 1440 / 10800Gi |
| 3 nodes leave | 72 / 1080 / 8100Gi |
| All nodes gone | flavor omitted, all resources count as 0 |
| A 16-GPU job starts | no change |

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Prerequisite testing updates

None.

#### Unit tests

- `pkg/controller/core/localcapacity`: node eligibility (Ready, cordoned,
  deleting, taints vs. tolerations and nodeTaints, label match), summing
  allocatable, omitted flavors, overlap detection, missing flavors, providers of
  other controllers ignored, feature gate disabled.
- `pkg/controller/core/dqo` (in #16168): unreported orchestrated flavors appear
  in `effectiveCapacity` with empty `resources`, their declared pairs are
  distributed as 0, and pairs of non-orchestrated flavors keep their spec value.

#### Integration tests

- Adding, cordoning or removing Nodes updates the CapacityProvider, then DQO,
  then the Cohort and ClusterQueue `effectiveQuotas`.
- Removing all nodes of a flavor drops quota to 0, not back to the spec value.
- Overlapping flavors set `Misconfigured` and effective quotas stay unchanged.
- The scheduler admits workloads according to the node-derived quota.

#### e2e tests

- On a kind cluster, adding or removing worker nodes changes which workloads are
  admitted.
- Disabling the feature gates falls back to spec quota.

### Graduation Criteria

#### Alpha

- The controller and the `LocalCapacityProvider` feature gate are implemented,
  on top of the DQO zero-handling change from #16168.
- The tests above are implemented.
- Documentation describes the shared-Cohort setup and the user stories.

#### Beta

- User feedback is addressed.
- A scale test with thousands of nodes.
- Events and metrics for excluded nodes and sync time.
- Freshness and expiry of effective quota are revisited together with DQO.
- Decide whether any of the knobs listed under Alternatives are needed.

## Implementation History

- 2026-09: Initial draft on top of DQO.
- 2026-09: Proof of concept in
  [#16080](https://github.com/kubernetes-sigs/kueue/pull/16080).

## Drawbacks

- Less tunable: no per-node reserve, no delay before new nodes count, and no node
  filter beyond the flavor's labels.
- The quota in effect is in status rather than spec, which is less obvious to
  administrators. This is inherent to DQO.
- Depends on the DQO change in #16168, which also means resources that nodes do
  not advertise cannot share an orchestrated flavor.

## Alternatives

### A LocalCapacity configuration API

A cluster-scoped `LocalCapacity` object, referenced from
`CapacityProvider.spec.parameters`, could hold:

- an explicit resource list, which would let the provider publish explicit zeros
  per resource;
- a per-node reserve for DaemonSets;
- required resources, such as GPUs, before a node counts;
- a delay before newly Ready nodes count;
- an extra node selector.

**Reasons for deferring:** it keeps the first version simple and adds a new API
object that users must understand. Because `parameters` is optional, adding it
later is backward compatible.

### Write quota into spec

The closed proposal in #10745 updated `nominalQuota` in ClusterQueue or Cohort
spec.

**Reasons for rejecting:** it conflicts with GitOps and mixes administrator
intent with computed values. DQO's `effectiveQuotas` in status solves both.

### External controller only

Leave node-based capacity entirely to external controllers, which DQO already
supports.

**Reasons for rejecting:** node capacity is the most common use case, and
KEP-12382 already uses `kueue.x-k8s.io/local-capacity` as its example of a
built-in provider.

### Remember reported resources instead of changing DQO

*Superseded by the DQO change in #16168.*

The provider could keep reporting, with value 0, every resource it has
previously published, so that DQO never sees a missing pair.

**Reasons for rejecting:** it is fragile. It does not work on the very first sync
of a flavor without nodes, and it cannot tell a resource that was removed on
purpose from one that disappeared temporarily.
