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
  - [Notes](#notes)
    - [Rationale for usableCapacityPercent](#rationale-for-usablecapacitypercent)
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
    - [CapacityProvider](#capacityprovider)
  - [Enablement](#enablement)
  - [Source of Truth for Quotas](#source-of-truth-for-quotas)
  - [Proportional distribution and rounding](#proportional-distribution-and-rounding)
  - [Effective quota construction](#effective-quota-construction)
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
  - [Nested ResorceCapacity for CapacitySnapshot](#nested-resorcecapacity-for-capacitysnapshot)
<!-- /toc -->

## Summary

This KEP introduces Dynamic Quota Orchestration (DQO), a mechanism for deriving
Kueue quota from capacity reported by one or more providers and optionally
distributing that capacity across a ClusterQueue/Cohort subtree.

The mechanism has two phases:

1. Capacity providers publish normalized capacity through `CapacityProvider`
   objects. A `DynamicQuotaOrchestrator` aggregates capacity from the referenced providers.
2. DQO proportionally distributes the
   aggregated capacity and writes the resulting effective quota to the status of
   each managed ClusterQueue and Cohort.

The ClusterQueue and Cohort specs remain the administrator-owned source of the
quota structure and distribution proportions. The scheduler uses
`status.effectiveQuota`, when present, instead of `spec.resourceGroups`.

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

Add `status.effectiveQuota` to ClusterQueue and Cohort. The DQO controller owns
this status when identified by `managerRef`. The scheduler uses the effective
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

### Notes

#### Rationale for usableCapacityPercent

The usableCapacityPercent parameter provides flexibility in translating observed
capacity into the effective capacity made available by Kueue for quota distribution.
Some of the use-cases:
- **Operational headroom** (eg 95%): when TAS is not used, leaving a small capacity buffer
can reduce the impact of node failures or resource fragmentation.
- **Capacity partitioning** (eg 50%): Allows to share the discovered node capacity
between two CQs which are orchestrated by different DQO instances.
- **MultiKueue candidate overbooking** (eg. 300%). Setting the value above 100 can
be useful to compensate for the fact that sometimes large workloads holding
manager-side quota, without overbooking, can exhaust the manager-side quota and
prevent other feasible workloads from being considered. This may happen when the
workloads are inadmissible or are very picky about the worker cluster. See also
[this section](https://github.com/kubernetes-sigs/kueue/tree/main/keps/9988-multikueue-manager-quota-automation#potential-reasons-for-increasing-manager-quota) 
in the related "MultiKueue Manager Quota Automation" KEP.

### Risks and Mitigations

#### User confusion about effective quota

This risk is an intrinsic [drawback](#drawbacks) of the proposal due to separating
administrator-configured intent from controller-computed runtime state. 

**Mitigation tactics**:

- `status.effectiveQuota` records its `managerRef` and `lastUpdateTime` fields
useful for troubleshooting and observability.
- The documentation will describe the precedence between `spec.resourceGroups` and
`status.effectiveQuota` and provide troubleshooting guidance.
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
  `status.effectiveQuota`  will allow admins to detect the anomalies when the workloads are overcommitted.

## Design Details

### Overview

DQO separates capacity discovery from quota distribution:

1. Providers convert their source-specific view into a common
   `CapacitySnapshot` in `CapacityProvider.status`.
2. A DQO aggregates contributions from the providers referenced in
   `spec.capacityDiscovery.providers` per `(ResourceFlavor, resource)` pair.
3. If `capacityDistribution` is configured, the DQO computes proportional
   effective quota for all ClusterQueues and Cohorts in the referenced subtree.
4. The scheduler uses effective quota when evaluating workloads.

### Phase 1: Capacity Discovery

Each referenced `CapacityProvider` contributes capacity per
`(ResourceFlavor, resource)` pair. `usableCapacityPercent` controls how much of
the provider's reported capacity contributes to the aggregated capacity.

The resulting aggregate is exposed in `DynamicQuotaOrchestrator.status.aggregatedCapacity`.

### Phase 2: Quota Distribution

When `spec.capacityDistribution` is present, the DQO distributes aggregated
capacity within the subtree rooted at the referenced ClusterQueue or Cohort.

The default policy is proportional distribution. For each
`(ResourceFlavor, resource)` pair, proportions come from the corresponding
quota configured by the administrator in ClusterQueue and Cohort
`spec.resourceGroups`.

The result for each managed object is represented as a complete
`ResourceGroup` list in `status.effectiveQuota`. Other distribution policies may
be introduced in future KEP updates.

### APIs

First, we propose to extend the ClusterQueue and Cohort APIs with the field
which will allow external controllers to set the dynamic quota assignments,
effectively overriding the quotas set in spec.resourceGroups.

#### ClusterQueue and Cohort effective quota

```go
type ClusterQueueStatus struct {
    // ...

    // effectiveQuota is used for scheduling instead of spec.resourceGroups when
    // present.
    //
    // +optional
    EffectiveQuota *EffectiveQuotaStatus `json:"effectiveQuota,omitempty"`
}

type CohortStatus struct {
    // ...

    // effectiveQuota is used for scheduling instead of spec.resourceGroups when
    // present.
    //
    // This field is alpha-level, and is ignored by Kueue when the DynamicQuotaOrchestration
    // feature gate is disabled.
    //
    // +optional
    EffectiveQuota *EffectiveQuotaStatus `json:"effectiveQuota,omitempty"`
}

type EffectiveQuotaStatus struct {
    // lastUpdateTime is the time at which the effective quota was last updated.
    // 
    // +required
    // +kubebuilder:validation:Type=string
    // +kubebuilder:validation:Format=date-time
    LastUpdateTime metav1.Time `json:"lastUpdateTime"`

    // managerRef identifies the component managing this value.
    //
    // +required
    ManagerRef EffectiveQuotaStatusManagerRef `json:"managerRef"`

    // resourceGroups is the effective quota used by the scheduler.
    //
    // +listType=atomic
    // +kubebuilder:validation:MaxItems=16
    ResourceGroups []ResourceGroup `json:"resourceGroups"`
}

type EffectiveQuotaStatusManagerRef struct {
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
    // but does not write effectiveQuota status.
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

    // usableCapacityPercent specifies the contribution of discovered capacity.
    // It defaults to 100.
    //
    // +optional
    // +kubebuilder:default=100
    // +kubebuilder:validation:Minimum=0
    // +kubebuilder:validation:Maximum=10000
    UsableCapacityPercent *int32 `json:"usableCapacityPercent,omitempty"`
}

type CapacityDistribution struct {
    // subtreeRootRef identifies the root of the quota subtree.
    //
    // +required
    SubtreeRootRef CapacityDistributionSubtreeRootRef `json:"subtreeRootRef"`
}

type SubtreeRootRefType string

const (
    ClusterQueueSubtreeRootRefType SubtreeRootRefType = "ClusterQueue"
    CohortSubtreeRootRefType       SubtreeRootRefType = "Cohort"
)

type CapacityDistributionSubtreeRootRef struct {
    // kind indicates the kind of the quota node, i.e. ClusterQueue or Cohort.
    // 
    // +required
    // +kubebuilder:validation:Enum=ClusterQueue;Cohort
    Kind SubtreeRootRefType `json:"kind"`

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
    // aggregatedCapacity is the capacity aggregated from the referenced providers.
    //
    // +optional
    AggregatedCapacity *AggregatedCapacity `json:"aggregatedCapacity,omitempty"`

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

type AggregatedCapacity struct {
    // flavors contains capacity per flavor and resource.
    //
    // +listType=map
    // +listMapKey=name
    // +kubebuilder:validation:MaxItems=128
    Flavors []AggregatedCapacityFlavor `json:"flavors"`

    // lastUpdateTime is the time at which the snapshot was last updated.
    // 
    // +required
    // +kubebuilder:validation:Type=string
    // +kubebuilder:validation:Format=date-time
    LastUpdateTime metav1.Time `json:"lastUpdateTime"`
}

type AggregatedCapacityFlavor struct {
    // name identifies the ResourceFlavor whose capacity is reported.
    //
    // +required
    Name ResourceFlavorReference `json:"name"`

    // resources contains total capacity by resource name.
    //
    // +required
    // +kubebuilder:validation:MinProperties=1
    // +kubebuilder:validation:MaxProperties=64
    // +kubebuilder:validation:XValidation:rule="self.all(r, type(self[r]) == string ? quantity(self[r]).sign() >= 0 : self[r] >= 0)",message="resource capacity must be non-negative"
    Resources corev1.ResourceList `json:"resources"`
}
```

The DQO reports conditions including `Active` and, when distribution is
configured, `Distributed`.

#### CapacityProvider

`CapacityProvider` is the common status API for all capacity providers. A
provider may use its own configuration CRD and reference it through
`spec.parameters`; DQO does not read or validate that object.

```go
type CapacityProvider struct {
    Spec   CapacityProviderSpec   `json:"spec,omitempty"`
    Status CapacityProviderStatus `json:"status,omitempty"`
}

type CapacityProviderSpec struct {
    // managedFlavors identifies the ResourceFlavors for which this provider may
    // publish capacity. DQO ignores entries in status.capacity.flavors whose
    // names are not listed here.
    //
    // +listType=map
    // +listMapKey=name
    // +kubebuilder:validation:MinItems=1
    // +kubebuilder:validation:MaxItems=64
    ManagedFlavors []CapacityProviderManagedFlavor `json:"managedFlavors"`

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

// CapacityProviderManagedFlavor identifies a flavor managed by a provider.
// The container allows the mapping to be extended in a future API version.
type CapacityProviderManagedFlavor struct {
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
    Capacity *CapacityProviderSnapshot `json:"capacity,omitempty"`

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

type CapacityProviderSnapshot struct {
    // flavors contains capacity per flavor and resource.
    //
    // +listType=map
    // +listMapKey=name
    // +kubebuilder:validation:MaxItems=64
    Flavors []CapacityProviderSnapshotFlavor `json:"flavors"`

    // lastUpdateTime is the time at which the snapshot was last updated.
    //
    // +required
    // +kubebuilder:validation:Type=string
    // +kubebuilder:validation:Format=date-time
    LastUpdateTime metav1.Time `json:"lastUpdateTime"`
}

type CapacityProviderSnapshotFlavor struct {
    // name identifies the ResourceFlavor whose capacity is reported.
    //
    // +required
    Name ResourceFlavorReference `json:"name"`

    // resources contains total capacity by resource name.
    //
    // +required
    // +kubebuilder:validation:MinProperties=1
    // +kubebuilder:validation:MaxProperties=64
    // +kubebuilder:validation:XValidation:rule="self.all(r, type(self[r]) == string ? quantity(self[r]).sign() >= 0 : self[r] >= 0)",message="resource capacity must be non-negative"
    Resources corev1.ResourceList `json:"resources"`
}

// +kubebuilder:validation:MinLength=1
// +kubebuilder:validation:MaxLength=253
type CapacityProviderControllerName string
```

### Enablement

The feature is opted-in by creation of the DynamicQuotaOrchestrator instance.

Additionally, in the early versions of the feature its enablement is controlled
by the DynamicQuotaOrchestration feature gate. This FG is responsible 
in particular for:
1. registration and start of the `DynamicQuotaOrchestration` controller
2. effectiveness of the `status.effectiveQuota` field. When the FG is disabled
the field is inactive even if populated.

### Source of Truth for Quotas

The source of truth for quotas in Kueue components, such as scheduler or metric reporters,
is defined as follows:
1. when `DynamicQuotaOrchestration` feature gate is enabled and `status.effectiveQuota` is
  defined, then use `status.effectiveQuota.resourceGroups`
2. otherwise use `spec.resourceGroups`

In particular this means the metrics such as `kueue_cluster_queue_nominal_quota`, 
`borrowing_limit` or `cohort_subtree_quota` will report using
`status.effectiveQuota.resourceGroups` when (1.) is satisfied.

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

For example, distributing 10 units among three ClusterQueues initially assigns
3 units to each. After the largest-remainder method we obtain: 4, 3, and 3.

### Effective quota construction

For each managed ClusterQueue or Cohort, DQO constructs `status.effectiveQuota.resourceGroups`
by copying `spec.resourceGroups`. It then overlays nominalQuota only for `(ResourceFlavor, resource)` 
pairs present in `status.aggregatedCapacity`. The aggregate contains the union of pairs
reported by the specified providers. For a pair in that union, a provider that omits the pair
contributes zero. A pair omitted by all specified providers remains unchanged from `spec.resourceGroups`.

Specifically, `status.effectiveQuota` does not override the administrator-configured
borrowing or lending limits - it copies them directly from `spec.resourceGroups`.

This behavior is subject to be re-evaluated in the later releases.

### Stale Effective Quota

Effective quota may become stale in a multiple of scenarios, for example:
1. DQO is deleted, deactivated or unpinned.
2. DQO has capacityDistribution disabled.
3. Another external controller managing the field no longer does that.

In all these cases we consider the stale effective quota, represented as
`status.effectiveQuota` to remain effective, and left for an admin
intervention.

This behavior is subject to be re-evaluated in the later releases based on
user feedback. In particular, one potential idea is to use
[automatic expiration of effective quota](#automatic-expiration-of-effective-quota).

### DQO Discovery-only mode

When `spec.capacityDistribution` is absent, the DQO aggregates the capacity
from referenced providers and updates `status.aggregatedCapacity`. It does not write
`status.effectiveQuota` on ClusterQueues or Cohorts.

This mode allows an administrator to validate discovery before enabling quota
distribution.

### DQO Distribution mode

When `spec.capacityDistribution` is present, the controller:

1. resolves the referenced quota subtree;
2. aggregates capacity from the referenced providers;
3. computes proportional quota for ClusterQueues and Cohorts in the subtree;
4. writes `status.effectiveQuota`, including a `managerRef` to the DQO; and
5. updates the DQO status and conditions.

### Soft validation of overlapping orchestrators

Nested distributing DQOs are not rejected by admission. Instead, the controller
performs soft validation against the quota tree.

If another DQO's `subtreeRootRef` points to a strict ancestor of this DQO's
subtree root, this DQO is deactivated, with condition:
```yaml
type: Active
status: False
reason: ConflictingDynamicQuotaOrchestrator
```

The higher DQO remains active. The controller continuously reevaluates the
condition, so the lower DQO can become active after the conflict is removed.
A discovery-only DQO has no subtree root and therefore does not participate in
this check. If there are two DQO instances pointing to the same root, then
the older wins. UUID is used as the final tie-break if created at the same time.

### Validation for aggregated capacity

DQO aggregates capacity from mulitple capacity providers which may manage
differnt sets of flavors. Thus the total number of flavors output by DQO
in the `status.aggregatedCapacity` is only capped by the multiplication of the
total number of capacity providers and their cap on the number of managed flavors.

In order to solve this problem in alpha DQO allows to output capacity for a higher
number of flavors (and resources within) than an individual capacity provider.

This is accaptable, because the computation of the EffectiveQuota truncate large
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
  managedFlavors:
  - name: cpu-flavor
  - name: gpu-flavor
  controllerName: kueue.x-k8s.io/local-capacity
status:
  capacity:
    lastUpdateTime: "2026-10-01T18:08:09Z"
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
      usableCapacityPercent: 95
  capacityDistribution:
    subtreeRootRef:
      kind: Cohort
      name: production
```

Illustrative effective quota written to a ClusterQueue:

```yaml
status:
  effectiveQuota:
    lastUpdateTime: "2026-10-01T18:08:09Z"
    managerRef:
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
- application of `usableCapacityPercent` and its default;
- scheduling based on `status.effectiveQuota`
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

- Introduce the `CapacityProvider`, `DynamicQuotaOrchestrator`, and `status.effectiveQuota` APIs.
- Introduce the `DynamicQuotaOrchestration` feature gate (disabled by default).
- Implement controller for `DynamicQuotaOrchestrator` controller.
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
condition, and the scheduler would ignore `status.effectiveQuota` and fall back
to `spec.resourceGroups`. This would prevent an effective quota from remaining
active indefinitely when the managing DQO is deleted, becomes unavailable, or
stops distributing capacity.

**Reasons for deferring/rejecting**:

The proposal is reasonable, but deferred from the first iteration of Alpha
to derisk deliver, and reduce the overall complexity of the solution. We will
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

### Nested ResorceCapacity for CapacitySnapshot

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
