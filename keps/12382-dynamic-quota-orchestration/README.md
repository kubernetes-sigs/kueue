# KEP-12382: Dynamic Quota Orchestration

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Dynamically adjust quota based on built-in plugins](#story-1-dynamically-adjust-quota-based-on-built-in-plugins)
    - [Story 2: Capacity discovered by an external controller](#story-2-capacity-discovered-by-an-external-controller)
    - [Story 3: Adjusting the discovered capacity before distributing as quota](#story-3-adjusting-the-discovered-capacity-before-distributing-as-quota)
  - [Notes](#notes)
    - [Relation to KEP-9988](#relation-to-kep-9988)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [User confusion about effective quota](#user-confusion-about-effective-quota)
    - [Overcommitted quota after a capacity reduction](#overcommitted-quota-after-a-capacity-reduction)
- [Design Details](#design-details)
  - [Overview](#overview)
  - [Phase 1: Capacity Discovery](#phase-1-capacity-discovery)
  - [Phase 2: Quota Distribution](#phase-2-quota-distribution)
  - [APIs](#apis)
    - [ClusterQueue and Cohort effective quota](#clusterqueue-and-cohort-effective-quota)
    - [DynamicQuotaOrchestrator](#dynamicquotaorchestrator)
    - [DynamicQuotaOrchestrator status](#dynamicquotaorchestrator-status)
      - [Condition <code>EffectiveCapacityComputed</code> (Phase 1: Capacity Discovery)](#condition-effectivecapacitycomputed-phase-1-capacity-discovery)
      - [Condition <code>Distributed</code> (Phase 2: Quota Distribution)](#condition-distributed-phase-2-quota-distribution)
    - [CapacityProvider](#capacityprovider)
      - [Condition <code>CapacitySynchronized</code>](#condition-capacitysynchronized)
  - [Enablement](#enablement)
  - [Source of Truth for Quotas](#source-of-truth-for-quotas)
  - [Proportional distribution and rounding](#proportional-distribution-and-rounding)
  - [Effective quota construction](#effective-quota-construction)
  - [Resource transformations and DRA resource mappings](#resource-transformations-and-dra-resource-mappings)
  - [Stale Effective Quota](#stale-effective-quota)
  - [DQO Discovery-only mode](#dqo-discovery-only-mode)
  - [DQO Distribution mode](#dqo-distribution-mode)
  - [Soft validation of overlapping orchestrators](#soft-validation-of-overlapping-orchestrators)
  - [Validation for aggregated capacity](#validation-for-aggregated-capacity)
  - [Examples](#examples)
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
  - [Internal quota transformation pipeline](#internal-quota-transformation-pipeline)
  - [Effective quota override in spec](#effective-quota-override-in-spec)
  - [Automatic expiration of effective quota](#automatic-expiration-of-effective-quota)
  - [Store all distributed quota in DQO status](#store-all-distributed-quota-in-dqo-status)
  - [Sparse effective quota overrides](#sparse-effective-quota-overrides)
  - [One CRD per capacity provider](#one-crd-per-capacity-provider)
  - [Typed union of built-in providers](#typed-union-of-built-in-providers)
  - [CapacityProvider referencing namespaced config](#capacityprovider-referencing-namespaced-config)
  - [Nested ResourceCapacity for CapacitySnapshot](#nested-resourcecapacity-for-capacitysnapshot)
<!-- /toc -->

## Summary

This KEP introduces Dynamic Quota Orchestration (DQO), a mechanism for deriving
Kueue quota from capacity reported by one or more providers and optionally
distributing that capacity across a ClusterQueue/Cohort subtree.

The mechanism has two phases:

1. Discovery: Capacity providers publish normalized capacity through `CapacityProvider`
   objects. The `DynamicQuotaOrchestrator` aggregates capacity from the referenced
   providers. While aggregating the capacity from each proivder can be transformed
   by a fixed multiplier to indicate the portion of the capacity used for the actual quota distribution (phase 2).
2. Distribution: DQO proportionally distributes the
   aggregated capacity and writes the resulting effective quota to the status of
   each managed ClusterQueue and Cohort.

The ClusterQueue and Cohort specs remain the administrator-owned source of the
quota structure and distribution proportions. The scheduler uses
`status.effectiveQuotas`, when present, instead of `spec.resourceGroups`.

## Motivation

Kueue quota is currently static and is configured in ClusterQueue and Cohort
specs. Administrators must keep this quota synchronized with infrastructure or
capacity managed by another system.

Quota should adjust to available node capacity. This streamlines adding new
nodes because administrators do not need to update quota manually, and reduces
available quota when nodes go down.

MultiKueue is also expected to dynamically adjust capacity based on aggregated
capacity reported by its worker clusters.

### Goals

- Introduce a pluggable mechanism for dynamically adjusting quota based on the
  currently available capacity.
- Preserve ClusterQueue and Cohort specs as administrator-owned configuration.
- Enable temporary quota overrides for users who reserve autoscaler capacity
  for a time window and want quota to increase automatically during that
  window.

### Non-Goals

- Define detailed APIs for LocalCapacity, MultiKueueCapacity, or other capacity
  provider integrations. These capabilities are left to individual KEPs.

## Proposal

Introduce two cluster-scoped APIs:

- `CapacityProvider`, written by a capacity provider, containing normalized
  capacity in status; and
- `DynamicQuotaOrchestrator`, referencing the capacity providers to aggregate
  the capacity and distribute as quota within the referenced subtree.

Add `status.effectiveQuotas` to ClusterQueue and Cohort. The DQO controller owns
this status when identified by `orchestratorRef`. The scheduler uses the effective
quota structure when it is present.

### User Stories

#### Story 1: Dynamically adjust quota based on built-in plugins

As a cluster administrator, I would like quota to adjust automatically based on
available capacity. For example:

- capacity derived from nodes ([#10270]);
- capacity aggregated from MultiKueue worker clusters ([#9988]); or
- quota overridden during a temporary window backed by an autoscaler
  reservation ([#8654]).

The exact set of built-in capacity plugins and their capabilities is left to
the individual KEPs that introduce them.

#### Story 2: Capacity discovered by an external controller

As a cluster administrator, I would like to write a custom controller that
discovers capacity using vendor-specific APIs and publishes it through a
`CapacityProvider`. For example, the controller may discover and expose
cluster-autoscaler capacity using vendor-specific APIs.

[#10270]: https://github.com/kubernetes-sigs/kueue/issues/10270
[#9988]: https://github.com/kubernetes-sigs/kueue/issues/9988
[#8654]: https://github.com/kubernetes-sigs/kueue/issues/8654

#### Story 3: Adjusting the discovered capacity before distributing as quota

As a cluster administrator I need to adjust the discored capacity
before distributing as quota.

Some of the situations:
- **Operational headroom** (e.g. 0.95 or 950m): when TAS is not used, leaving a small capacity buffer
can reduce the impact of node failures or resource fragmentation.
- **Capacity partitioning** (e.g. 0.5 or 500m, or fractional values like 0.555 or 555m): Allows sharing the discovered node capacity
between two CQs which are orchestrated by different DQO instances.
- **MultiKueue candidate overbooking** (e.g. 3): Setting the value above 1 can
be useful to compensate for the fact that sometimes large workloads holding
manager-side quota, without overbooking, can exhaust the manager-side quota and
prevent other feasible workloads from being considered. This may happen when the
workloads are inadmissible or are very picky about the worker cluster. See also
[this section](https://github.com/kubernetes-sigs/kueue/tree/main/keps/9988-multikueue-manager-quota-automation#potential-reasons-for-increasing-manager-quota) 
in the related "MultiKueue Manager Quota Automation" KEP.

The effectiveCapacityMultiplier parameter provides flexibility in translating observed
capacity into the effective capacity made available by Kueue for quota distribution.

### Notes

#### Relation to KEP-9988

For MultiKueue, DQO builds foundation to decomission the Alpha1 manager-quota controller
described in [KEP-9988](https://github.com/kubernetes-sigs/kueue/tree/main/keps/9988-multikueue-manager-quota-automation).
The mechanism will no longer update ClusterQueue's `.spec.resourceGroups`.

The integration-specific API and migration are defined by an update to KEP-9988
for its Alpha2, see the related discussion [here](https://github.com/kubernetes-sigs/kueue/tree/main/keps/9988-multikueue-manager-quota-automation#move-the-aggregated-quota-out-of-clusterqueuespec).

### Risks and Mitigations

#### User confusion about effective quota

This risk is an intrinsic [drawback](#drawbacks) of the proposal due to separating
administrator-configured intent from controller-computed runtime state. 

This is particularly important because all scheduler decisions (admissions, preemptions,
scaling etc.) will consider the effective quotas, represented in `status.effectiveQuotas`,
instead of `spec.resourceGroups` where admins may be used to look.

**Mitigation tactics**:

- `status.effectiveQuotas` records its `orchestratorRef` field
useful for troubleshooting and observability.
- The new ClusterQueue/Cohort condition `EffectiveCapacityComputed` will increase
  discoverability of the flow.
- The documentation will describe the precedence between `spec.resourceGroups` and
`status.effectiveQuotas` and provide troubleshooting guidance.
- Quota metrics report the values actually used by Kueue components.

#### Overcommitted quota after a capacity reduction

As the capacity may fluctuate the workloads admitted at the peak points
are not actively evicted by Kueue. 

This risk arguably exists even before DQO, for example when:
1. admins adjust quota manually to reflect the capacity reduction.
2. admins re-org the stable capacity, for example introduction of a new team/tenant.
3. admins write their automated scripts for aligning quota with the actual capacity.

**Mitigation tactics**:

- observability: the use of metrics to report the committed quota, and the
  `status.effectiveQuotas` will allow admins to detect the anomalies when the workloads are overcommitted.

## Design Details

### Overview

DQO separates capacity discovery from quota distribution:

1. Providers convert their source-specific view into a common representation
   in `CapacityProvider.status.capacity`.
2. A DQO aggregates contributions from the providers referenced in
   `spec.capacityDiscovery.providers` per `(ResourceFlavor, resource)` pair.
3. If `capacityDistribution` is configured, the DQO computes proportional
   effective quota for all ClusterQueues and Cohorts in the referenced subtree.
4. The scheduler uses effective quota when evaluating workloads.

### Phase 1: Capacity Discovery

Each referenced `CapacityProvider` contributes capacity per
`(ResourceFlavor, resource)` pair. `effectiveCapacityMultiplier` controls how much of
the provider's reported capacity contributes to the aggregated capacity.

The resulting aggregate is exposed in `DynamicQuotaOrchestrator.status.effectiveCapacity`.

### Phase 2: Quota Distribution

When `spec.capacityDistribution` is present, the DQO distributes aggregated
capacity within the subtree rooted at the referenced ClusterQueue or Cohort.

The default policy is proportional distribution. For each
`(ResourceFlavor, resource)` pair, proportions come from the corresponding
quota configured by the administrator in ClusterQueue and Cohort
`spec.resourceGroups`.

The result for each managed object is represented as a complete
`ResourceGroup` list in `status.effectiveQuotas`. Other distribution policies may
be introduced in future KEP updates.

### APIs

First, we propose to extend the ClusterQueue and Cohort APIs with the field
which will allow external controllers to set the dynamic quota assignments,
effectively overriding the quotas set in spec.resourceGroups.

#### ClusterQueue and Cohort effective quota

```go
type ClusterQueueStatus struct {
    // ...

    // effectiveQuotas is used for scheduling instead of spec.resourceGroups when
    // present.
    //
    // This field is alpha-level, and is ignored by Kueue when the DynamicQuotaOrchestration
    // feature gate is disabled.
    //
    // +optional
    EffectiveQuotas *EffectiveQuotaStatus `json:"effectiveQuotas,omitempty"`
}

type CohortStatus struct {
    // ...

    // effectiveQuotas is used for scheduling instead of spec.resourceGroups when
    // present.
    //
    // This field is alpha-level, and is ignored by Kueue when the DynamicQuotaOrchestration
    // feature gate is disabled.
    //
    // +optional
    EffectiveQuotas *EffectiveQuotaStatus `json:"effectiveQuotas,omitempty"`
}

type EffectiveQuotaStatus struct {
    // orchestratorRef identifies the component managing this value.
    //
    // +required
    OrchestratorRef EffectiveQuotaStatusOrchestratorRef `json:"orchestratorRef"`

    // resourceGroups is the effective quota used by the scheduler.
    // An empty list is a valid complete override and does not cause fallback to
    // spec.resourceGroups.
    //
    // +listType=atomic
    // +kubebuilder:validation:MaxItems=16
    ResourceGroups []ResourceGroup `json:"resourceGroups"`
}

type EffectiveQuotaStatusOrchestratorRef struct {
    // apiGroup is the group for the resource representing the manager.
    // +required
    // +kubebuilder:validation:MaxLength=253
    // +kubebuilder:validation:MinLength=1
  	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	  APIGroup string `json:"apiGroup,omitempty"`

    // kind is the type of the manager setting the effective quota.
    // +required
    // +kubebuilder:validation:MaxLength=63
    // +kubebuilder:validation:MinLength=1
    // +kubebuilder:validation:Pattern="^(?i)[a-z]([-a-z0-9]*[a-z0-9])?$"
    Kind string `json:"kind"`

    // name is the name of the manager setting the effective quota.
    // +required
    // +kubebuilder:validation:MinLength=1
    // +kubebuilder:validation:MaxLength=253
    // +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
    Name string `json:"name"`
}
```

#### DynamicQuotaOrchestrator

Next we introduce the main new API and controller responsible for the 
discovery and distribution of the capacity.

```go
type DynamicQuotaOrchestrator struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`

    Spec   DynamicQuotaOrchestratorSpec   `json:"spec,omitempty"`
    Status DynamicQuotaOrchestratorStatus `json:"status,omitempty"`
}

type DynamicQuotaOrchestratorSpec struct {
    // capacityDiscovery specifies capacity aggregation.
    //
    // +required
    CapacityDiscovery CapacityDiscovery `json:"capacityDiscovery"`

    // capacityDistribution specifies how aggregated capacity is distributed.
    // When omitted, the DQO is discovery-only: it reports aggregated capacity
    // but does not write effectiveQuotas status.
    //
    // +optional
    CapacityDistribution *CapacityDistribution `json:"capacityDistribution,omitempty"`
}

type CapacityDiscovery struct {
    // providers lists CapacityProvider objects consumed by this DQO.
    //
    // +listType=map
    // +listMapKey=name
    // +kubebuilder:validation:MinItems=1
    // +kubebuilder:validation:MaxItems=8
    Providers []CapacityDiscoveryProviderContribution `json:"providers"`
}

type CapacityDiscoveryProviderContribution struct {
    // name identifies a CapacityProvider.
    Name CapacityProviderName `json:"name"`

    // effectiveCapacityMultiplier specifies the multiplier applied to the
    // discovered capacity from this provider. It defaults to 1.
    //
    // +optional
    // +kubebuilder:default=1
    // +kubebuilder:validation:XValidation:rule="quantity(self).sign() >= 0",message="effectiveCapacityMultiplier must be non-negative"
    EffectiveCapacityMultiplier *resource.Quantity `json:"effectiveCapacityMultiplier,omitempty"`
}

type CapacityDistribution struct {
    // subtreeRootQuotaRef identifies the root of the quota subtree.
    //
    // +required
    SubtreeRootQuotaRef CapacityDistributionSubtreeRootRef `json:"subtreeRootQuotaRef"`
}

type SubtreeRootRefKind string

const (
    ClusterQueueSubtreeRootRefKind SubtreeRootRefKind = "ClusterQueue"
    CohortSubtreeRootRefKind       SubtreeRootRefKind = "Cohort"
)

type CapacityDistributionSubtreeRootRef struct {
    // kind indicates the kind of the quota node, i.e. ClusterQueue or Cohort.
    // 
    // +required
    // +kubebuilder:validation:Enum=ClusterQueue;Cohort
    Kind SubtreeRootRefKind `json:"kind"`

    // name indicates the name of the quota node, i.e. ClusterQueue or Cohort.
    // +kubebuilder:validation:MinLength=1
    // +kubebuilder:validation:MaxLength=253
    // +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
    Name string             `json:"name"`
}

// +kubebuilder:validation:MinLength=1
// +kubebuilder:validation:MaxLength=253
// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
type CapacityProviderName string
```

#### DynamicQuotaOrchestrator status

```go
type DynamicQuotaOrchestratorStatus struct {
    // effectiveCapacity is the capacity aggregated from the referenced providers.
    //
    // +optional
    EffectiveCapacity *EffectiveCapacity `json:"effectiveCapacity,omitempty"`

    // conditions represents the current state of the DQO.
    //
    // +optional
    // +listType=map
    // +listMapKey=type
    // +patchStrategy=merge
    // +patchMergeKey=type
    // +kubebuilder:validation:MaxItems=16
    Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type EffectiveCapacity struct {
    // flavors contains capacity per flavor and resource.
    //
    // +listType=map
    // +listMapKey=name
    // +kubebuilder:validation:MaxItems=128
    Flavors []EffectiveCapacityFlavor `json:"flavors"`
}

type EffectiveCapacityFlavor struct {
    // name identifies the ResourceFlavor whose capacity is reported.
    //
    // +required
    Name ResourceFlavorReference `json:"name"`

    // resources contains total capacity by resource name.
    //
    // +required
    // +kubebuilder:validation:XValidation:rule="size(self) >= 1 && size(self) <= 64",message="resource capacity must have between 1 and 64 entries"
    // +kubebuilder:validation:XValidation:rule="self.all(r, type(self[r]) == string ? quantity(self[r]).sign() >= 0 : self[r] >= 0)",message="resource capacity must be non-negative"
    Resources corev1.ResourceList `json:"resources"`
}

// DynamicQuotaOrchestrator condition types and reasons
const (
    // DynamicQuotaOrchestratorEffectiveCapacityComputed indicates whether status.effectiveCapacity
    // is successfully aggregated from all referenced CapacityProviders.
    DynamicQuotaOrchestratorEffectiveCapacityComputed string = "EffectiveCapacityComputed"

    // DynamicQuotaOrchestratorReasonComputed indicates that effective capacity was aggregated successfully.
    DynamicQuotaOrchestratorReasonComputed string = "Computed"

    // DynamicQuotaOrchestratorReasonProviderNotReady indicates a referenced CapacityProvider does not have CapacitySynchronized=True.
    DynamicQuotaOrchestratorReasonProviderNotReady string = "ProviderNotReady"

    // DynamicQuotaOrchestratorReasonAggregationFailed indicates aggregation failed (e.g. multiplier application error, math overflow, or capacity limits exceeded).
    DynamicQuotaOrchestratorReasonAggregationFailed string = "AggregationFailed"

    // DynamicQuotaOrchestratorDistributed indicates whether status.effectiveQuotas has been
    // successfully computed and distributed across the referenced subtree.
    // This condition is only present when spec.capacityDistribution is configured.
    DynamicQuotaOrchestratorDistributed string = "Distributed"

    // DynamicQuotaOrchestratorReasonQuotasDistributed indicates all effective quotas were successfully applied.
    DynamicQuotaOrchestratorReasonQuotasDistributed string = "QuotasDistributed"

    // DynamicQuotaOrchestratorReasonEffectiveCapacityNotComputed indicates distribution was skipped because Phase 1 discovery is not ready.
    DynamicQuotaOrchestratorReasonEffectiveCapacityNotComputed string = "EffectiveCapacityNotComputed"

    // DynamicQuotaOrchestratorReasonConflictingDynamicQuotaOrchestrator indicates this DQO was deactivated by soft validation.
    DynamicQuotaOrchestratorReasonConflictingDynamicQuotaOrchestrator string = "ConflictingDynamicQuotaOrchestrator"

    // DynamicQuotaOrchestratorReasonEffectiveQuotasConflict indicates another controller owns status.effectiveQuotas on a target queue.
    DynamicQuotaOrchestratorReasonEffectiveQuotasConflict string = "EffectiveQuotasConflict"

    // DynamicQuotaOrchestratorReasonMisconfigured indicates a configuration or reference error in spec.
    DynamicQuotaOrchestratorReasonMisconfigured string = "Misconfigured"
)
```

The DQO reports conditions reflecting its two-phase lifecycle:

##### Condition `EffectiveCapacityComputed` (Phase 1: Capacity Discovery)

Tracks whether aggregated capacity was successfully computed and written to `status.effectiveCapacity`.

| Status | Reason | Description |
|---|---|---|
| `True` | `Computed` | Aggregated capacity calculated and written to `status.effectiveCapacity`. |
| `False` | `ProviderNotReady` | One or more referenced `CapacityProvider` objects do not report `CapacitySynchronized=True`. |
| `False` | `AggregationFailed` | Aggregated capacity calculation failed (e.g. multiplier application error, arithmetic overflow, or exceeded flavor/resource limits). |
| `False` | `Misconfigured` | One or more `CapacityProvider` objects referenced in `spec.capacityDiscovery.providers` do not exist. |

##### Condition `Distributed` (Phase 2: Quota Distribution)

Tracks whether effective quota was distributed across the subtree. **This condition is omitted when `spec.capacityDistribution` is omitted (Discovery-only mode).**

| Status | Reason | Description |
|---|---|---|
| `True` | `QuotasDistributed` | Proportional distribution calculated and `status.effectiveQuotas` updated across all subtree ClusterQueues and Cohorts. |
| `False` | `EffectiveCapacityNotComputed` | Distribution skipped because Phase 1 discovery is not ready (`EffectiveCapacityComputed != True`). |
| `False` | `ConflictingDynamicQuotaOrchestrator` | Deactivated by soft validation because an ancestor DQO has priority. |
| `False` | `EffectiveQuotasConflict` | A target ClusterQueue or Cohort already has `status.effectiveQuotas.orchestratorRef` owned by a different orchestrator. |
| `False` | `Misconfigured` | The `ClusterQueue` or `Cohort` specified in `spec.capacityDistribution.subtreeRootQuotaRef` does not exist. |

This set of condition types and reasons is provisional for Alpha and will be re-evaluated in Beta based on user and operational feedback.

#### CapacityProvider

`CapacityProvider` is the common status API for all capacity providers. A
provider may use its own configuration CRD and reference it through
`spec.parameters`; DQO does not read or validate that object.

```go
type CapacityProvider struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`

    Spec   CapacityProviderSpec   `json:"spec,omitempty"`
    Status CapacityProviderStatus `json:"status,omitempty"`
}

type CapacityProviderSpec struct {
    // orchestratedFlavors identifies the ResourceFlavors for which this provider may
    // publish capacity. DQO ignores entries in status.capacity.flavors whose
    // names are not listed here.
    //
    // +listType=map
    // +listMapKey=name
    // +kubebuilder:validation:MinItems=1
    // +kubebuilder:validation:MaxItems=64
    OrchestratedFlavors []CapacityProviderOrchestratedFlavor `json:"orchestratedFlavors"`

    // controllerName identifies the controller publishing capacity. 
    // This field is immutable.
    //
    // +required
    // +kubebuilder:validation:XValidation:rule="self == oldSelf", message="field is immutable"
    ControllerName CapacityProviderControllerName `json:"controllerName"`

    // parameters optionally references implementation-specific configuration.
    // DQO does not read or validate the referenced object.
    //
    // +optional
    Parameters *CapacityProviderParametersReference `json:"parameters,omitempty"`
}

// CapacityProviderOrchestratedFlavor identifies a flavor managed by a provider.
// The container allows the mapping to be extended in a future API version.
type CapacityProviderOrchestratedFlavor struct {
    // name identifies the ResourceFlavor managed by this provider.
    //
    // +required
    Name ResourceFlavorReference `json:"name"`
}

type CapacityProviderParametersReference struct {
	// apiGroup is the group for the resource being referenced.
	// +required
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	APIGroup string `json:"apiGroup,omitempty"`
	// kind is the type of the resource being referenced.
	// +required
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern="^(?i)[a-z]([-a-z0-9]*[a-z0-9])?$"
	Kind string `json:"kind,omitempty"`
	// name is the name of the resource being referenced.
	// +required
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
	Name string `json:"name,omitempty"`
}

type CapacityProviderStatus struct {
    // capacity is the normalized capacity published by the provider.
    //
    // +optional
    Capacity *CapacityProviderNormalizedCapacity `json:"capacity,omitempty"`

    // conditions represents the current state of this provider.
    //
    // +optional
    // +listType=map
    // +listMapKey=type
    // +patchStrategy=merge
    // +patchMergeKey=type
    // +kubebuilder:validation:MaxItems=16
    Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type CapacityProviderNormalizedCapacity struct {
    // flavors contains capacity per flavor and resource.
    //
    // +listType=map
    // +listMapKey=name
    // +kubebuilder:validation:MaxItems=64
    Flavors []CapacityProviderNormalizedCapacityFlavor `json:"flavors"`
}

type CapacityProviderNormalizedCapacityFlavor struct {
    // name identifies the ResourceFlavor whose capacity is reported.
    //
    // +required
    Name ResourceFlavorReference `json:"name"`

    // resources contains total capacity by resource name.
    //
    // +required
    // +kubebuilder:validation:XValidation:rule="size(self) >= 1 && size(self) <= 64",message="resource capacity must have between 1 and 64 entries"
    // +kubebuilder:validation:XValidation:rule="self.all(r, type(self[r]) == string ? quantity(self[r]).sign() >= 0 : self[r] >= 0)",message="resource capacity must be non-negative"
    Resources corev1.ResourceList `json:"resources"`
}

// +kubebuilder:validation:MinLength=1
// +kubebuilder:validation:MaxLength=253
type CapacityProviderControllerName string

// CapacityProvider condition types and reasons
const (
    // CapacityProviderCapacitySynchronized indicates whether status.capacity is synchronized
    // with observations from the underlying capacity source.
    CapacityProviderCapacitySynchronized string = "CapacitySynchronized"

    // CapacityProviderReasonSynchronized indicates that capacity was successfully observed and published.
    CapacityProviderReasonSynchronized string = "Synchronized"

    // CapacityProviderReasonSourceUnavailable indicates that the controller cannot reach the capacity source.
    CapacityProviderReasonSourceUnavailable string = "SourceUnavailable"

    // CapacityProviderReasonInvalidCapacity indicates that observed capacity contained negative or corrupt quantities.
    CapacityProviderReasonInvalidCapacity string = "InvalidCapacity"

    // CapacityProviderReasonMisconfigured indicates that spec parameters or flavor mappings are invalid.
    CapacityProviderReasonMisconfigured string = "Misconfigured"
)
```

##### Condition `CapacitySynchronized`

Tracks whether `status.capacity` is freshly synchronized with the observed capacity source:

| Status | Reason | Description |
|---|---|---|
| `True` | `Synchronized` | `status.capacity` is freshly synchronized with the observed capacity source. |
| `False` | `SourceUnavailable` | The provider controller cannot query or reach the backend source (e.g., node API, cloud endpoint, or MultiKueue worker cluster). |
| `False` | `InvalidCapacity` | Discovered capacity contained negative, unparseable, or illegal resource quantities. |
| `False` | `Misconfigured` | The `spec.parameters` reference or `spec.orchestratedFlavors` configuration is invalid. |

This set of condition types and reasons is provisional for Alpha and will be re-evaluated in Beta based on user and operational feedback.

### Enablement

The feature is opted-in by creation of the DynamicQuotaOrchestrator instance.

Additionally, in the early versions of the feature its enablement is controlled
by the DynamicQuotaOrchestration feature gate. This FG is responsible 
in particular for:
1. registration and start of the `DynamicQuotaOrchestration` controller
2. effectiveness of the `status.effectiveQuotas` field. When the FG is disabled
the field is inactive even if populated.

### Source of Truth for Quotas

The source of truth for quotas in Kueue components, such as scheduler or metric reporters,
is defined as follows:
1. when `DynamicQuotaOrchestration` feature gate is enabled and `status.effectiveQuotas` is
  defined, then use `status.effectiveQuotas.resourceGroups`
2. otherwise use `spec.resourceGroups`

An update to the effective quotas refreshes the scheduler cache
for the affected ClusterQueues or Cohorts, and requests retry of affected inadmissible
workloads.

This entails:
- all scheduler decisions (preemptions, admission, scaling of Jobs, etc.) consider the
effective quotas defined in `status.effectiveQuotas.resourceGroups` as they would
consider `.spec.resourceGroups` otherwise.
- the presence of `status.effectiveQuotas` indicates the effective quotas,
even if `.resourceGroups` is empty, ie. `.status.effectiveQuotas.resourceGroups: []`
exposes no quota resource groups as `spec.resourceGroups: []` would.
- the metrics such as `kueue_cluster_queue_nominal_quota`,
`borrowing_limit` or `cohort_subtree_quota` will report using
`status.effectiveQuotas.resourceGroups` when (1.) is satisfied.

### Proportional distribution and rounding

Effective quota is computed independently for each `(ResourceFlavor, Resource)`
pair. Each pair defines a separate distribution slice.

For a given slice, every ClusterQueue or Cohort in the managed subtree that
defines the pair in `spec.resourceGroups` participates in the distribution. 

For example, if the configured nominal quotas are `100` for `cq1` and `200` for
`cq2`, and the aggregated capacity is `600`, the resulting effective quotas are
`200` and `400`, respectively.

The ideal result may not be representable in the integral units, so 
Kueue uses the largest-remainder method:
- Compute every ideal allocation and round it down.
- Subtract the sum of the rounded allocations from the aggregated capacity, the
  result is the expected capacity which can be collected from the fractional remainders.
- Collect the capacity in descending order of the fractional remainders.

Equal remainders are resolved by the UUIDs of the ClusterQueue or Cohorts.

For example, distributing 10 units among three ClusterQueues initially assigns
3 units to each. After the largest-remainder method we obtain: 4, 3, and 3.
Here the value 4 is picked for the ClusterQueue with the smallest UUID.

For CPU the distribution unit is milliCPU (1m), while for other resources - 1.
For example, one byte for memory or one item for an extended scalar resource.

### Effective quota construction

For each managed ClusterQueue or Cohort, DQO constructs `status.effectiveQuotas.resourceGroups`
by copying `spec.resourceGroups`. It then overlays nominalQuota only for `(ResourceFlavor, resource)` 
pairs present in `status.effectiveCapacity`. The aggregate contains the union of pairs
reported by the specified providers. For a pair in that union, a provider that omits the pair
contributes zero. A pair omitted by all specified providers remains unchanged from `spec.resourceGroups`.

Specifically, `status.effectiveQuotas` does not override the administrator-configured
borrowing or lending limits. DQO treats borrowingLimit and lendingLimit as absolute quantities
and does not scale them proportionally with nominalQuota.

More precisely, borrowingLimit is copied unchanged. A non-null lendingLimit is copied unchanged
for Cohorts, but capped at the effective nominalQuota for ClusterQueues.

This behavior is subject to be re-evaluated in the later releases.

### Resource transformations and DRA resource mappings

DQO does not apply resource transformations or DRA resource mappings during capacity aggregation or distribution.

Instead, the responsibilities are handled at the existing system boundaries:
1. **CapacityProvider controllers** discover underlying resources (such as physical nodes, accelerator devices, or DRA `ResourceSlice` objects) and publish them as normalized `(ResourceFlavor, resource)` pairs into `CapacityProvider.status.capacity`.
2. **DQO controller** operates strictly on normalized `(ResourceFlavor, resource)` tuples to compute `status.effectiveQuotas.resourceGroups`.
3. **Kueue Scheduler** applies any administrator-configured `resourceTransformations` and DRA `resourceMappings`
when evaluating and admitting workloads against `status.effectiveQuotas`.

### Stale Effective Quota

Effective quota may become stale in multiple scenarios, for example:
1. DQO is deleted, deactivated or unpinned.
2. DQO has capacityDistribution disabled.
3. Another external controller managing the field no longer does that.

In all these cases we consider the stale effective quota, represented as
`status.effectiveQuotas` to remain effective, leaving it for administrator
intervention.

This behavior is subject to be re-evaluated in the later releases based on
user feedback. In particular, one potential idea is to use
[automatic expiration of effective quota](#automatic-expiration-of-effective-quota).

### DQO Discovery-only mode

When `spec.capacityDistribution` is absent, the DQO aggregates the capacity
from referenced providers and updates `status.effectiveCapacity`. It does not write
`status.effectiveQuotas` on ClusterQueues or Cohorts.

This mode allows an administrator to validate discovery before enabling quota
distribution.

### DQO Distribution mode

When `spec.capacityDistribution` is present, the controller:

1. resolves the referenced quota subtree;
2. aggregates capacity from the referenced providers;
3. computes proportional quota for ClusterQueues and Cohorts in the subtree;
4. writes `status.effectiveQuotas`, including an `orchestratorRef` to the DQO; and
5. updates the DQO status and conditions.

### Soft validation of overlapping orchestrators

Nested distributing DQOs are not rejected by admission. Instead, the controller
performs soft validation against the quota tree.

If another DQO's `subtreeRootQuotaRef` points to a strict ancestor of this DQO's
subtree root, this DQO is deactivated, with condition:
```yaml
type: Distributed
status: False
reason: ConflictingDynamicQuotaOrchestrator
```

The higher DQO remains active. The controller continuously reevaluates the
condition, so the lower DQO can become active after the conflict is removed.
A discovery-only DQO has no subtree root and therefore does not participate in
this check. If there are two DQO instances pointing to the same root, then
the older wins. UUID is used as the final tie-break if created at the same time.

### Validation for aggregated capacity

DQO aggregates capacity from multiple capacity providers which may manage
different sets of flavors. Thus the total number of flavors output by DQO
in the `status.effectiveCapacity` is only capped by the multiplication of the
total number of capacity providers and their cap on the number of managed flavors.

In order to solve this problem in alpha DQO allows to output capacity for a higher
number of flavors (and resources within) than an individual capacity provider.

This is acceptable, because the computation of the EffectiveQuotas truncate large
set to only those which are specified in the spec.resourceGroups.

When the cap is exceeded, the DQO gets deactivated with a dedicated reason:
`TooManyFlavorsInAggregatedCapacity` or `TooManyResourcesInAggregatedCapacity`.

### Examples

Capacity provider using the local-capacity controller:

```yaml
kind: CapacityProvider
apiVersion: kueue.x-k8s.io/v1alpha1
metadata:
  name: local-nodes
spec:
  orchestratedFlavors:
  - name: cpu-flavor
  - name: gpu-flavor
  controllerName: kueue.x-k8s.io/local-capacity
status:
  capacity:
    flavors:
    - name: cpu-flavor
      resources:
        cpu: "900"
        memory: 3.6Ti
    - name: gpu-flavor
      resources:
        nvidia.com/gpu: "32"
```

DQO referencing the provider and distributing capacity from a Cohort root:

```yaml
kind: DynamicQuotaOrchestrator
apiVersion: kueue.x-k8s.io/v1alpha1
metadata:
  name: production-capacity
spec:
  capacityDiscovery:
    providers:
    - name: local-nodes
      effectiveCapacityMultiplier: 0.95
  capacityDistribution:
    subtreeRootQuotaRef:
      kind: Cohort
      name: production
```

Illustrative effective quota written to a ClusterQueue:

```yaml
status:
  effectiveQuotas:
    orchestratorRef:
      apiGroup: kueue.x-k8s.io
      kind: DynamicQuotaOrchestrator
      name: production-capacity
    resourceGroups:
    - coveredResources: [cpu, memory]
      flavors:
      - name: cpu-flavor
        resources:
        - name: cpu
          nominalQuota: "18"
        - name: memory
          nominalQuota: 72Gi
```

### Test Plan

[x] I/we understand the owners of the involved components may require updates
to existing tests to make this code solid enough prior to committing the
changes necessary to implement this enhancement.

#### Unit Tests

Unit tests will cover:

- aggregation of capacity from multiple providers per flavor/resource pair;
- application of `effectiveCapacityMultiplier` and its default;
- scheduling based on `status.effectiveQuotas`
- `DynamicQuotaOrchestrator` reconcile (discovery, distribution)

#### Integration Tests

Integration tests will cover:

- The Kueue scheduler using effective quota instead of spec quota;
- Distributing DQO writing effective quota throughout its subtree;
- CapacityProvider update propagating to DQO aggregated capacity;
- Discovery-only DQO not writing effective quota;
- Conflicting pointers by two different DQO instances disable the lower one (soft validation)

### Graduation Criteria

#### Alpha

- Introduce the `CapacityProvider`, `DynamicQuotaOrchestrator`, and `status.effectiveQuotas` APIs.
- Introduce the `DynamicQuotaOrchestration` feature gate (disabled by default).
- Implement the `DynamicQuotaOrchestrator` controller.
- Make the scheduler consume effective quota when present (and FG enabled).
- Implement tests using the "external" `CapacityProvider` controller implemented for tests.

#### Beta

- Fix all known bugs.
- Address user feedback.
- Demonstrate the mechanism with supported capacity-provider integrations.
- Document operational behavior, observability, and upgrade expectations.
- Introduce the `DynamicQuotaOrchestration` feature gate (enabled by default).
- Re-evaluate the idea for [automatic expiration of effective quota](#automatic-expiration-of-effective-quota).
- Re-evaluate the idea for DQO scaling borrowing or lending limit.
- Revisit and refine the condition types, reasons, and state transitions across `CapacityProvider` and `DynamicQuotaOrchestrator` (and evaluate introducing dedicated `ClusterQueue`/`Cohort` status conditions) based on operational feedback.

#### Stable

- Fix all known bugs.
- Production feedback has been addressed.
- Scalability tests.
- Introduce the `DynamicQuotaOrchestration` feature gate (locked to be enabled).

## Implementation History

- 2026 Aug 09: Initial KEP draft.

## Drawbacks

Increased conceptual and operational complexity of quota management. 
In particular, inspecting the spec alone may no longer be sufficient to
determine the quota currently in effect. 

## Alternatives

### Internal quota transformation pipeline

An internal `QuotaManager` could apply a hard-coded pipeline of transformations
and store only the final result in ClusterQueue status.

**Reasons for deferring/rejecting**:

This was not selected because it is not extensible to external capacity
providers, overloads the ClusterQueue controller, and makes the active pipeline
less transparent.

### Effective quota override in spec

The DQO could write an override into ClusterQueue or Cohort spec.

**Reasons for deferring/rejecting**:

This was not selected because it requires write access to administrator-owned
spec, conflicts with GitOps reconciliation, and causes frequent desired-state
changes.

### Automatic expiration of effective quota

EffectiveQuotaStatus could include a `validUntil` timestamp that the managing
DQO periodically extends as a heartbeat. After the timestamp expires, Kueue would
mark the effective quota as stale, for example with an `EffectiveQuotaStale=True` 
condition, and the scheduler would ignore `status.effectiveQuotas` and fall back
to `spec.resourceGroups`. This would prevent an effective quota from remaining
active indefinitely when the managing DQO is deleted, becomes unavailable, or
stops distributing capacity.

**Reasons for deferring/rejecting**:

The proposal is reasonable, but deferred from the first iteration of Alpha
to derisk delivery, and reduce the overall complexity of the solution. We will
re-evaluate the approach based on user feedback before Beta.

### Store all distributed quota in DQO status

The DQO could store every computed ClusterQueue and Cohort quota in its own
status, with another controller copying or consuming the result.

**Reasons for deferring/rejecting**:

This was not selected because the DQO object could approach the Kubernetes
object-size limit in large clusters and would duplicate responsibility between
controllers.

### Sparse effective quota overrides

Effective quota could contain only overridden `(ResourceFlavor, resource)`
pairs.

**Reasons for deferring/rejecting**:

This was not selected because matching the `spec.resourceGroups` structure
simplifies scheduler consumption and leaves room for future distribution logic
that needs the full quota structure.

### One CRD per capacity provider

Each provider could define a separate capacity CRD with a standardized status
shape.

**Reasons for deferring/rejecting**:

This was not selected because it creates many CRDs to maintain and allows their
status and condition conventions to diverge.

### Typed union of built-in providers

`CapacityProvider.spec` could contain a union with built-in variants such as
Specified, Local, MultiKueue, and Controller.

**Reasons for deferring/rejecting**:

This was not selected because adding providers would grow the core API. A
generic `controllerName` plus optional provider parameters keeps the provider
API canonical while provider-specific configuration evolves independently.

### CapacityProvider referencing namespaced config

The capacity provider's `parameters` field could contain the `Namespace`
and read from a namespaced object.

**Reasons for deferring/rejecting**:

CapacityProvider itself is cluster-scoped and affects cluster-scoped quota,
so this keeps the ownership and authorization model straightforward.

Also, this follows the model of Parameters used for modeling AdmissionChecks.

### Nested ResourceCapacity for CapacitySnapshot

We could consider nested structure for resources produced by CapacityProviders,
within a flavor, ie:

```yaml
flavors:
- name: cpu-flavor
  resources:
  - name: "cpu"
    value: "900"
  - name: "memory"
    value: "3.6Ti"
```

**Reasons for deferring/rejecting**:

Since capacity providers are scraping the capacity information rather
than quotas it seems preferred to use the k8s native model for representing
capacities.
