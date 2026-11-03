---
title: "Dynamic Quota Orchestration"
linkTitle: "Dynamic Quota Orchestration"
date: 2026-10-30
weight: 8
description: >
  Derive ClusterQueue and Cohort quotas from the capacity that providers report.
---

{{< feature-state state="alpha" for_version="v0.20" >}}

Dynamic Quota Orchestration (DQO) lets Kueue set quotas from the capacity that
is actually available, instead of only from the numbers an administrator writes
in `spec.resourceGroups`. External controllers report capacity, Kueue adds it up
and splits it across a subtree of [ClusterQueues](/docs/concepts/cluster_queue)
and [Cohorts](/docs/concepts/cohort).

DQO works in two phases:

- **Discovery:** Kueue reads capacity from one or more `CapacityProvider`
  objects and computes the total effective capacity.
- **Distribution:** Kueue splits that capacity across the ClusterQueues and
  Cohorts in a subtree, in proportion to their configured nominal quotas, and
  writes the result to `status.effectiveQuotas`.

For every field, see the
[v1alpha1 API reference](/docs/reference/kueue.v1alpha1/).

## Objects

### CapacityProvider

A `CapacityProvider` is a cluster-scoped object (short name `cp`) that holds
capacity reported by a provider controller.

- `spec.controllerName` names the controller that publishes the capacity. It
  is immutable.
- `spec.orchestratedFlavors` lists the
  [ResourceFlavors](/docs/concepts/resource_flavor) that the provider reports
  capacity for. Kueue ignores flavors in the status that are not in this list.
- `status.capacity.flavors` holds the reported capacity per flavor and
  resource.
- The `CapacitySynchronized` condition tells Kueue whether the status is
  current. Kueue only checks its `status` and `observedGeneration`. The API
  defines the reasons `Synchronized`, `SourceUnavailable`, `InvalidCapacity`
  and `Misconfigured` for provider controllers to use.

{{% alert title="Warning" color="warning" %}}
Listing a flavor in `spec.orchestratedFlavors` puts all resources of that
flavor under DQO management. Providers must report every resource that the
queues in the subtree configure for that flavor. A resource they do not report
gets an effective nominal quota of 0.
{{% /alert %}}

Kueue only reads CapacityProviders. A provider controller writes the status.
Kueue uses the capacity only when `CapacitySynchronized` is `True` and the
condition's `observedGeneration` equals the object's `metadata.generation`.
Otherwise Kueue treats the provider as not ready.

Kueue does not include a provider controller yet. In this alpha version, you
run your own controller, or write the status by hand for testing.

### DynamicQuotaOrchestrator

A `DynamicQuotaOrchestrator` is a cluster-scoped object (short name `dqo`).

- `spec.capacityDiscovery.providers` lists 1 to 8 CapacityProviders. Each entry
  can set an `effectiveCapacityMultiplier` (default 1, must be 0 or more).
- `spec.capacityDistribution` is optional. When set, its required
  `subtreeRootQuotaRef` points to the ClusterQueue or Cohort at the root of the
  subtree that receives the capacity. Omit `spec.capacityDistribution` to run in
  discovery-only mode.

### Effective quotas

ClusterQueues and Cohorts have a `status.effectiveQuotas` field. It holds the
quotas to use instead of `spec.resourceGroups`, and an `orchestratorRef` that
names the component that wrote them. When DQO writes the field,
`orchestratorRef` names the DynamicQuotaOrchestrator.

## Discovery

For each entry in `spec.capacityDiscovery.providers`, Kueue multiplies the
capacity that the CapacityProvider reports by that entry's
`effectiveCapacityMultiplier`. Then it adds the results for each pair of
ResourceFlavor and resource. The total goes to `status.effectiveCapacity` on
the DynamicQuotaOrchestrator.

Some examples of multipliers from the KEP:

- `0.95` keeps 5% headroom.
- `0.5` gives half of a shared pool to this orchestrator.
- A value above `1` overbooks the quota on a MultiKueue manager cluster. Then
  workloads that hold manager quota while they wait for a worker cluster do not
  block other workloads.

The `EffectiveCapacityComputed` condition reports the result:

| Reason | Meaning |
|---|---|
| `Computed` | Kueue computed the effective capacity. |
| `ProviderNotReady` | A provider is not synchronized, or its condition is stale. |
| `Misconfigured` | A referenced CapacityProvider does not exist. |
| `AggregationFailed` | The total has more than 128 flavors, or more than 64 resources in one flavor. |

When the condition is `False`, Kueue clears `status.effectiveCapacity`.

## Distribution

