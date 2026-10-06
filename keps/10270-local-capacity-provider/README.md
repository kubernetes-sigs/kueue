# KEP-10270: Local Capacity Provider for Dynamic Quota Orchestration

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Broken or cordoned nodes keep their quota, so admitted jobs stay Pending](#story-1-broken-or-cordoned-nodes-keep-their-quota-so-admitted-jobs-stay-pending)
    - [Story 2: New nodes sit idle until someone edits quota](#story-2-new-nodes-sit-idle-until-someone-edits-quota)
    - [Story 3: Team shares must follow a pool that changes size](#story-3-team-shares-must-follow-a-pool-that-changes-size)
    - [Story 4: Quota lags behind nodes moved between clusters](#story-4-quota-lags-behind-nodes-moved-between-clusters)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [Quota based on current nodes blocks ProvisioningRequest scale-up](#quota-based-on-current-nodes-blocks-provisioningrequest-scale-up)
    - [A node can label itself into a flavor](#a-node-can-label-itself-into-a-flavor)
    - [Overlapping flavors would count a node twice](#overlapping-flavors-would-count-a-node-twice)
- [Design Details](#design-details)
  - [Which nodes count](#which-nodes-count)
  - [Calculation](#calculation)
  - [Controller](#controller)
  - [Conditions and observability](#conditions-and-observability)
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
- [Examples](#examples)
  - [Shared pool for several teams](#shared-pool-for-several-teams)
  - [Proportional team shares](#proportional-team-shares)
  - [Several GPU types in one cluster](#several-gpu-types-in-one-cluster)
  - [Headroom for DaemonSets](#headroom-for-daemonsets)
  - [Reported capacity as nodes change](#reported-capacity-as-nodes-change)
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
tree.

No new API types or fields are introduced. The feature uses only the existing
`CapacityProvider`, `DynamicQuotaOrchestrator` and `ResourceFlavor` APIs.

## Motivation

Today `nominalQuota` is a static number, so it drifts from the real capacity of
the cluster:

- when a node pool is resized, or new nodes fail to join;
- when nodes break, are cordoned, or lose their accelerators;
- when nodes are moved between clusters, for example during an upgrade.

If quota is higher than the real capacity, workloads are admitted but stay
Pending. If it is lower, capacity sits idle. Administrators have to keep quota in
sync by hand or with custom scripts.
Topology-Aware Scheduling (TAS) closes part of this gap, because it does not admit
a workload when there are not enough physical nodes to place it, but not fully:

1. Not every setup uses TAS.
2. TAS cannot increase quota when new physical capacity is added, so new nodes
   still sit idle.
3. It is easier to explain to a team why its jobs do not schedule with an
   adjusted quota than with TAS placement failures, which are hard for end users
   to see.

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
   Enabling it without `DynamicQuotaOrchestration` fails at startup.

Object ownership and opt-in follow [KEP-12382](../12382-dynamic-quota-orchestration/README.md):
the administrator writes every spec, and this controller writes only
`CapacityProvider.status`.

### User Stories

#### Story 1: Broken or cordoned nodes keep their quota, so admitted jobs stay Pending

As a cluster administrator with a pool of H100 nodes, I want quota to equal the
GPUs that are actually usable. Today, when a node fails or is cordoned, its GPUs
stay in quota, so Kueue admits jobs that then stay Pending until someone lowers
`nominalQuota` by hand.

With this KEP the administrator keeps the ResourceFlavor and ClusterQueue as they
are, and asks Kueue to track the flavor's nodes:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ResourceFlavor
metadata:
  name: h100
spec:
  nodeLabels:
    example.com/gpu-type: h100
---
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

The controller then reports the healthy capacity:

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

When a node goes NotReady or is cordoned, `research` loses that node's GPUs
within seconds. When the node is repaired or replaced, the GPUs come back.
Running workloads are not evicted; only new admissions see the lower quota.

#### Story 2: New nodes sit idle until someone edits quota

As a cluster administrator, I add nodes to a pool shared by several teams. Today
the new GPUs sit idle until someone raises `nominalQuota`, and if only some of
the requested nodes join, the edit has to be corrected again.

With this KEP, a Cohort holds all the discovered capacity and every team borrows
from it. Quota grows as soon as each new node becomes Ready, by exactly the
capacity that joined. See [Shared pool for several teams](#shared-pool-for-several-teams).

#### Story 3: Team shares must follow a pool that changes size

As a cluster administrator, I want team-a to own 60% of the GPUs and team-b 40%,
whether the pool has 40 nodes or 60. Today each resize means recomputing and
editing both teams' quota.

With this KEP, the teams' spec `nominalQuota` holds only the ratio, and DQO
splits the node-derived capacity in that proportion.
See [Proportional team shares](#proportional-team-shares).

#### Story 4: Quota lags behind nodes moved between clusters

As an administrator replacing cluster A with cluster B, I move nodes over a few
at a time. Today both clusters' quota must be edited after every batch, and until
then A admits work onto nodes it no longer has.

With this KEP, both clusters run the setup from Story 1 or 2 with the same
flavor labels. Cordoning a node on A removes it from A's quota immediately, and
when it joins B and becomes Ready, B's quota grows. At every step, each cluster's
quota matches the nodes it actually has.

### Notes/Constraints/Caveats

- **Only node resources belong on an orchestrated flavor.** A provider
  orchestrates all resources of its flavors, so a resource declared for such a
  flavor that nodes do not advertise (for example a license token) is
  distributed as 0. Keep such resources on a separate flavor that is not
  orchestrated.
- **Only the resources used in quota matter.** The provider reports everything
  in `allocatable` (cpu, memory, pods, extended resources and so on). DQO ignores
  pairs that are not declared in the ClusterQueue/Cohort specs.

### Risks and Mitigations

Risks of DQO itself, such as stale quota when a provider stops updating or two
orchestrators sharing one provider, are inherited and handled as described in
[KEP-12382](../12382-dynamic-quota-orchestration/README.md#risks-and-mitigations).
The risks below are specific to deriving capacity from Nodes.

#### Quota based on current nodes blocks ProvisioningRequest scale-up

Kueue's ProvisioningRequest flow needs quota above the current capacity to
trigger scale-up, and quota based on current nodes never exceeds it.

Mitigation: document that this provider is for fixed or externally scaled pools,
and emit a warning event when a tracked flavor is used by a ClusterQueue with a
ProvisioningRequest admission check.

#### A node can label itself into a flavor

A kubelet that self-applies a flavor label gets its node's capacity counted for
that flavor and the flavor's workloads placed on it. This risk already exists for
placement; counting capacity from the same labels widens its impact to quota.

Mitigation: document that flavor labels must be set only by a trusted component,
as described in [Which nodes count](#which-nodes-count).

#### Overlapping flavors would count a node twice

Flavors with overlapping `nodeLabels` match the same node, for example a
label-less `default` flavor next to a GPU flavor.

Mitigation: the controller detects the overlap across all local-capacity
providers and marks the provider `Misconfigured`, so DQO keeps the last good
quota and the condition message names the node.

## Design Details

### Which nodes count

The ResourceFlavor is the node selector. Its `nodeLabels` are the same labels
Kueue uses to place workloads, so counting and placement always agree.

A node contributes to a flavor only if all of the following hold:

- it matches all of the flavor's `nodeLabels`;
- it is `Ready`, not `unschedulable`, and not being deleted;
- it has no NoSchedule/NoExecute taint that the flavor neither tolerates
  (`tolerations`) nor declares (`nodeTaints`).

A node that stops meeting these conditions is removed on the next sync.

Flavor labels should be set only by a trusted component, for example keys under
the `node-restriction.kubernetes.io/` prefix, which the `NodeRestriction`
admission plugin prevents kubelets from setting, not labels a node's kubelet can
set for itself.

### Calculation

```
per flavor, per resource: sum of node.status.allocatable over eligible nodes
```

- `status.allocatable` already excludes kubelet and system reservations.
- Running workloads are not subtracted; Kueue already tracks usage against quota.
- DQO applies `effectiveCapacityMultiplier` afterwards.
- A flavor with no eligible nodes is published with empty `resources`, and DQO
  then distributes 0 for its resources.

### Controller

The controller watches `CapacityProvider`, `Node` and `ResourceFlavor` objects.
Any change to a Node, a ResourceFlavor or a local-capacity CapacityProvider
enqueues all local-capacity providers, because it may affect any of them through
overlap detection. Node updates that cannot affect capacity are filtered out:
only changes to labels, taints, `unschedulable`, deletion, `allocatable` or
readiness are considered.

For each provider, the reconcile loop:

1. Reads all local-capacity CapacityProviders, ResourceFlavors and Nodes from the
   informer cache.
2. Checks that every flavor in `orchestratedFlavors` exists, and that no flavor
   is orchestrated by more than one local-capacity provider.
3. For each node, determines which orchestrated flavors (across all
   local-capacity providers) it matches. A node matching more than one flavor
   makes the provider `Misconfigured`.
4. Sums `allocatable` over the eligible nodes of each of the provider's flavors.
5. Writes `status.capacity` and the `CapacitySynchronized` condition, skipping
   writes that change nothing.

The controller needs `get/list/watch` on Nodes, ResourceFlavors and
CapacityProviders, which Kueue already has, and `get/update/patch` on
`capacityproviders/status`, which is new.

### Conditions and observability

| State | `CapacitySynchronized` condition |
|---|---|
| Normal, including zero nodes | `True`, `Synchronized`, with a message summarizing counted and excluded nodes per flavor |
| Nodes cannot be read | `False`, `SourceUnavailable`; the last capacity is kept |
| Missing flavor, flavor claimed twice, node matching several flavors, or more than 64 resources in a flavor | `False`, `Misconfigured`, with a message naming the flavor or node; the last capacity is kept |

Because DQO requires `CapacitySynchronized=True`, a provider in either `False`
state makes DQO stop redistributing and keep the last effective quotas.

For Beta we plan to add events for excluded nodes, and metrics for the number of
eligible and excluded nodes per flavor and the time of the last successful sync.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Prerequisite testing updates

None.

#### Unit tests

- `pkg/controller/core/localcapacity`: node eligibility (Ready, cordoned,
  deleting, taints vs. tolerations and nodeTaints, label match), summing
  allocatable, flavors without nodes published as empty, overlap detection,
  missing flavors, more than 64 resources, a failure to list Nodes keeping the
  last capacity (`SourceUnavailable`), unchanged status not rewritten, providers
  of other controllers ignored, feature gate disabled.

#### Integration tests

- Adding, cordoning or removing Nodes updates the CapacityProvider, then DQO,
  then the Cohort and ClusterQueue `effectiveQuotas`.
- Removing all nodes of a flavor drops quota to 0, not back to the spec value.
- Overlapping flavors set `Misconfigured` and effective quotas stay unchanged,
  even when more nodes join.
- Deleting an overlapping provider brings the remaining provider back to
  `Synchronized`.
- `effectiveCapacityMultiplier` scales the distributed quota (for example 80
  GPUs with `0.95` give 76).
- The scheduler admits workloads according to the node-derived quota.

#### e2e tests

- On a kind cluster, adding or removing worker nodes changes which workloads are
  admitted.
- Disabling the feature gates falls back to spec quota.

### Graduation Criteria

#### Alpha

- The controller and the `LocalCapacityProvider` feature gate are implemented.
- The tests above are implemented.
- Documentation describes the shared-Cohort setup and the user stories.

#### Beta

- User feedback is addressed.
- A scale test with thousands of nodes.
- Events and metrics for excluded nodes and sync time.
- An explicit per-flavor resource list, so the provider is not limited by the
  64-resource cap and can support per-resource headroom.
- An option to count a GPU node only once its GPUs are advertised, so its CPU and
  memory do not count while the device plugin is still starting.
- A Node cache transform that keeps only the fields the provider and TAS read,
  agreed with TAS owners because the Node informer is shared.
- Freshness and expiry of effective quota are revisited together with DQO.
- Decide whether any of the knobs listed under Alternatives are needed.

## Implementation History

- 2026-09: Initial draft on top of DQO.
- 2026-09: Proof of concept in
  [#16080](https://github.com/kubernetes-sigs/kueue/pull/16080).
- 2026-09-28: DQO zero-handling fix merged in
  [#16168](https://github.com/kubernetes-sigs/kueue/pull/16168).

## Drawbacks

- Less tunable: no per-node reserve, no delay before new nodes count, and no node
  filter beyond the flavor's labels.
- The quota in effect is in status rather than spec, which is less obvious to
  administrators. This is inherent to DQO.
- Resources that nodes do not advertise cannot share an orchestrated flavor.

## Alternatives

### A LocalCapacity configuration API

A cluster-scoped `LocalCapacity` object, referenced from
`CapacityProvider.spec.parameters`, could hold:

- an explicit resource list, which would let the provider publish explicit zeros
  per resource and lift the 64-resource limit (planned for Beta);
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

## Examples

These examples reuse the `h100` ResourceFlavor and the `nodes` CapacityProvider
from [Story 1](#story-1-broken-or-cordoned-nodes-keep-their-quota-so-admitted-jobs-stay-pending).

### Shared pool for several teams

For [Story 2](#story-2-new-nodes-sit-idle-until-someone-edits-quota). The Cohort
is the only participant with a non-zero spec value, so DQO gives it 100% of the
capacity. Team ClusterQueues list the same flavor and resources with `0`, so they
can borrow from it. The Cohort's spec value is the fallback before DQO first
writes effective quota, so set it to today's static quota.

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
      - {name: nvidia.com/gpu, nominalQuota: "80"}
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
---
apiVersion: kueue.x-k8s.io/v1alpha1
kind: DynamicQuotaOrchestrator
metadata:
  name: shared-pool
spec:
  capacityDiscovery:
    providers:
    - name: nodes
  capacityDistribution:
    subtreeRootQuotaRef: {kind: Cohort, name: shared-pool}
```

To check the numbers before switching on, first create the DQO without
`capacityDistribution` and compare `status.effectiveCapacity` with the current
quota.

### Proportional team shares

For [Story 3](#story-3-team-shares-must-follow-a-pool-that-changes-size). Use the
shared-pool setup, but set the team ClusterQueues' spec `nominalQuota` to the
ratio (for example `6` and `4`) and the Cohort's to `0`. With 10 nodes (80 GPUs),
team-a gets 48 and team-b 32; with 5 nodes left (40 GPUs), they get 24 and 16.
Unused quota can still be borrowed through the Cohort.

### Several GPU types in one cluster

Create one ResourceFlavor per pool, with non-overlapping `nodeLabels`, and list
both in the same CapacityProvider. Each flavor's quota follows only its own nodes.

```yaml
spec:
  controllerName: kueue.x-k8s.io/local-capacity
  orchestratedFlavors:
  - name: h100
  - name: a100
```

### Headroom for DaemonSets

When monitoring and networking DaemonSets use about 5% of each node, set a
multiplier on the provider in the DQO. 80 discovered GPUs become 76 of quota, and
1200 CPUs become 1140.

```yaml
  capacityDiscovery:
    providers:
    - name: nodes
      effectiveCapacityMultiplier: "0.95"
```

### Reported capacity as nodes change

10 nodes, each with 8 GPUs, 120 CPUs and 900Gi of allocatable memory. The DQO
multiplier is 1.

| Event | Reported (GPU / CPU / memory) |
|---|---|
| 10 nodes | 80 / 1200 / 9000Gi |
| Pool target raised to 14, only 2 nodes join | 96 / 1440 / 10800Gi |
| 3 nodes leave | 72 / 1080 / 8100Gi |
| All nodes gone | flavor published with empty `resources`; all resources count as 0 |
| A 16-GPU job starts | no change |
