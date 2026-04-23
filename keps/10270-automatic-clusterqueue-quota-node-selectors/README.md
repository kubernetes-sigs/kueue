# KEP-10270: Automatic quota limits based on node selectors

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
    - [Story 3](#story-3)
    - [Story 4](#story-4)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Alpha: Coarse-Grained Auto-Detection](#alpha-coarse-grained-auto-detection)
    - [API Changes (Alpha)](#api-changes-alpha)
    - [Validation Rules (Alpha)](#validation-rules-alpha)
    - [Status Extension](#status-extension)
  - [Beta: Fine-Grained Node Label Intersection](#beta-fine-grained-node-label-intersection)
    - [API Changes (Beta)](#api-changes-beta)
    - [Intersection Semantics](#intersection-semantics)
    - [Validation Rules (Beta)](#validation-rules-beta)
  - [Controller Design](#controller-design)
    - [Node Watcher](#node-watcher)
    - [Reconciliation Loop](#reconciliation-loop)
  - [Interaction with Existing Features](#interaction-with-existing-features)
  - [RBAC Requirements](#rbac-requirements)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration Tests](#integration-tests)
    - [e2e Tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [GA](#ga)
- [Implementation History](#implementation-history)
<!-- /toc -->

## Summary

This KEP proposes extending the ClusterQueue and Cohort APIs to support automatic
quota detection based on node labels. Instead of requiring administrators to
manually set and maintain `nominalQuota` values, Kueue can dynamically compute
quotas by watching Kubernetes Nodes and aggregating their Allocatable resources.

The feature is delivered in two phases:

- **Alpha (coarse-grained)**: A simple `autoDetect: true` flag at the flavor
  level. Kueue uses the associated ResourceFlavor's `nodeLabels` to select nodes
  and computes quota for all resources in that flavor automatically.
- **Beta (fine-grained)**: Administrators can additionally specify `nodeLabels`
  at the Cohort and ClusterQueue levels. The effective node set is the
  **intersection** of all configured label selectors (Cohort, ClusterQueue,
  ResourceFlavor), enabling per-scope narrowing of which nodes contribute to
  quota.

## Motivation

Currently, `nominalQuota` in a ClusterQueue or Cohort is a static value that
administrators must manually configure and update. When the underlying node pool
changes (e.g., monthly node onboarding, manual provisioning), administrators
must manually adjust quotas to reflect the new cluster capacity. This creates
several problems:

1. **Operational overhead**: Manually updating quotas for each provisioning event
   is error-prone and labor intensive.
2. **Quota drift**: If quotas are not updated promptly, Kueue may over-admit
   workloads (quota > actual capacity) or under-utilize the cluster (quota <
   actual capacity).
3. **Redundant configuration**: Capacity management is already handled at the
   node pool level. Requiring administrators to duplicate this information in
   ClusterQueue or Cohort configuration is redundant.
4. **ResourceFlavor already knows the nodes**: A ResourceFlavor specifies
   `nodeLabels` that associate it with a set of Nodes. However, Kueue does not
   use this information to compute how many resources those nodes actually provide.
   The administrator must separately and manually state the total quota.

### Goals

- Allow ClusterQueue and Cohort quotas to be automatically computed from the
  Allocatable resources of Nodes matching a ResourceFlavor's `nodeLabels`.
- (Beta) Support additional `nodeLabels` at CQ and Cohort levels to narrow the
  effective node set via intersection.
- Support configurable headroom to reserve resources for DaemonSets and
  non-Kueue workloads running alongside Kueue workloads on the same nodes.
- Ensure automatic quotas update dynamically as Nodes are added, removed, or
  change status.
- Be backward compatible — existing static `nominalQuota` configurations
  continue to work unchanged.
- Allow individual resources within a flavor to override with a manual
  `nominalQuota` (e.g., for custom resources not reported by nodes).

### Non-Goals

- Replace or deprecate static `nominalQuota`. Both modes will coexist.
- Automatically create or manage ResourceFlavors.
- Account for resources consumed by non-Kueue workloads beyond a configurable
  headroom percentage or fixed amount.
- Drive reactive autoscaling. This feature reflects current node capacity only.
  Reactive capacity provisioning via cluster-autoscaler is covered by
  ProvisioningRequest (KEP-1136).
- Introduce auto-detection for DRA Device resources. DRA quota is managed
  through DeviceClassMappings (KEP-2941).

## Proposal

### User Stories

#### Story 1

As a cluster administrator, I manage a GPU node pool. Each node has 8 GPUs and
is labeled `gpu-type=a100`. I already have a ResourceFlavor that uses this label.
I want Kueue to automatically compute GPU quota from those nodes without me
updating the ClusterQueue every time a node is added or removed.

```yaml
# Existing ResourceFlavor (unchanged)
apiVersion: kueue.x-k8s.io/v1beta2
kind: ResourceFlavor
metadata:
  name: gpu-nodes
spec:
  nodeLabels:
    gpu-type: a100

---
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: gpu-team-cq
spec:
  resourceGroups:
  - coveredResources: ["nvidia.com/gpu"]
    flavors:
    - name: gpu-nodes
      autoDetect: true   # uses gpu-nodes ResourceFlavor nodeLabels automatically
      resources:
      - name: nvidia.com/gpu
```

#### Story 2

As a platform engineer, I run a multi-tenant cluster where different teams use
different node pools. I want each team's ClusterQueue to automatically reflect
the capacity of their dedicated node pool, with 5% headroom reserved for system
DaemonSets running on those nodes.

```yaml
kind: ClusterQueue
metadata:
  name: team-alpha-cq
spec:
  resourceGroups:
  - coveredResources: ["cpu", "memory"]
    flavors:
    - name: team-alpha-nodes   # ResourceFlavor with nodeLabels: {team: alpha}
      autoDetect: true
      headroom:
        percentage: 5
      resources:
      - name: cpu              # both cpu and memory auto-computed
      - name: memory
      - name: example.com/token
        nominalQuota: 100      # not reported by nodes — manual override
```

#### Story 3

As a capacity administrator at a large organization, I onboard new node pools
monthly. I want total capacity to automatically land at the Cohort level as a
shared pool, so my team can then statically carve up `nominalQuota` across
ClusterQueues.

```yaml
# Cohort: shared pool — auto-detected from shared-nodes ResourceFlavor
apiVersion: kueue.x-k8s.io/v1beta2
kind: Cohort
metadata:
  name: org-cohort
spec:
  resourceGroups:
  - coveredResources: ["cpu", "memory"]
    flavors:
    - name: shared-nodes     # ResourceFlavor with nodeLabels: {node-pool: shared}
      autoDetect: true
      resources:
      - name: cpu
      - name: memory

---
# CQs borrow from the cohort shared pool
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: team-alpha-cq
spec:
  cohortName: org-cohort
  resourceGroups:
  - coveredResources: ["cpu", "memory"]
    flavors:
    - name: shared-nodes
      resources:
      - name: cpu
        nominalQuota: 0     # borrows from cohort's auto-detected pool
      - name: memory
        nominalQuota: 0
```

Note: the Cohort's `nominalQuota` represents additional shared resources on top
of CQ quotas. A CQ and its parent Cohort may both use `autoDetect` as long as
their ResourceFlavors' `nodeLabels` select **different** physical nodes, to
avoid double-counting.

#### Story 4

**(Beta)** As a cluster administrator managing a multi-region cluster, I have a
ResourceFlavor `gpu-nodes` that covers all A100 GPU nodes across all regions.
I want one ClusterQueue to automatically compute quota from only the `us-east`
region's GPU nodes, and another from `us-west`, without creating separate
ResourceFlavors per region.

Since a ClusterQueue can reference multiple flavors, the additional `nodeLabels`
for fine-grained filtering are placed at the **flavor level**, not the CQ level.

```yaml
# ResourceFlavor covers all A100 nodes across regions
kind: ResourceFlavor
metadata:
  name: gpu-nodes
spec:
  nodeLabels:
    gpu-type: a100

---
# CQ narrows to us-east by adding nodeLabels on the flavor entry (Beta)
kind: ClusterQueue
metadata:
  name: us-east-gpu-cq
spec:
  resourceGroups:
  - coveredResources: ["nvidia.com/gpu"]
    flavors:
    - name: gpu-nodes
      autoDetect: true
      nodeLabels:            # Beta: flavor-level label narrows this flavor's node set
        region: us-east
      resources:
      - name: nvidia.com/gpu
      # effective nodes: gpu-type=a100 AND region=us-east

---
kind: ClusterQueue
metadata:
  name: us-west-gpu-cq
spec:
  resourceGroups:
  - coveredResources: ["nvidia.com/gpu"]
    flavors:
    - name: gpu-nodes
      autoDetect: true
      nodeLabels:
        region: us-west
      resources:
      - name: nvidia.com/gpu
      # effective nodes: gpu-type=a100 AND region=us-west
```

### Notes/Constraints/Caveats

- **`autoDetect` and `nominalQuota` interaction**: When `autoDetect: true` is
  set at the flavor level, individual resource entries must not set `nominalQuota`
  unless they explicitly want to override auto-detection for that resource.
- **Headroom and Allocatable**: `node.Status.Allocatable` already equals total
  node capacity minus kubelet's `kube-reserved` and `system-reserved`, so those
  system reservations are implicitly handled. The `headroom` field is for
  DaemonSets and other non-Kueue pods that consume node resources beyond what
  kubelet reserves.
- **Cohort + CQ autoDetect overlap**: A Cohort and a CQ within it may both set
  `autoDetect: true` on the same flavor only if their ResourceFlavors' nodeLabels
  select **different** physical nodes. Overlapping node sets cause double-counting.
  This is the administrator's responsibility; Beta will add validation warnings
  for overlapping selectors.
- **Node readiness filtering**: By default only Nodes with condition `Ready=True`
  are counted. An optional `includeNotReady` field can override this.
- **Rate limiting**: The controller aggregates node events and re-computes quotas
  at a bounded frequency (e.g., every 15 seconds) to avoid excessive API writes.
- **Feature gate**: This feature is gated behind `AutomaticQuotaFromNodes`
  (disabled by default in Alpha).
- **Recommended use case**: This feature targets predictable, known capacity
  (fixed node pools, periodic onboarding). For reactive autoscaling via
  ProvisioningRequest, static `nominalQuota` remains recommended.

### Risks and Mitigations

- **Stale quota if Node watch is delayed**: Kueue already relies on informer
  caches for correctness. Risk is no different from existing Workload/Pod watches.
- **Resource churn causing frequent quota updates**: Mitigation: batch node
  events with a bounded reconciliation interval (e.g., 15 seconds). Only update
  status when the computed quota actually changes.
- **Conflicting selectors across ClusterQueues or between Cohort and CQ**:
  Overlapping node sets lead to over-committed quotas, same as manually
  overlapping quotas today. Beta will add validation warnings.
- **Security**: The controller needs `get`, `list`, `watch` on Nodes. Kueue
  already has these permissions for TAS. No additional RBAC needed.

## Design Details

### Alpha: Coarse-Grained Auto-Detection

#### API Changes (Alpha)

Add `autoDetect`, `headroom`, and `includeNotReady` at the **flavor level**
(`FlavorQuotas` struct) in `apis/kueue/v1beta2/clusterqueue_types.go`. This
struct is shared by both ClusterQueue and Cohort `resourceGroups`.

```go
type FlavorQuotas struct {
    // name of this flavor.
    Name ResourceFlavorReference `json:"name"`

    // autoDetect enables automatic quota computation for all resources in
    // this flavor. When true, Kueue watches Nodes matching the associated
    // ResourceFlavor's nodeLabels and computes nominalQuota as the sum of
    // their Allocatable values for each resource. Individual resources may
    // still override with nominalQuota for resources not reported by nodes.
    // Mutually exclusive with setting nominalQuota on all resources in this
    // flavor simultaneously.
    // +optional
    AutoDetect bool `json:"autoDetect,omitempty"`

    // headroom defines how much capacity to reserve from the auto-detected
    // total. Applies only when autoDetect is true.
    // Note: node.Status.Allocatable already accounts for kubelet kube-reserved
    // and system-reserved; headroom is for DaemonSets and other non-Kueue pods.
    // +optional
    Headroom *QuotaHeadroom `json:"headroom,omitempty"`

    // includeNotReady specifies whether Nodes without Ready=True should be
    // included in quota computation. Defaults to false.
    // Applies only when autoDetect is true.
    // +optional
    IncludeNotReady *bool `json:"includeNotReady,omitempty"`

    Resources []ResourceQuota `json:"resources,omitempty"`
}

// QuotaHeadroom configures how much capacity to reserve from the
// auto-detected total.
type QuotaHeadroom struct {
    // percentage specifies the percentage of total Allocatable to subtract.
    // Must be between 0 and 100.
    // +optional
    // +kubebuilder:validation:Minimum=0
    // +kubebuilder:validation:Maximum=100
    Percentage *int32 `json:"percentage,omitempty"`

    // fixed specifies a fixed quantity to subtract as headroom.
    // +optional
    Fixed *resource.Quantity `json:"fixed,omitempty"`
}
```

`ResourceQuota` is unchanged in Alpha. Individual resources may set `nominalQuota`
to override auto-detection for that specific resource (e.g., custom resources
not reported by node Allocatable).

#### Validation Rules (Alpha)

- `autoDetect: true` requires the `AutomaticQuotaFromNodes` feature gate.
- `headroom` and `includeNotReady` are only valid when `autoDetect: true`.
- Within `headroom`, at most one of `percentage` or `fixed` may be set.
- The associated ResourceFlavor must define `nodeLabels`; if `nodeLabels` is
  empty, `autoDetect: true` is a validation error.
- A resource entry must not set `nominalQuota` when `autoDetect: true` is set
  at the flavor level, unless it is explicitly overriding auto-detection for
  that resource (permitted but must be acknowledged via a dedicated field in Beta).

#### Status Extension

Add `detectedQuotas` to both `ClusterQueueStatus` and `CohortStatus` to expose
the computed quota values:

```go
type ClusterQueueStatus struct {
    // ... existing fields ...

    // detectedQuotas reports the most recently computed quota values
    // for resources using autoDetect. Allows administrators to observe
    // what Kueue has computed.
    // +optional
    DetectedQuotas []DetectedQuota `json:"detectedQuotas,omitempty"`
}

type DetectedQuota struct {
    // flavorName is the name of the ResourceFlavor.
    FlavorName ResourceFlavorReference `json:"flavorName"`

    // resourceName is the name of the resource.
    ResourceName corev1.ResourceName `json:"resourceName"`

    // quantity is the computed quota value after headroom subtraction.
    Quantity resource.Quantity `json:"quantity"`

    // nodeCount is the number of matching Nodes that contributed.
    NodeCount int32 `json:"nodeCount"`

    // lastUpdated is when the quota was last recomputed.
    LastUpdated metav1.Time `json:"lastUpdated"`
}
```

### Beta: Fine-Grained Node Label Intersection

#### API Changes (Beta)

Add an optional `nodeLabels` field at the **flavor level** (`FlavorQuotas`
struct) in both ClusterQueue and Cohort `resourceGroups`. Since a CQ or Cohort
can reference multiple flavors, placing `nodeLabels` at the flavor level allows
each flavor to independently narrow its node set.

```go
type FlavorQuotas struct {
    // name of this flavor.
    Name ResourceFlavorReference `json:"name"`

    // autoDetect enables automatic quota computation for all resources in
    // this flavor using the ResourceFlavor's nodeLabels. (Alpha field)
    // +optional
    AutoDetect bool `json:"autoDetect,omitempty"`

    // nodeLabels defines additional labels used to narrow the node set for
    // quota computation when autoDetect is true. The effective node set is
    // the intersection of the ResourceFlavor's nodeLabels and these labels.
    // This allows multiple CQs or Cohorts to reference the same ResourceFlavor
    // but compute quota from different node subsets (e.g., per-region).
    // Beta field; requires AutomaticQuotaFromNodes feature gate.
    // +optional
    NodeLabels map[string]string `json:"nodeLabels,omitempty"`

    // headroom reserves capacity from the auto-detected total. (Alpha field)
    // +optional
    Headroom *QuotaHeadroom `json:"headroom,omitempty"`

    // includeNotReady includes non-Ready nodes in computation. (Alpha field)
    // +optional
    IncludeNotReady *bool `json:"includeNotReady,omitempty"`

    Resources []ResourceQuota `json:"resources,omitempty"`
}
```

#### Intersection Semantics

`nodeLabels` is placed at the **flavor level**, so each flavor entry in a CQ or
Cohort can independently narrow its node set. The CQ and Cohort each compute
their quota independently — there is no cross-scope filtering.

```
# Node-set perspective (∩ = intersection of matching node sets):
CQ flavor effectiveNodes     = {nodes matching RF.nodeLabels} ∩ {nodes matching flavor.nodeLabels in CQ}
Cohort flavor effectiveNodes = {nodes matching RF.nodeLabels} ∩ {nodes matching flavor.nodeLabels in Cohort}
```

This allows administrators to:
- Use a single ResourceFlavor that covers a broad node class (e.g., all A100 nodes)
- Narrow quota computation per-CQ-flavor (e.g., `region=us-east`) or
  per-Cohort-flavor (e.g., `zone=prod`) without creating additional ResourceFlavors
- Have multiple CQs share one ResourceFlavor but each compute quota from a
  different regional or zonal subset of nodes

#### Validation Rules (Beta)

- `nodeLabels` at CQ or Cohort level is only meaningful when at least one flavor
  in the object has `autoDetect: true`. A warning is emitted otherwise.
- If a key appears in both CQ `nodeLabels` and ResourceFlavor `nodeLabels` with
  different values, the node must satisfy both — in practice this means no nodes
  match. This is flagged as a validation warning.
- Beta adds validation warnings when Cohort and CQ selectors (after intersection)
  still select overlapping node sets.

### Controller Design

#### Node Watcher

A new controller `AutoQuotaReconciler` watches Node objects using a shared
informer (reusing the same informer as TAS to avoid duplicate Node watches). It
maintains an in-memory index:

```
effectiveLabels -> set of matching nodes -> aggregated Allocatable per resource
```

When a Node is created, updated, or deleted, the controller determines which
ClusterQueues and Cohorts have affected `autoDetect` flavors and enqueues those
objects for reconciliation.

#### Reconciliation Loop

1. Node event arrives (create/update/delete)
2. Determine affected ClusterQueues and Cohorts by matching their effective label sets
3. Debounce: coalesce events within a ~15-second window
4. Recompute effective quota for affected flavors
5. Update internal cache (scheduler snapshot)
6. Update ClusterQueue/Cohort Status with `DetectedQuotas`
7. If quotas decreased, trigger scheduling cycle to evaluate preemptions

### Interaction with Existing Features

| Feature | Interaction |
|---|---|
| Borrowing/Lending | Works unchanged. The auto-detected value replaces `nominalQuota` in all calculations. `borrowingLimit` and `lendingLimit` remain relative to the effective quota. |
| Hierarchical Cohorts | SubtreeQuota accumulation uses the effective quota from auto-detection at both Cohort and CQ levels. No changes needed in `resource_node.go`. |
| Preemption | If detected quota decreases (nodes removed), this may trigger preemption of workloads exceeding the new quota. Consistent with existing behavior when an admin manually reduces `nominalQuota`. |
| Fair Sharing | Share calculations use the effective quota. No changes needed. |
| ProvisioningRequest | These features target different scenarios. `autoDetect` reflects **current** physical capacity for predictable node pools. ProvisioningRequest is for **reactive** autoscaling — requesting capacity on demand. Using both simultaneously for the same resource is not recommended. Once nodes provisioned by ProvisioningRequest come online, auto-detection naturally picks them up. |
| TAS (Topology Aware Scheduling) | TAS already watches Nodes via `rfReconciler`. The `AutoQuotaReconciler` reuses the same shared Node informer to avoid duplicate watches. |
| Cohort quota | Cohort `nominalQuota` represents additional shared resources on top of CQ quotas. `autoDetect: true` at the Cohort flavor level populates that shared pool automatically. Cohort and member CQ flavors must not select overlapping node sets to avoid double-counting. |

### RBAC Requirements

Kueue already has the necessary RBAC for Node watching:

```go
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
```

This is currently used by the TAS controller. The `AutoQuotaReconciler` reuses
the same permissions. No additional RBAC changes are needed.

### Test Plan

#### Prerequisite testing updates

Existing unit tests for `resourceNode.available()` and `potentialAvailable()`
should be extended to verify correct behavior when `nominalQuota` is dynamically
updated (simulating auto-detection).

#### Unit Tests

- `pkg/cache/scheduler/`: Verify that auto-detected quotas flow correctly into
  Snapshot, SubtreeQuota, and available calculations at both CQ and Cohort levels.
- `pkg/controller/core/`: Test the `AutoQuotaReconciler`:
  - Quota computation from ResourceFlavor `nodeLabels` (Alpha).
  - Headroom percentage and fixed calculations.
  - Node readiness filtering.
  - Per-resource `nominalQuota` override when `autoDetect: true`.
  - Debouncing behavior.
  - Status update with `DetectedQuotas`.
  - (Beta) Label intersection across Cohort, CQ, and ResourceFlavor.
- API validation: `autoDetect: true` requires non-empty ResourceFlavor `nodeLabels`.

#### Integration Tests

**Alpha:**
- ClusterQueue flavor with `autoDetect: true` correctly reflects node capacity.
- Cohort flavor with `autoDetect: true` correctly reflects shared node capacity;
  CQs with `nominalQuota: 0` borrow from it.
- Adding/removing Nodes updates the effective quota.
- Workload admission respects the auto-detected quota.
- Quota decrease triggers preemption of over-quota workloads.
- Mixed flavor: `autoDetect: true` with per-resource `nominalQuota` override.
- Interaction with borrowing/lending limits.

**Beta:**
- CQ-level `nodeLabels` narrows the node set (intersection with ResourceFlavor labels).
- Cohort-level `nodeLabels` narrows the node set for all member CQs.
- Three-way intersection: Cohort + CQ + ResourceFlavor `nodeLabels` all applied.
- Conflicting labels (same key, different value) result in empty node set and
  zero quota with a validation warning.

#### e2e Tests

- End-to-end test with a multi-node cluster verifying that ClusterQueue and
  Cohort quotas dynamically adjust as Nodes join and leave.
- (Beta) Multi-region cluster verifying that CQ-level `nodeLabels` correctly
  restricts quota computation to the appropriate regional node subset.

### Graduation Criteria

#### Alpha

- Feature gate `AutomaticQuotaFromNodes` (default disabled).
- `autoDetect`, `headroom`, and `includeNotReady` fields added to `FlavorQuotas`
  struct (shared by ClusterQueue and Cohort `resourceGroups`).
- `AutoQuotaReconciler` implemented using ResourceFlavor `nodeLabels` for node
  selection, integrated with scheduler cache and shared Node informer.
- Per-resource `nominalQuota` override supported when `autoDetect: true`.
- Status reporting via `DetectedQuotas` on ClusterQueue and Cohort.
- Unit and integration tests covering core Alpha functionality.
- Documentation of the new API fields and behavior.

#### Beta

- Feature gate default enabled.
- `nodeLabels` field added to `ClusterQueueSpec` and `CohortSpec` for
  fine-grained intersection-based node selection.
- Intersection semantics implemented and documented.
- Validation warnings for:
  - Overlapping node sets between Cohort and CQ `autoDetect` flavors.
  - Conflicting label values across scopes.
- Metrics:
  - `kueue_detected_quota` (gauge): current effective quota per object/flavor/resource.
  - `kueue_quota_detect_reconcile_duration_seconds` (histogram): reconcile latency.
- Performance testing in large clusters (1000+ Nodes).

#### GA

- Feature gate removed (always enabled).
- Stable API with no breaking changes.
- Production usage feedback incorporated.

## Implementation History

- 2026-04-23: KEP created based on issue [#10270](https://github.com/kubernetes-sigs/kueue/issues/10270).
- 2026-04-24: Revised based on review feedback:
  - Moved `autoDetect` from resource level to flavor level; uses ResourceFlavor
    `nodeLabels` directly (no duplicated `nodeSelector` field).
  - Extended support to Cohort level.
  - Split into Alpha (coarse-grained, flavor-level flag) and Beta (fine-grained,
    label intersection across Cohort/CQ/ResourceFlavor scopes).
  - Clarified headroom vs. Allocatable semantics and ProvisioningRequest interaction.