For each pair of flavor and resource, Kueue splits the effective capacity
across the ClusterQueues and Cohorts in the subtree that list that pair in
`spec.resourceGroups`. Each one gets a share in proportion to its nominal quota
for that pair. The split is flat: Cohorts and ClusterQueues at every level of
the subtree share one pool, and a Cohort's share is not split again among its
children.

For example, `team-a` has a nominal quota of 10 CPUs and `team-b` has 20 CPUs.
If the effective capacity is 48 CPUs, `team-a` gets 16 and `team-b` gets 32.

Kueue rounds down to whole units: 1m for CPU and 1 for every other resource.
It gives the units that are left over to the participants with the largest
remainders. Ties go to the smaller UID. For example, 10 units split equally over three
ClusterQueues gives 4, 3 and 3.

Some edge cases:

- A flavor that DQO does not orchestrate keeps its quota from
  `spec.resourceGroups`.
- If an orchestrated flavor has a resource that the providers do not report,
  that resource gets an effective nominal quota of 0.
- If every participant has a nominal quota of 0 in `spec.resourceGroups` for a
  resource, every participant gets an effective nominal quota of 0.
- A Cohort with no `spec.resourceGroups` gets an empty list of effective
  quotas.

When `capacityDistribution` is set, the `Distributed` condition reports the
result. In discovery-only mode, the condition is not set.

| Reason | Meaning |
|---|---|
| `QuotasDistributed` | Kueue wrote effective quotas to the subtree. |
| `EffectiveCapacityNotComputed` | Discovery failed, so there is nothing to distribute. |
| `ConflictingDynamicQuotaOrchestrator` | An orchestrator with a higher root, or an older one with the same root, takes precedence. |
| `EffectiveQuotasConflict` | A ClusterQueue or Cohort in the subtree has effective quotas from another active orchestrator, or from a controller that is not DQO. |
| `Misconfigured` | The subtree root object does not exist, or its kind is not supported. A Cohort that is only named in a ClusterQueue's `spec.cohortName` is not an object, so create a Cohort for it. |

If two orchestrators manage overlapping subtrees, the one with the higher root
wins. If they have the same root, the one with the earlier `creationTimestamp`
wins. If both were created at the same time, the smaller UID wins. The winner
keeps precedence even when it is not distributing, for example because its
discovery fails. Then no orchestrator updates the subtree.

## Which quota Kueue uses

When the `DynamicQuotaOrchestration` feature gate is enabled and
`status.effectiveQuotas` is set, Kueue uses the effective quotas. Otherwise it
uses `spec.resourceGroups`. An empty list of effective quotas is still used as
is. Kueue does not fall back to the spec in that case.

Turning the feature gate off does not remove `status.effectiveQuotas`. If you
turn it on again, Kueue uses the old values until an orchestrator rewrites
them.

The scheduler, preemption and the quota metrics all use the same quotas.

For the other quota fields:

- `borrowingLimit` is copied from the spec.
- `lendingLimit` is copied from the spec. For a ClusterQueue, it is capped at
  the effective nominal quota.
- DQO does not apply resource transformations or DRA mappings. The scheduler
  still applies them as usual.

## Modes

- **Discovery only:** leave out `capacityDistribution`. Kueue computes
  `status.effectiveCapacity` but does not change any quota. Use this to check
  what DQO would do.
- **Distribution:** set `capacityDistribution`. Kueue writes
  `status.effectiveQuotas` to the subtree.

## Limits

| Field | Limit |
|---|---|
| Providers per DynamicQuotaOrchestrator | 1 to 8 |
| `orchestratedFlavors` per CapacityProvider | 1 to 64 |
| Flavors in a CapacityProvider status | 1 to 64 |
| Resources per flavor | 64 |
| Flavors in `effectiveCapacity` | 128 |
| Resource groups in `effectiveQuotas` | 16 |

## Caveats

DQO is alpha. Keep these points in mind:

- When capacity goes down, Kueue does not evict workloads that are already
  admitted. Usage can be above the new quota for a while.
- Effective quotas stay on every object that the orchestrator no longer
  writes. This happens when you delete the DynamicQuotaOrchestrator, remove
  `capacityDistribution`, change `subtreeRootQuotaRef`, or move a ClusterQueue
  or Cohort out of the subtree. It also happens when `Distributed` becomes
  `False` for any reason, such as a discovery failure or a conflict. Kueue
  keeps admitting workloads against the last values until you clear them. See
  [Clean up](/docs/tasks/manage/setup_dynamic_quota_orchestration/#clean-up).

## What's next?

- [Set up Dynamic Quota Orchestration](/docs/tasks/manage/setup_dynamic_quota_orchestration)
- Read the [KEP](https://github.com/kubernetes-sigs/kueue/tree/main/keps/12382-dynamic-quota-orchestration)
