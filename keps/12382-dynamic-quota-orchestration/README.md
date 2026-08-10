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
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Overview](#overview)
  - [Phase 1: Capacity Discovery](#phase-1-capacity-discovery)
  - [Phase 2: Quota Distribution](#phase-2-quota-distribution)
  - [APIs](#apis)
    - [ClusterQueue and Cohort effective quota](#clusterqueue-and-cohort-effective-quota)
    - [DynamicQuotaOrchestrator](#dynamicquotaorchestrator)
    - [DynamicQuotaOrchestrator status](#dynamicquotaorchestrator-status)
    - [CapacityReport](#capacityreport)
  - [Controller Behavior](#controller-behavior)
    - [Discovery-only mode](#discovery-only-mode)
    - [Distribution mode](#distribution-mode)
    - [Soft validation of overlapping orchestrators](#soft-validation-of-overlapping-orchestrators)
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
  - [Store all distributed quota in DQO status](#store-all-distributed-quota-in-dqo-status)
  - [Sparse effective quota overrides](#sparse-effective-quota-overrides)
  - [One CRD per capacity provider](#one-crd-per-capacity-provider)
  - [Typed union of built-in providers](#typed-union-of-built-in-providers)
<!-- /toc -->

## Summary

This KEP introduces Dynamic Quota Orchestration (DQO), a mechanism for deriving
Kueue quota from capacity reported by one or more providers and optionally
distributing that capacity across a ClusterQueue/Cohort subtree.

The mechanism has two phases:

1. Capacity providers publish normalized capacity through `CapacityReport`
   objects. A `DynamicQuotaOrchestrator` aggregates selected reports.
2. distribution of the aggregated capact proportionally distributes the
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
- Support distribution policies other than proportional distribution.
- Automatically change borrowing or lending limits.
- Restrict individual capacity providers by resource name. This may be added
  later if needed.

## Proposal

Introduce two cluster-scoped APIs:

- `CapacityReport`, written by a capacity provider, containing normalized
  capacity in status; and
- `DynamicQuotaOrchestrator`, selecting reports and optionally identifying the
  quota subtree where aggregated capacity is distributed.

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
`CapacityReport`. For example, the controller may discover and expose
cluster-autoscaler capacity using vendor-specific APIs.

[#10270]: https://github.com/kubernetes-sigs/kueue/issues/10270
[#9988]: https://github.com/kubernetes-sigs/kueue/issues/9988
[#8654]: https://github.com/kubernetes-sigs/kueue/issues/8654

### Notes

### Risks and Mitigations

## Design Details

### Overview

DQO separates capacity discovery from quota distribution:

1. Providers convert their source-specific view into a common
   `CapacitySnapshot` in `CapacityReport.status`.
2. A DQO selects reports and aggregates their contributions per
   `(ResourceFlavor, resource)` pair.
3. If `capacityDistribution` is configured, the DQO computes proportional
   effective quota for all ClusterQueues and Cohorts in the selected subtree.
4. The scheduler uses effective quota when evaluating workloads.

### Phase 1: Capacity Discovery

Each referenced `CapacityReport` contributes capacity per
`(ResourceFlavor, resource)` pair. `usableCapacityPercent` controls how much of
the report contributes to the aggregate and defaults to 100.

Reported values represent total capacity attributable to the provider, not
currently free capacity. Values must be non-negative. The resulting aggregate
is exposed in `DynamicQuotaOrchestrator.status.aggregatedCapacity`.

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
    // +optional
    EffectiveQuota *EffectiveQuotaStatus `json:"effectiveQuota,omitempty"`
}

type EffectiveQuotaStatus struct {
    // lastUpdateTime is the time at which the effective quota was last updated.
    LastUpdateTime metav1.Time `json:"lastUpdateTime"`

    // managerRef identifies the component managing this value.
    ManagerRef EffectiveQuotaStatusManagerRef `json:"managerRef"`

    // resourceGroups is the effective quota used by the scheduler.
    //
    // +listType=atomic
    // +kubebuilder:validation:MaxItems=16
    ResourceGroups []ResourceGroup `json:"resourceGroups"`
}

type EffectiveQuotaStatusManagerRef struct {
    Kind string `json:"kind"`
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
    CapacityDiscovery CapacityDiscovery `json:"capacityDiscovery"`

    // capacityDistribution specifies how aggregated capacity is distributed.
    // When omitted, the DQO is discovery-only: it reports aggregated capacity
    // but does not write effectiveQuota status.
    //
    // +optional
    CapacityDistribution *CapacityDistribution `json:"capacityDistribution,omitempty"`
}

type CapacityDiscovery struct {
    // reports lists CapacityReport objects consumed by this DQO.
    Reports []CapacityDiscoveryReport `json:"reports"`
}

type CapacityDiscoveryReport struct {
    // name identifies a CapacityReport.
    Name CapacityReportName `json:"name"`

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
    SubtreeRootRef CapacityDistributionSubtreeRootRef `json:"subtreeRootRef"`
}

type SubtreeRootRefType string

const (
    ClusterQueueSubtreeRootRefType SubtreeRootRefType = "ClusterQueue"
    CohortSubtreeRootRefType       SubtreeRootRefType = "Cohort"
)

type CapacityDistributionSubtreeRootRef struct {
    // kind is ClusterQueue or Cohort.
    Kind SubtreeRootRefType `json:"kind"`
    Name string             `json:"name"`
}

type CapacityReportName string
```

#### DynamicQuotaOrchestrator status

```go
type DynamicQuotaOrchestratorStatus struct {
    // aggregatedCapacity is the capacity aggregated from selected reports.
    //
    // +optional
    AggregatedCapacity *CapacitySnapshot `json:"aggregatedCapacity,omitempty"`

    // conditions represents the current state of the DQO.
    //
    // +optional
    Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type CapacitySnapshot struct {
    // flavors contains capacity per flavor and resource.
    Flavors []FlavorCapacity `json:"flavors"`

    // lastUpdateTime is the time at which the snapshot was last updated.
    LastUpdateTime metav1.Time `json:"lastUpdateTime"`
}

type FlavorCapacity struct {
    // name identifies the ResourceFlavor whose capacity is reported.
    Name ResourceFlavorReference `json:"name"`

    // resources lists capacity by resource name.
    //
    // +kubebuilder:validation:MinItems=1
    // +kubebuilder:validation:MaxItems=64
    Resources []ResourceCapacity `json:"resources"`
}

type ResourceCapacity struct {
    // name identifies the resource whose capacity is reported.
    Name corev1.ResourceName `json:"name"`

    // value is total capacity attributable to the provider, not currently free
    // capacity. It must be non-negative.
    Value resource.Quantity `json:"value"`
}
```

The DQO reports conditions including `Active` and, when distribution is
configured, `Distributed`.

#### CapacityReport

`CapacityReport` is the common status API for all capacity providers. A
provider may use its own configuration CRD and reference it through
`spec.parameters`; DQO does not read or validate that object.

```go
type CapacityReport struct {
    Spec   CapacityReportSpec   `json:"spec,omitempty"`
    Status CapacityReportStatus `json:"status,omitempty"`
}

type CapacityReportSpec struct {
    // managedFlavors identifies the ResourceFlavors for which this report may
    // publish capacity.
    //
    // +kubebuilder:validation:MinItems=1
    // +kubebuilder:validation:MaxItems=64
    ManagedFlavors []CapacityReportManagedFlavor `json:"managedFlavors"`

    // controllerName identifies the controller publishing capacity. This field
    // is immutable.
    ControllerName CapacityReportControllerName `json:"controllerName"`

    // parameters optionally references implementation-specific configuration.
    // DQO does not read or validate the referenced object.
    //
    // +optional
    Parameters *CapacityReportParametersReference `json:"parameters,omitempty"`
}

// CapacityReportManagedFlavor identifies a flavor managed by a report.
// The container allows the mapping to be extended in a future API version.
type CapacityReportManagedFlavor struct {
    Name ResourceFlavorReference `json:"name"`
}

type CapacityReportParametersReference struct {
    APIGroup string `json:"apiGroup"`
    Kind     string `json:"kind"`
    Name     string `json:"name"`
}

type CapacityReportStatus struct {
    // capacity is the normalized capacity published by the provider.
    //
    // +optional
    Capacity *CapacitySnapshot `json:"capacity,omitempty"`

    // conditions represents the current state of this report.
    //
    // +optional
    Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type CapacityReportControllerName string
```

### Controller Behavior

#### Discovery-only mode

When `spec.capacityDistribution` is absent, the DQO aggregates its selected
reports and updates `status.aggregatedCapacity`. It does not write
`status.effectiveQuota` on ClusterQueues or Cohorts.

This mode allows an administrator to validate discovery before enabling quota
distribution.

#### Distribution mode

When `spec.capacityDistribution` is present, the controller:

1. resolves the referenced quota subtree;
2. aggregates the selected capacity reports;
3. computes proportional quota for ClusterQueues and Cohorts in the subtree;
4. writes `status.effectiveQuota`, including a `managerRef` to the DQO; and
5. updates the DQO status and conditions.

#### Soft validation of overlapping orchestrators

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
this check.

### Examples

Capacity report produced by a local-capacity controller:

```yaml
kind: CapacityReport
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
      - name: cpu
        value: "900"
      - name: memory
        value: 3.6Ti
    - name: gpu-flavor
      resources:
      - name: nvidia.com/gpu
        value: "32"
```

DQO using the report and distributing capacity from a Cohort root:

```yaml
kind: DynamicQuotaOrchestrator
metadata:
  name: production-capacity
spec:
  capacityDiscovery:
    reports:
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

- aggregation of multiple reports per flavor/resource pair;
- application of `usableCapacityPercent` and its default;
- scheduling based on `status.effectiveQuota`
- `DynamicQuotaOrchestrator` reconcile (discovery, distribution)

#### Integration Tests

Integration tests will cover:

- The Kueue scheduler using effective quota instead of spec quota;
- Distributing DQO writing effective quota throughout its subtree;
- CapacityReport update propagating to DQO aggregated capacity;
- Discovery-only DQO not writing effective quota;
- Conflicting pointers by two different DQO instances disable the lower one (soft validation)

### Graduation Criteria

#### Alpha

- Introduce the `CapacityReport`, `DynamicQuotaOrchestrator`, and `status.effectiveQuota` APIs.
- Introduce the `DynamicQuotaOrchestration` feature gate (disabled by default).
- Implement controller for `DynamicQuotaOrchestrator` controller.
- Make the scheduler consume effective quota when present (and FG enabled).
- Implement tests using the "external" `CapacityReport` controller implemented for tests.

#### Beta

- Fix all known bugs.
- Address user feedback.
- Demonstrate the mechanism with supported capacity-provider integrations.
- Document operational behavior, observability, and upgrade expectations.
- Introduce the `DynamicQuotaOrchestration` feature gate (enabled by default).

#### Stable

- Fix all known bugs.
- Production feedback has been addressed.
- Scalability tests.
- Introduce the `DynamicQuotaOrchestration` feature gate (locked to be enabled1).

## Implementation History

- 2026 Aug 09: Initial KEP draft.

## Drawbacks

<!-- Intentionally left empty in this draft. -->

## Alternatives

### Internal quota transformation pipeline

An internal `QuotaManager` could apply a hard-coded pipeline of transformations
and store only the final result in ClusterQueue status.

This was not selected because it is not extensible to external capacity
providers, overloads the ClusterQueue controller, and makes the active pipeline
less transparent.

### Effective quota override in spec

The DQO could write an override into ClusterQueue or Cohort spec.

This was not selected because it requires write access to administrator-owned
spec, conflicts with GitOps reconciliation, and causes frequent desired-state
changes.

### Store all distributed quota in DQO status

The DQO could store every computed ClusterQueue and Cohort quota in its own
status, with another controller copying or consuming the result.

This was not selected because the DQO object could approach the Kubernetes
object-size limit in large clusters and would duplicate responsibility between
controllers.

### Sparse effective quota overrides

Effective quota could contain only overridden `(ResourceFlavor, resource)`
pairs.

This was not selected because matching the `spec.resourceGroups` structure
simplifies scheduler consumption and leaves room for future distribution logic
that needs the full quota structure.

### One CRD per capacity provider

Each provider could define a separate capacity CRD with a standardized status
shape.

This was not selected because it creates many CRDs to maintain and allows their
status and condition conventions to diverge.

### Typed union of built-in providers

`CapacityReport.spec` could contain a union with built-in variants such as
Specified, Local, MultiKueue, and Controller.

This was not selected because adding providers would grow the core API. A
generic `controllerName` plus optional provider parameters keeps the normalized
report stable while provider-specific configuration evolves independently.
