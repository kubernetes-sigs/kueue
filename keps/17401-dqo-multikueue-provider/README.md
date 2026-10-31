# KEP-17401: MultiKueue Capacity Provider

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Automatically sync the manager ClusterQueue quota with the worker ClusterQueues](#story-1-automatically-sync-the-manager-clusterqueue-quota-with-the-worker-clusterqueues)
    - [Story 2: Kueue and a GitOps tool overwrite the manager ClusterQueue quota](#story-2-kueue-and-a-gitops-tool-overwrite-the-manager-clusterqueue-quota)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
    - [Alpha scope](#alpha-scope)
    - [Relation to KEP-9988 (MultiKueue Manager Quota Automation)](#relation-to-kep-9988-multikueue-manager-quota-automation)
    - [Relation to KEP-12382 (Dynamic Quota Orchestration)](#relation-to-kep-12382-dynamic-quota-orchestration)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [The CapacityProvider and the DQO can target different ClusterQueues](#the-capacityprovider-and-the-dqo-can-target-different-clusterqueues)
    - [Disconnected worker clusters affect the manager quota](#disconnected-worker-clusters-affect-the-manager-quota)
    - [Alpha1 users lose quota automation after the upgrade](#alpha1-users-lose-quota-automation-after-the-upgrade)
- [Design Details](#design-details)
  - [API](#api)
  - [Validation](#validation)
  - [Finding the worker quotas](#finding-the-worker-quotas)
  - [CapacitySynchronized condition](#capacitysynchronized-condition)
  - [Upgrade from KEP-9988 Alpha1](#upgrade-from-kep-9988-alpha1)
  - [Test Plan](#test-plan)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [<code>spec.parameters</code> refers to a MultiKueueConfig](#specparameters-refers-to-a-multikueueconfig)
  - [<code>spec.parameters</code> refers to the manager ClusterQueue directly](#specparameters-refers-to-the-manager-clusterqueue-directly)
  - [The provider uses the DQO feature gate](#the-provider-uses-the-dqo-feature-gate)
<!-- /toc -->

## Summary

This KEP adds a built-in MultiKueue CapacityProvider for Dynamic Quota Orchestration (DQO, [KEP-12382]), as the Alpha2 stage of MultiKueue Manager Quota Automation ([KEP-9988]).
With this provider, the manager ClusterQueue quota changes when the worker quotas change, and Kueue does not change the ClusterQueue `spec`.

[KEP-12382]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/12382-dynamic-quota-orchestration
[KEP-9988]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/9988-multikueue-manager-quota-automation

## Motivation

In KEP-9988 Alpha1, the MultiKueue controller sums the quotas of the [related worker ClusterQueues].
It writes the sum to the manager `ClusterQueue.spec.resourceGroups[].nominalQuota`.
[KEP-9988 Drawbacks] lists this as a drawback:

> We allow Kueue to modify `spec` of objects it's supposed to reconcile (specifically, ClusterQueues). [...]
>
> * It blurs the line between configuration (managed by users) and state (managed by controllers).
> * It brings a risk of an infinite reconciling loop [...].

For example, if a tool manages ClusterQueues from Git, the tool and the controller can overwrite each other's changes.

[KEP-12382] adds DQO.
DQO reads capacity from a list of CapacityProviders, and writes the quota to `ClusterQueue.status.effectiveQuotas`.
A MultiKueue CapacityProvider lets MultiKueue use DQO.
This change gives these benefits:

- MultiKueue uses the same mechanism as other planned providers, for example nodes ([#10270]) and DRA ([#14977]).
- Admins can scale the worker capacity with `effectiveCapacityMultiplier`.
  For example, they can make the manager quota larger than the worker capacity.
  KEP-9988 lists this need as future work.

[related worker ClusterQueues]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/9988-multikueue-manager-quota-automation#defining-related-worker-clusterqueues
[KEP-9988 Drawbacks]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/9988-multikueue-manager-quota-automation#drawbacks
[#10270]: https://github.com/kubernetes-sigs/kueue/issues/10270
[#14977]: https://github.com/kubernetes-sigs/kueue/issues/14977

### Goals

- Define the MultiKueue CapacityProvider and its configuration API, `MultiKueueCapacityProviderConfig`.
- Replace the KEP-9988 Alpha1 mechanism with DQO.
  DQO writes the manager quota to `ClusterQueue.status.effectiveQuotas`, and Kueue no longer changes `ClusterQueue.spec`.
  This unblocks dropping the Kueue controller manager's RBAC permission to update `ClusterQueue` objects, if no other feature needs it.

This KEP is successful if the quota of the manager ClusterQueue changes when the worker quotas change.

### Non-Goals

- Changes to how the provider finds the related worker ClusterQueues and sums their quotas.
  This works the same way as in Alpha1.
- Changes to the `CapacityProvider` and DQO APIs from [KEP-12382].
- Changes to Kueue on the worker clusters.
  The provider only reads the worker ClusterQueues and LocalQueues, so the workers do not need an upgrade or a new feature gate.

## Proposal

Kueue adds a built-in MultiKueue capacity provider.
It works in three steps:

1. The provider finds the related worker ClusterQueues of a manager ClusterQueue and sums their quotas, as in Alpha1.
2. The provider writes the sum to the `CapacityProvider` status.
3. DQO reads the `CapacityProvider` and writes the manager quota to `ClusterQueue.status.effectiveQuotas`.

The admin selects the manager ClusterQueue in a new `MultiKueueCapacityProviderConfig` object.
The `CapacityProvider` refers to this object in `spec.parameters`.

[Upgrade from KEP-9988 Alpha1](#upgrade-from-kep-9988-alpha1) describes how to move existing Alpha1 setups to the provider.

### User Stories

#### Story 1: Automatically sync the manager ClusterQueue quota with the worker ClusterQueues

As a batch admin using MultiKueue, I want the quota of a manager ClusterQueue to automatically follow the sum of the quotas of its related worker ClusterQueues, so that I do not have to update the manager ClusterQueue manually whenever worker quotas change or worker clusters are added or removed.

This also covers two related sub-scenarios:

- Inspecting the total worker capacity for a manager ClusterQueue on the manager cluster without connecting to the worker clusters ([KEP-9988 Story 4]), via `CapacityProvider.status.capacity`.
- Scaling the discovered worker capacity with `effectiveCapacityMultiplier` (for example, overbooking manager quota so large workloads do not block other feasible workloads; see [KEP-12382 Story 3]).

#### Story 2: Kueue and a GitOps tool overwrite the manager ClusterQueue quota

As a batch admin, I manage ClusterQueues with a GitOps tool.
With Alpha1, Kueue writes the sum of the worker quotas to the manager `ClusterQueue.spec`, and my tool reverts this change.
I want the manager quota to change when the worker quotas change, and Kueue must not change `spec`.

[KEP-9988 Story 4]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/9988-multikueue-manager-quota-automation#story-4
[KEP-12382 Story 3]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/12382-dynamic-quota-orchestration#story-3-adjusting-the-discovered-capacity-before-distributing-as-quota

### Notes/Constraints/Caveats

#### Alpha scope

In Alpha, the provider keeps the scope of Alpha1:

- It covers one manager ClusterQueue.
  The API uses a subtree reference, but validation accepts only `kind: ClusterQueue`.
  Support for Cohorts needs a design for discovery and aggregation, and is re-evaluated before Beta.
- It sums the quotas of all worker flavors into one manager flavor ([#11862] tracks more flavors).
  The need for more than one manager flavor is re-evaluated before Beta.
- Kueue does not prevent other CapacityProviders for the same manager ClusterQueue.
  If the manager ClusterQueue has exactly one flavor, DQO adds the capacity of the other providers to this flavor.

[#11862]: https://github.com/kubernetes-sigs/kueue/issues/11862

#### Relation to KEP-9988 (MultiKueue Manager Quota Automation)

This KEP replaces [KEP-9988] and defines the Alpha2, Beta and Stable stages of the feature.
KEP-9988 planned Alpha2 as an update to KEP-9988 itself (see [Move the aggregated quota out of ClusterQueueSpec]), but KEP-9988 is already large, so this KEP defines it instead.
This KEP still uses the KEP-9988 definitions of [related worker ClusterQueues] and [Treatment of ResourceFlavors].
KEP-9988 links to this KEP.

[Move the aggregated quota out of ClusterQueueSpec]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/9988-multikueue-manager-quota-automation#move-the-aggregated-quota-out-of-clusterqueuespec

#### Relation to KEP-12382 (Dynamic Quota Orchestration)

KEP-12382 says that an update to KEP-9988 defines the MultiKueue API and the migration.
This KEP defines them instead.
KEP-12382 links to this KEP.

The MultiKueue capacity provider only reports the sum of the worker quotas.
How DQO distributes this sum to the manager ClusterQueue, including when the manager ClusterQueue has `nominalQuota: 0`, is defined in [Proportional distribution and rounding] in KEP-12382 and is outside the scope of the provider.

[Proportional distribution and rounding]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/12382-dynamic-quota-orchestration#proportional-distribution-and-rounding

### Risks and Mitigations

#### The CapacityProvider and the DQO can target different ClusterQueues

The `MultiKueueCapacityProviderConfig` selects the manager ClusterQueue.
The DQO selects the ClusterQueue that gets the quota.
If the two do not match, a ClusterQueue gets the worker quota sum of a different ClusterQueue.

**Mitigation:**

- In Alpha, the scope is a single ClusterQueue, so the admin only needs to set the same ClusterQueue name in `MultiKueueCapacityProviderConfig` and `DynamicQuotaOrchestrator`.
- The documentation provides a complete configuration example and troubleshooting steps to verify that both objects reference the same manager ClusterQueue (for example, by checking `ClusterQueue.status.effectiveQuotas.orchestratorRef`).
- The documentation also shows how to check the discovered capacity before quota distribution starts: create the DQO without `spec.capacityDistribution` ([DQO Discovery-only mode]) and check its `status.effectiveCapacity`.

[DQO Discovery-only mode]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/12382-dynamic-quota-orchestration#dqo-discovery-only-mode

#### Disconnected worker clusters affect the manager quota

The provider sums only the quotas of connected worker clusters:
- When **some** worker clusters are disconnected, the provider counts only the connected workers (as in MultiKueue Manager Quota Automation, [KEP-9988]), which reduces the manager quota until the worker reconnects. This only delays admitting new workloads and does not evict already-admitted workloads.
- When **all** worker clusters are disconnected, the provider sets `CapacitySynchronized` to `False` with reason `SourceUnavailable`, and DQO does not update `ClusterQueue.status.effectiveQuotas` (see [CapacitySynchronized condition](#capacitysynchronized-condition)). The MultiKueue `AdmissionCheck` also becomes inactive, which stops the manager ClusterQueue from admitting new workloads.

**Mitigation:**

- The provider lists disconnected workers in the `CapacitySynchronized` condition message, and admins can monitor worker availability through the `MultiKueueCluster` `Active` condition.
- When all worker clusters are disconnected, the manager ClusterQueue becomes inactive, and the `kueue_cluster_queue_status` metric reports it as `pending`, so admins can alert on it.
- The documentation describes the effects of partial and total worker disconnection and how to monitor them.

#### Alpha1 users lose quota automation after the upgrade

After the upgrade, Kueue removes the `MultiKueueManagerQuotaAutomation` feature gate and ignores `MultiKueueConfig.spec.quotaManagement`.
If `MultiKueueManagerQuotaAutomation` remains in the Kueue configuration, the controller manager fails to start until the gate is removed.
Once the gate is removed, the manager `ClusterQueue.spec` keeps the last sum that Alpha1 wrote, and the quota no longer changes until the admin migrates to the provider and DQO (see [Upgrade from KEP-9988 Alpha1](#upgrade-from-kep-9988-alpha1)).
Old `MultiKueueManagerQuotaAutomation` conditions stay on the manager ClusterQueues, but Kueue no longer updates them.

**Mitigation:**

- Publish the migration guide with Alpha and include an **ACTION REQUIRED** release note.
  The release note says that Kueue removes the `MultiKueueManagerQuotaAutomation` feature gate and the `multikueue_cluster_status` metric, ignores `MultiKueueConfig.spec.quotaManagement`, and no longer updates the `MultiKueueManagerQuotaAutomation` condition, and it lists the migration steps.
- Document how to verify that the replacement provider and DQO are updating the manager's effective quota (by checking `CapacitySynchronized=True` on the `CapacityProvider`, `Distributed=True` on the `DynamicQuotaOrchestrator`, and `ClusterQueue.status.effectiveQuotas`).

[Account for temporarily unavailable workers]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/9988-multikueue-manager-quota-automation#account-for-temporarily-unavailable-workers

## Design Details

### API

Kueue adds the cluster-scoped `MultiKueueCapacityProviderConfig` API in `kueue.x-k8s.io/v1alpha1`.

```go
// MultiKueueCapacityProviderConfig is the configuration of the MultiKueue CapacityProvider.
// The name has the same constraints as CapacityProviderParametersReference.name
// (at most 63 characters, no dots), so that a CapacityProvider can refer to it in spec.parameters.
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 63",message="name must be no more than 63 characters"
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$')",message="name must not contain dots"
type MultiKueueCapacityProviderConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the configuration of the MultiKueue CapacityProvider.
	// +required
	Spec MultiKueueCapacityProviderConfigSpec `json:"spec,omitzero"`
}

type MultiKueueCapacityProviderConfigSpec struct {
	// managerSubtreeRootRef selects the manager ClusterQueue
	// whose related worker ClusterQueues the provider sums.
	// Only kind ClusterQueue is supported.
	// +required
	// +kubebuilder:validation:XValidation:rule="self.kind == 'ClusterQueue'",message="only the ClusterQueue kind is allowed for managerSubtreeRootRef"
	ManagerSubtreeRootRef CapacityDistributionSubtreeRootRef `json:"managerSubtreeRootRef,omitzero"`
}
```

`managerSubtreeRootRef` uses the same type as the DQO [`subtreeRootQuotaRef`].
With this type, Cohort support does not need a new field, but it needs a new discovery and aggregation mechanism.

The MultiKueue CapacityProvider uses these fields:

- `controllerName` is `kueue.x-k8s.io/multikueue`.
- `parameters` refers to a `MultiKueueCapacityProviderConfig`.
- `orchestratedFlavors` has exactly one flavor, which is the flavor of the manager ClusterQueue.

The provider sums the quotas of all worker flavors into this one flavor, as in Alpha1.
Separate totals for each flavor would be wrong (see [Treatment of ResourceFlavors] in [KEP-9988]).

Example:

```yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: MultiKueueCapacityProviderConfig
metadata:
  name: team-a
spec:
  managerSubtreeRootRef:
    kind: ClusterQueue
    name: team-a
---
apiVersion: kueue.x-k8s.io/v1alpha1
kind: CapacityProvider
metadata:
  name: team-a-workers
spec:
  controllerName: kueue.x-k8s.io/multikueue
  orchestratedFlavors:
  - name: multikueue-flavor
  parameters:
    apiGroup: kueue.x-k8s.io
    kind: MultiKueueCapacityProviderConfig
    name: team-a
status:
  capacity:
    flavors:
    - name: multikueue-flavor # The sum of the related worker ClusterQueues
      resources:
        cpu: "21"
        memory: 84Gi
        nvidia.com/gpu: "12"
---
apiVersion: kueue.x-k8s.io/v1alpha1
kind: DynamicQuotaOrchestrator
metadata:
  name: team-a
spec:
  capacityDiscovery:
    providers:
    - name: team-a-workers
  capacityDistribution:
    subtreeRootQuotaRef:
      kind: ClusterQueue
      name: team-a # The same ClusterQueue as in managerSubtreeRootRef
```

[`subtreeRootQuotaRef`]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/12382-dynamic-quota-orchestration#dynamicquotaorchestrator
[Treatment of ResourceFlavors]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/9988-multikueue-manager-quota-automation#treatment-of-resourceflavors

### Validation

The API server validates `MultiKueueCapacityProviderConfig` with these rules:

- `metadata.name` has at most 63 characters and no dots, matching `CapacityProviderParametersReference.name` from [KEP-12382].
- `spec.managerSubtreeRootRef` is required and has no default. The API server rejects an object without it.
- `spec.managerSubtreeRootRef.kind` must be `ClusterQueue`. The shared `CapacityDistributionSubtreeRootRef` type also allows `Cohort`, so the CEL rule on `managerSubtreeRootRef` rejects `Cohort` in Alpha.
- `spec.managerSubtreeRootRef.name` follows the `CapacityDistributionSubtreeRootRef` validation rules from [KEP-12382] (1 to 253 characters).
- `spec.managerSubtreeRootRef` is mutable. When it changes, the provider recomputes `CapacityProvider.status.capacity` for the new manager ClusterQueue.

### Finding the worker quotas

The provider finds the [related worker ClusterQueues] and sums their quotas, as in Alpha1.
[MultiKueue ClusterQueue Reconciler] in [KEP-9988] describes this process.
The provider writes the sum to `CapacityProvider.status.capacity`, not to the manager ClusterQueue `spec`.
The provider also updates the sum when the `MultiKueueCapacityProviderConfig` or the `CapacityProvider` changes.

The provider reads the quota of each worker ClusterQueue like this:

- It uses `status.effectiveQuotas` if it is set.
- Otherwise, it uses `spec.resourceGroups`.

This is the [Source of Truth for Quotas] rule from [KEP-12382], but the provider does not check the feature gates of the worker.
This has one known problem:

- If a worker turns off the `DynamicQuotaOrchestration` feature gate, the worker ignores its old `status.effectiveQuotas`, but the provider still uses it.
- Kueue does not remove the old `status.effectiveQuotas` (see [Stale Effective Quota] in [KEP-12382]), so the admin must remove it.

[MultiKueue ClusterQueue Reconciler]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/9988-multikueue-manager-quota-automation#multikueue-clusterqueue-reconciler
[Source of Truth for Quotas]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/12382-dynamic-quota-orchestration#source-of-truth-for-quotas
[Automatic expiration of effective quota]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/12382-dynamic-quota-orchestration#automatic-expiration-of-effective-quota

### CapacitySynchronized condition

The provider sets the `CapacitySynchronized` condition from [KEP-12382] with these reasons:

| Status | Reason | Description |
| --- | --- | --- |
| True | Synchronized | At least one worker cluster of the manager ClusterQueue is connected. `status.capacity` holds the sum of the connected worker clusters. The message lists the disconnected worker clusters. |
| False | Misconfigured | One of these occurs:<br>• The `spec.parameters` reference or `spec.orchestratedFlavors` configuration is invalid.<br>• The `MultiKueueCapacityProviderConfig`, the manager ClusterQueue, or its MultiKueue AdmissionCheck or `MultiKueueConfig` does not exist.<br>• The manager ClusterQueue does not meet the [KEP-9988 constraint]: exactly one ResourceFlavor, which is the flavor in `spec.orchestratedFlavors` and covers all resources of the related worker ClusterQueues. |
| False | SourceUnavailable | No worker cluster of the manager ClusterQueue is connected. |

If the condition is not True, DQO does not distribute the capacity, and the manager `status.effectiveQuotas` keeps its last value.
This is the DQO behavior from [KEP-12382] ([Stale Effective Quota]).

[Stale Effective Quota]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/12382-dynamic-quota-orchestration#stale-effective-quota
[KEP-9988 constraint]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/9988-multikueue-manager-quota-automation#alpha1-version

### Upgrade from KEP-9988 Alpha1

The alpha feature gate `MultiKueueManagerQuotaAutomation` controls Alpha1, so Kueue can remove the Alpha1 code immediately.

Two Alpha1 items are in the v1beta2 API:

- the `quotaManagement` field in `MultiKueueConfig`;
- the `MultiKueueManagerQuotaAutomation` condition on ClusterQueue.

Because `quotaManagement` is part of the `v1beta2` CRD schema, Kueue cannot remove it before the next API version.
For this reason, Kueue keeps `quotaManagement`, marks it as deprecated, and ignores it.

Kueue keeps the field, its type and its enum in `apis/kueue/v1beta2/multikueue_types.go`, and only updates the API comments:

```go
type MultiKueueConfigSpec struct {
	// ...

	// quotaManagement is deprecated and ignored since v0.21.
	// To sum the quotas of the worker ClusterQueues into the manager ClusterQueue,
	// use a CapacityProvider with a MultiKueueCapacityProviderConfig.
	// The field is removed in the next API version.
	//
	// Deprecated: Kueue ignores this field.
	// +optional
	QuotaManagement *MultiKueueConfigQuotaManagementMode `json:"quotaManagement,omitempty"`
}

// MultiKueueConfigQuotaManagementMode is deprecated and ignored since v0.21.
// Supported modes:
// - `Manual`
// - `Automated`
// +kubebuilder:validation:Enum=Manual;Automated
type MultiKueueConfigQuotaManagementMode string

const (
	QuotaManagementManual    MultiKueueConfigQuotaManagementMode = "Manual"
	QuotaManagementAutomated MultiKueueConfigQuotaManagementMode = "Automated"
)
```

Kueue keeps the enum, so the API server accepts existing `MultiKueueConfig` objects with `quotaManagement: Automated`.

Kueue removes the `MultiKueueManagerQuotaAutomation` condition type (which is not part of the CRD schema) and the `MultiKueueManagerQuotaAutomation` alpha feature gate.
Kueue also removes the `multikueue_cluster_status` metric, which only the Alpha1 reconciler reports today (when `MultiKueueManagerQuotaAutomation` is enabled).
The metric tracks worker cluster connectivity rather than quota automation, so the MultiKueue capacity provider does not report it; [#17892] tracks reporting it independently of quota automation.

[#17892]: https://github.com/kubernetes-sigs/kueue/issues/17892

Kueue adds the alpha feature gate `DQOMultiKueueCapacityProvider`, disabled by default.
This gate depends on the `DynamicQuotaOrchestration` and `MultiKueue` feature gates.
With a dedicated gate, users can use DQO without the MultiKueue provider, and this KEP does not depend on the DQO graduation schedule.
When `DQOMultiKueueCapacityProvider` is disabled, Kueue does not run the provider controller and does not update `CapacityProvider.status`.
The `CapacityProvider` keeps its last `status.capacity` and `CapacitySynchronized` condition.
DQO cannot see that the gate is disabled, so it keeps using the last capacity, and no condition shows that this capacity is old.

For this reason, when the admin disables the gate, the admin also does these steps, so that the scheduler uses `spec.resourceGroups` as the quota again ([Source of Truth for Quotas]):

1. Unpin the DQO by removing its `spec.capacityDistribution` ([DQO Discovery-only mode]).
   DQO then stops writing `status.effectiveQuotas`.
2. Remove `status.effectiveQuotas` from the manager ClusterQueue.
   Do this after step 1, because DQO writes `status.effectiveQuotas` again while it still distributes capacity.

Until step 2, the manager `ClusterQueue.status.effectiveQuotas` keeps its last value, as described in [Stale Effective Quota] in [KEP-12382].
When the admin enables the gate again and adds `spec.capacityDistribution` back, the provider updates `CapacityProvider.status`, and DQO distributes the new capacity.
The documentation and the release notes describe these steps.

[Source of Truth for Quotas]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/12382-dynamic-quota-orchestration#source-of-truth-for-quotas

When the admin updates Kueue to v0.21 or later, the admin does these steps to keep the quota automation:

1. Remove the `MultiKueueManagerQuotaAutomation` feature gate from the Kueue configuration, and enable the `DynamicQuotaOrchestration` and `DQOMultiKueueCapacityProvider` feature gates.
2. For each manager ClusterQueue that uses Alpha1, create the three objects from the [API](#api) example.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

We adapt the KEP-9988 Alpha1 tests that find and sum the worker quotas to the new destination (`CapacityProvider.status.capacity`).
The KEP-12382 tests cover the DQO distribution.

#### Unit tests

- The provider sums the quotas of the related worker ClusterQueues into one flavor.
- The provider uses the worker `status.effectiveQuotas` when it is present, otherwise `spec.resourceGroups`.
- The provider sets each `CapacitySynchronized` reason from the table in [CapacitySynchronized condition](#capacitysynchronized-condition).

#### Integration tests

- A change of a worker LocalQueue or ClusterQueue updates `CapacityProvider.status.capacity`.
- The provider, the DQO and the manager `ClusterQueue.status.effectiveQuotas` work together.
- The API rejects a `MultiKueueCapacityProviderConfig` with `managerSubtreeRootRef.kind: Cohort`.
- Kueue ignores `MultiKueueConfig.spec.quotaManagement`.
- When the `DQOMultiKueueCapacityProvider` feature gate is disabled, a change of a worker quota does not update `CapacityProvider.status`.
- When the feature gate is enabled again, the provider updates `CapacityProvider.status`.

#### e2e tests

- A change of a worker quota updates the manager `ClusterQueue.status.effectiveQuotas`, and the manager admits a workload that fits the new quota.

### Graduation Criteria

- **Alpha:**
  - The feature is implemented behind the `DQOMultiKueueCapacityProvider` feature gate, disabled by default.
    The gate depends on the `DynamicQuotaOrchestration` and `MultiKueue` feature gates.
  - The KEP-9988 Alpha1 code is removed (see [Upgrade from KEP-9988 Alpha1](#upgrade-from-kep-9988-alpha1)).
  - The unit, integration and e2e tests from the [Test Plan](#test-plan) are implemented.
  - The migration guide and an **ACTION REQUIRED** release note are published (see [Risks and Mitigations](#risks-and-mitigations)).

- **Beta:**
  - The feature gate is enabled by default.
    This step requires that the `DynamicQuotaOrchestration` feature gate is enabled by default.
  - Re-evaluate support for scraping capacity from Cohorts, based on user feedback.
  - Re-evaluate the need for mapping worker quotas into more than one manager flavor, based on user feedback.
  - Re-evaluate whether additional validation or observability (such as metrics or conditions) is needed to detect misconfigurations early and to alert on them, based on user feedback.
  - Re-evaluate the handling of temporarily unavailable workers ([Account for temporarily unavailable workers]), based on user feedback.
    For example, the provider can keep the last known quota of a disconnected worker until `workerLostTimeout` expires, so that a short disconnection does not decrease the manager quota.
  - Re-evaluate using `spec.resourceGroups` when a worker ClusterQueue marks its `status.effectiveQuotas` as stale ([Automatic expiration of effective quota] in [KEP-12382]).
  - Re-evaluate marking the MultiKueue `CapacityProvider` as not synchronized when the feature gate is disabled, based on user feedback.
  - Re-evaluate dropping the Kueue controller manager's RBAC permission to update `ClusterQueue` objects, if no other feature needs it.
  - User documentation is published on the website, and the release notes say how to opt out.
  - The feature has been successfully used in a production environment.

- **Stable:**
  - The feature (offered functionality, API surface and implementation) has stabilized, without raising concerns.
  - The feature gate is locked to `true` (and then cleaned up in a later release).

## Implementation History

- 2026 OCT 19: Initial KEP draft.

## Drawbacks

This KEP moves the MultiKueue quota automation to DQO, which [KEP-12382] already defines, and removes [KEP-9988 Drawbacks] item 1 (Kueue changes the ClusterQueue `spec`).
This KEP has two costs.

The `quotaManagement` field in `MultiKueueConfig` is part of the `v1beta2` API, but it controls only an alpha feature.
From v0.21, Kueue ignores this field, without a deprecation period.
Kueue cannot remove the field before the next API version, so the deprecated field stays in the `v1beta2` API (see [Upgrade from KEP-9988 Alpha1](#upgrade-from-kep-9988-alpha1)).

The setup is more complex than in Alpha1.
In Alpha1, the admin enables one feature gate and sets one field in the `MultiKueueConfig`.
With this KEP, the admin enables two feature gates and creates three objects: a `MultiKueueCapacityProviderConfig`, a `CapacityProvider` and a `DynamicQuotaOrchestrator`.

## Alternatives

### `spec.parameters` refers to a MultiKueueConfig

The CapacityProvider refers to an existing `MultiKueueConfig` instead of a new configuration object.

**Reasons for discarding**

- Many manager ClusterQueues can share one `MultiKueueConfig`, so a `MultiKueueConfig` does not select a single manager ClusterQueue.
  The provider then sums the worker quotas of all these ClusterQueues, and the DQO gets too much quota.

### `spec.parameters` refers to the manager ClusterQueue directly

The CapacityProvider refers to the manager ClusterQueue instead of a new configuration object.

**Reasons for discarding**

- There is no configuration object, so there is no place for future settings.
  [KEP-12382] intends `spec.parameters` to refer to a configuration object of the provider.
- `spec.parameters.name` allows a maximum of 63 characters and no dots.
  ClusterQueue and Cohort names allow 253 characters with dots.

### The provider uses the DQO feature gate

The `DynamicQuotaOrchestration` feature gate enables the MultiKueue CapacityProvider, without a dedicated feature gate.

**Reasons for discarding**

- Users cannot use DQO with other providers and keep the MultiKueue provider disabled.
- This KEP must then graduate together with [KEP-12382].

