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
    - [Alpha: NodeQuotaPolicy CRD](#alpha-nodequotapolicy-crd)
      - [API: NodeQuotaPolicy](#api-nodequotapolicy)
      - [ClusterQueue and Cohort API Changes](#clusterqueue-and-cohort-api-changes)
      - [Validation Rules (Alpha)](#validation-rules-alpha)
      - [Status Reporting](#status-reporting)
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
  - [Open Questions](#open-questions)
    - [OQ-1: Initial value of <code>nominalQuota</code> when managed by NodeQuotaPolicy](#oq-1-initial-value-of-nominalquota-when-managed-by-nodequotapolicy)
- [NodeQuotaPolicy authored separately](#nodequotapolicy-authored-separately)
<!-- /toc -->

## Summary

This KEP proposes a new cluster-scoped CRD, `NodeQuotaPolicy`, that enables automatic quota computation for ClusterQueues and Cohorts based on physical node capacity. For this design, a dedicated controller reads `NodeQuotaPolicy` objects, watches the Nodes they select, aggregates `node.Status.Allocatable`, and writes the computed `nominalQuota` back into the target ClusterQueue or Cohort.

This design follows the Kubernetes principle of **separation of concerns**: ClusterQueue and Cohort remain responsible only for declaring what the quota is (a single numeric value), while `NodeQuotaPolicy` and its controller are responsible for how that quota is derived. The analogy is kubelet computing `node.Status.Allocatable` independently from the Node object's schema — the Node does not embed the calculation logic.

The feature is delivered in two phases:

- **Alpha**: A `NodeQuotaPolicy` CRD with `targetRef` (ClusterQueue or Cohort) and per-flavor rules. The controller uses the referenced ResourceFlavor's `nodeLabels` to select nodes and writes the summed `Allocatable` as `nominalQuota`.
- **Beta**: An optional `nodeLabels` field in `FlavorNodeQuotaPolicy` allows administrators to narrow the effective node set via intersection with the ResourceFlavor's `nodeLabels`, enabling per-CQ regional or zonal quota subsetting without creating additional ResourceFlavors.

## Motivation

Currently, `nominalQuota` in a ClusterQueue or Cohort is a static value that administrators must manually configure and update. When the underlying node pool changes (e.g., monthly node onboarding, manual provisioning), administrators must manually adjust quotas to reflect the new cluster capacity. This creates several problems:

1. **Operational overhead**: Manually updating quotas for each provisioning event is error-prone and labor intensive.
2. **Quota drift**: If quotas are not updated promptly, Kueue may over-admit workloads (quota > actual capacity) or under-utilize the cluster (quota < actual capacity).
3. **Redundant configuration**: Capacity management is already handled at the node pool level. Requiring administrators to duplicate this information in ClusterQueue or Cohort configuration is redundant.
4. **ResourceFlavor already knows the nodes**: A ResourceFlavor specifies `nodeLabels` that associate it with a set of Nodes. However, Kueue does not use this information to compute how many resources those nodes actually provide. The administrator must separately and manually state the total quota.

### Goals

- Introduce a `NodeQuotaPolicy` CRD that declares how to derive `nominalQuota` for a target ClusterQueue or Cohort from the `Allocatable` resources of matching Nodes.
- Keep ClusterQueue and Cohort APIs unchanged with respect to quota declaration: `nominalQuota` remains the single authoritative value consumed by the scheduler.
- Support configurable headroom to reserve resources for DaemonSets and non-Kueue workloads running alongside Kueue workloads on the same nodes.
- Ensure computed quotas update dynamically as Nodes are added, removed, or change status.
- Be backward compatible — existing static `nominalQuota` configurations continue to work unchanged.
- (Beta) Allow `nodeLabels` in `NodeQuotaPolicy` to narrow the node set beyond what the ResourceFlavor alone selects, enabling per-region or per-zone quota subsetting.

### Non-Goals

- Replace or deprecate static `nominalQuota`. Both modes coexist.
- Automatically create or manage ResourceFlavors.
- Account for resources consumed by non-Kueue workloads beyond a configurable headroom percentage or fixed amount.
- Drive reactive autoscaling. This feature reflects current node capacity only. Reactive capacity provisioning via cluster-autoscaler is covered by ProvisioningRequest (KEP-1136).
- Introduce auto-detection for DRA Device resources. DRA quota is managed through DeviceClassMappings (KEP-2941).

## Proposal

### User Stories

#### Story 1

As a cluster administrator, I manage a GPU node pool. Each node has 8 GPUs and is labeled `gpu-type=a100`. I already have a ResourceFlavor that uses this label. I want Kueue to automatically compute GPU quota from those nodes without me updating the ClusterQueue every time a node is added or removed.

```yaml
# Existing ResourceFlavor — unchanged
apiVersion: kueue.x-k8s.io/v1beta2
kind: ResourceFlavor
metadata:
  name: gpu-nodes
spec:
  nodeLabels:
    gpu-type: a100

---
# ClusterQueue — unchanged, nominalQuota will be written by the controller
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: gpu-team-cq
spec:
  resourceGroups:
  - coveredResources: ["nvidia.com/gpu"]
    flavors:
    - name: gpu-nodes
      resources:
      - name: nvidia.com/gpu
        nominalQuota: "0"   # placeholder; NodeQuotaPolicy controller overwrites this

---
# NodeQuotaPolicy — declares how to compute nominalQuota for gpu-team-cq
apiVersion: kueue.x-k8s.io/v1beta2
kind: NodeQuotaPolicy
metadata:
  name: gpu-team-auto-quota
spec:
  targetRef:
    kind: ClusterQueue
    name: gpu-team-cq
  flavorPolicies:
  - flavorName: gpu-nodes   # uses gpu-nodes ResourceFlavor nodeLabels: {gpu-type: a100}
```

#### Story 2

As a platform engineer, I run a multi-tenant cluster where different teams use different node pools. I want each team's ClusterQueue to automatically reflect the capacity of their dedicated node pool, with 5% headroom reserved for system DaemonSets running on those nodes. One resource (`example.com/token`) is not reported by nodes and must be set manually.

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: NodeQuotaPolicy
metadata:
  name: team-alpha-auto-quota
spec:
  targetRef:
    kind: ClusterQueue
    name: team-alpha-cq
  flavorPolicies:
  - flavorName: team-alpha-nodes   # ResourceFlavor nodeLabels: {team: alpha}
    headroomPercentage: 5          # reserve 5% for DaemonSets
    resources:
    - name: example.com/token      # not reported by nodes — fixed manual value
      nominalQuota: "100"

---
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: team-alpha-cq
spec:
  resourceGroups:
  - coveredResources: ["cpu", "memory", "example.com/token"]
    flavors:
    - name: team-alpha-nodes
      resources:
      - name: cpu
        nominalQuota: "0"          # overwritten by controller
      - name: memory
        nominalQuota: "0"          # overwritten by controller
      - name: example.com/token
        nominalQuota: "0"          # overwritten by resourceOverride in NodeQuotaPolicy
```

#### Story 3

As a capacity administrator at a large organization, I onboard new node pools monthly. I want total capacity to automatically land at the Cohort level as a shared pool, so my team can then statically carve up `nominalQuota` across ClusterQueues.

```yaml
# NodeQuotaPolicy targets the Cohort directly
apiVersion: kueue.x-k8s.io/v1beta2
kind: NodeQuotaPolicy
metadata:
  name: org-cohort-auto-quota
spec:
  targetRef:
    kind: Cohort
    name: org-cohort
  flavorPolicies:
  - flavorName: shared-nodes   # ResourceFlavor nodeLabels: {node-pool: shared}

---
# Cohort: nominalQuota written by controller
apiVersion: kueue.x-k8s.io/v1beta2
kind: Cohort
metadata:
  name: org-cohort
spec:
  resourceGroups:
  - coveredResources: ["cpu", "memory"]
    flavors:
    - name: shared-nodes
      resources:
      - name: cpu
        nominalQuota: "0"
      - name: memory
        nominalQuota: "0"

---
# CQs borrow from the cohort's auto-computed shared pool
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
        nominalQuota: "0"     # borrows from cohort's auto-detected pool
      - name: memory
        nominalQuota: "0"
```

Note: the Cohort's `nominalQuota` represents additional shared resources on top of CQ quotas. A CQ and its parent Cohort may both be targeted by separate `NodeQuotaPolicy` objects as long as their ResourceFlavors' `nodeLabels` select **different** physical nodes to avoid double-counting.

#### Story 4

**(Beta)** As a cluster administrator managing a multi-region cluster, I have a ResourceFlavor `gpu-nodes` that covers all A100 GPU nodes across all regions. I want one ClusterQueue to automatically compute quota from only the `us-east` region's GPU nodes, and another from `us-west`, without creating separate ResourceFlavors per region.

```yaml
# ResourceFlavor covers all A100 nodes across regions
apiVersion: kueue.x-k8s.io/v1beta2
kind: ResourceFlavor
metadata:
  name: gpu-nodes
spec:
  nodeLabels:
    gpu-type: a100

---
# NodeQuotaPolicy for us-east: narrows to region=us-east via nodeLabels (Beta)
apiVersion: kueue.x-k8s.io/v1beta2
kind: NodeQuotaPolicy
metadata:
  name: us-east-gpu-quota
spec:
  targetRef:
    kind: ClusterQueue
    name: us-east-gpu-cq
  flavorPolicies:
  - flavorName: gpu-nodes
    nodeLabels:
      region: us-east        # effective nodes: gpu-type=a100 AND region=us-east

---
# NodeQuotaPolicy for us-west: narrows to region=us-west via nodeLabels (Beta)
apiVersion: kueue.x-k8s.io/v1beta2
kind: NodeQuotaPolicy
metadata:
  name: us-west-gpu-quota
spec:
  targetRef:
    kind: ClusterQueue
    name: us-west-gpu-cq
  flavorPolicies:
  - flavorName: gpu-nodes
    nodeLabels:
      region: us-west        # effective nodes: gpu-type=a100 AND region=us-west

---
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: us-east-gpu-cq
spec:
  resourceGroups:
  - coveredResources: ["nvidia.com/gpu"]
    flavors:
    - name: gpu-nodes
      resources:
      - name: nvidia.com/gpu
        nominalQuota: "0"

---
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: us-west-gpu-cq
spec:
  resourceGroups:
  - coveredResources: ["nvidia.com/gpu"]
    flavors:
    - name: gpu-nodes
      resources:
      - name: nvidia.com/gpu
        nominalQuota: "0"
```

### Notes/Constraints/Caveats

- **Separation of concerns**: `NodeQuotaPolicy` owns the computation logic; ClusterQueue and Cohort own only the quota value. This mirrors the kubelet pattern where `node.Status.Allocatable` is computed externally and written into the Node object — the Node schema does not embed the computation.
- **Headroom and Allocatable**: `node.Status.Allocatable` already equals total node capacity minus kubelet's `kube-reserved` and `system-reserved`, so those system reservations are implicitly handled. Headroom (`headroomPercentage`, or per-resource `fixedHeadroom`/`percentageHeadroom`) is for DaemonSets and other non-Kueue pods that consume node resources beyond what kubelet reserves.
- **Cohort + CQ NodeQuotaPolicy overlap**: A Cohort and a CQ within it may each be targeted by a `NodeQuotaPolicy` only if their respective ResourceFlavors' effective node sets (after label intersection) are **disjoint**; overlapping node sets double-count physical capacity. In Alpha this cannot arise between two policies that target different ResourceFlavors, because flavors are already expected to select disjoint nodes — so there is effectively no new overlap surface. The surface appears in Beta, where `flavorPolicy.nodeLabels` can sub-select within a shared flavor. Detection is reactive, not a webhook check: on overlap the controller sets `Ready=False` with reason `OverlappingNodeSet` on the affected policies and withholds the quota write (keeping the last-known-good value) rather than silently over-provisioning.
- **Node readiness filtering**: By default only Nodes with condition `Ready=True` are counted. The `includeNotReady` field in `FlavorNodeQuotaPolicy` can override this.
- **Rate limiting**: The controller aggregates node events and re-computes quotas at a bounded frequency (e.g., every 15 seconds) to avoid excessive API writes to ClusterQueue or Cohort.
- **Feature gate**: This feature is gated behind `AutomaticQuotaFromNodes` (disabled by default in Alpha).
- **Recommended use case**: This feature targets predictable, known capacity (fixed node pools, periodic onboarding). For reactive autoscaling via ProvisioningRequest, static `nominalQuota` remains recommended.

### Risks and Mitigations

- **Stale quota**: two latency sources contribute. Informer cache lag is involuntary and sub-second — the same as existing Workload/Pod watches. The intentional enqueue debounce (the batch window) is new to this controller and adds to it, so worst-case staleness is roughly informer lag + debounce window + write/scheduler-refresh. The risk is asymmetric: on scale-up the quota lags *below* real capacity, so Kueue under-admits (safe/conservative); on scale-down it lags *above*, so it can transiently over-admit until the controller writes the lower value, which then converges. For the target use case (predictable, slow-changing node pools) a bounded few-second lag is acceptable, and the window is tunable; the controller may also debounce increases while reacting faster to decreases if the scale-down window proves too loose.
- **Resource churn causing frequent quota updates**: Mitigation: batch node events with a bounded reconciliation interval (e.g., 15 seconds). Only write to the target object when the computed quota actually changes.
- **Conflicting selectors across ClusterQueues or between Cohort and CQ**: Overlapping effective node sets lead to over-committed quotas, same as manually overlapping quotas today. The controller detects this at reconcile and sets `Ready=False`/`OverlappingNodeSet` on the affected policies while withholding the write, instead of silently over-provisioning (Beta; the Alpha node set is the whole flavor, which is already expected to be disjoint).
- **Concurrent writes**: The controller writes via server-side apply with `ForceOwnership` under field manager `kueue.x-k8s.io/node-quota-policy`. This scopes the write to just the `nominalQuota` fields it manages (it does not disturb `borrowingLimit`, other flavors, or labels) while remaining authoritative: if an administrator manually edits a managed `nominalQuota`, the force-apply reclaims it and overwrites on the next reconcile (same behavior as HPA overwriting `spec.replicas`). This is in `Apply` mode; in `Recommend` mode the controller does not write the target at all (see Quota vending / external source of truth).
- **Security**: The controller needs `get`, `list`, `watch` on Nodes and `patch` on ClusterQueues and Cohorts. Node watching is already granted to Kueue for TAS. Patch on ClusterQueue/Cohort is already granted for existing controllers. No additional RBAC changes are needed.

## Design Details

### Alpha: NodeQuotaPolicy CRD

The core design principle is that **ClusterQueue and Cohort APIs are not modified**. All auto-detection configuration lives in the new `NodeQuotaPolicy` CRD. The controller writes computed values into `nominalQuota` fields of the target object, just as the HPA controller writes `spec.replicas` into a Deployment.

#### API: NodeQuotaPolicy

New file: `apis/kueue/v1beta2/nodequotapolicy_types.go`

```go
// NodeQuotaPolicy drives automatic quota computation for a ClusterQueue or
// Cohort. A dedicated controller watches this object together with the Nodes
// selected by the referenced ResourceFlavors, aggregates their Allocatable
// resources, and writes the result back into the target object's nominalQuota
// fields.
//
// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:scope=Cluster,shortName={nqp}
// +kubebuilder:subresource:status
type NodeQuotaPolicy struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`

    Spec   NodeQuotaPolicySpec   `json:"spec,omitempty"`
    Status NodeQuotaPolicyStatus `json:"status,omitempty"`
}

// NodeQuotaPolicySpec defines the desired state of NodeQuotaPolicy.
type NodeQuotaPolicySpec struct {
    // targetRef identifies the ClusterQueue or Cohort whose nominalQuota
    // this policy manages. Exactly one NodeQuotaPolicy may target a given
    // (kind, name) pair; the controller rejects duplicates.
    // +required
    TargetRef NodeQuotaPolicyTargetRef `json:"targetRef"`

    // mode controls whether the controller actuates the computed quota.
    // In "Apply" (the default) the controller writes the computed nominalQuota
    // to the target object. In "Recommend" the controller only records the
    // computed values in status.computedQuotas and does not modify the target,
    // so an external system of record (e.g. GitOps/Terraform) can consume the
    // proposal and apply it through its own pipeline.
    // +optional
    // +kubebuilder:validation:Enum=Apply;Recommend
    // +kubebuilder:default=Apply
    Mode NodeQuotaPolicyMode `json:"mode,omitempty"`

    // flavorPolicies defines per-flavor quota computation rules. Each entry
    // corresponds to one flavor in the target object's resourceGroups.
    // The controller only writes nominalQuota for (flavor, resource) pairs
    // that appear both in flavorPolicies and in the target object's spec.
    // +listType=map
    // +listMapKey=flavorName
    // +kubebuilder:validation:MinItems=1
    // +kubebuilder:validation:MaxItems=64
    // +required
    FlavorPolicies []FlavorNodeQuotaPolicy `json:"flavorPolicies"`
}

// NodeQuotaPolicyMode controls whether the controller actuates the computed quota.
type NodeQuotaPolicyMode string

const (
    // NodeQuotaPolicyModeApply writes the computed nominalQuota to the target.
    NodeQuotaPolicyModeApply NodeQuotaPolicyMode = "Apply"
    // NodeQuotaPolicyModeRecommend records the computed values in status only,
    // leaving the target object untouched for an external system to actuate.
    NodeQuotaPolicyModeRecommend NodeQuotaPolicyMode = "Recommend"
)

// NodeQuotaPolicyTargetRef identifies the object whose nominalQuota is managed.
type NodeQuotaPolicyTargetRef struct {
    // kind is the type of the target object.
    // +kubebuilder:validation:Enum=ClusterQueue;Cohort
    // +required
    Kind string `json:"kind"`

    // name is the name of the target ClusterQueue or Cohort.
    // +required
    // +kubebuilder:validation:MaxLength=253
    // +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
    Name string `json:"name"`
}

// FlavorNodeQuotaPolicy defines how to compute nominalQuota for one flavor.
type FlavorNodeQuotaPolicy struct {
    // flavorName references the ResourceFlavor whose nodeLabels are used as
    // the base node selector. A ResourceFlavor with no nodeLabels is allowed and
    // matches all nodes (the "default flavor" case); its computed quota is then
    // the sum of Allocatable across every matching node. Note that a match-all
    // flavor also counts control-plane/unschedulable nodes unless they are
    // filtered out (see node filtering); use headroom or per-resource rules to
    // adjust if needed.
    // +required
    FlavorName ResourceFlavorReference `json:"flavorName"`

    // nodeLabels optionally narrows the effective node set by intersecting with
    // the ResourceFlavor's nodeLabels. Useful when multiple ClusterQueues share
    // one ResourceFlavor but each targets a different node subset (e.g., per
    // region or zone). Beta field; ignored in Alpha.
    // +optional
    // +mapType=atomic
    // +kubebuilder:validation:MaxProperties=8
    NodeLabels map[string]string `json:"nodeLabels,omitempty"`

    // headroomPercentage optionally reserves a percentage of the computed total
    // across all resources of this flavor, for DaemonSets and other non-Kueue
    // pods. Being dimensionless, it applies uniformly to every resource.
    // node.Status.Allocatable already accounts for kubelet kube-reserved and
    // system-reserved; this is additional. A per-resource rule in `resources`
    // overrides this default for that resource.
    // +optional
    // +kubebuilder:validation:Minimum=0
    // +kubebuilder:validation:Maximum=100
    HeadroomPercentage *int32 `json:"headroomPercentage,omitempty"`

    // includeNotReady controls whether Nodes without the Ready=True condition
    // contribute to quota computation. Defaults to false (only Ready nodes).
    // +optional
    IncludeNotReady *bool `json:"includeNotReady,omitempty"`

    // resources defines per-resource quota rules: pin a fixed nominalQuota, or
    // apply a fixed/percentage headroom that overrides headroomPercentage for
    // that resource. Resources not listed here are computed from node
    // Allocatable (minus headroomPercentage, if set).
    // +optional
    // +listType=map
    // +listMapKey=name
    // +kubebuilder:validation:MaxItems=64
    Resources []ResourceQuotaRule `json:"resources,omitempty"`
}

// ResourceQuotaRule sets the quota policy for one resource within a flavor.
// Exactly one of nominalQuota, fixedHeadroom, or percentageHeadroom may be set.
// +kubebuilder:validation:XValidation:rule="[has(self.nominalQuota), has(self.fixedHeadroom), has(self.percentageHeadroom)].filter(x, x).size() <= 1",message="at most one of nominalQuota, fixedHeadroom, or percentageHeadroom may be set"
type ResourceQuotaRule struct {
    // name is the Kubernetes resource name (e.g., nvidia.com/gpu, cpu, memory,
    // or a custom resource such as example.com/token).
    // +required
    Name corev1.ResourceName `json:"name"`

    // nominalQuota pins this resource to a fixed value, bypassing node-based
    // computation. Use for resources not reported in node.Status.Allocatable
    // (e.g., example.com/token). Mutually exclusive with the headroom fields.
    // +optional
    NominalQuota *resource.Quantity `json:"nominalQuota,omitempty"`

    // fixedHeadroom subtracts a fixed, correctly-dimensioned quantity from this
    // resource's node-derived total (floored at zero).
    // +optional
    FixedHeadroom *resource.Quantity `json:"fixedHeadroom,omitempty"`

    // percentageHeadroom subtracts a percentage from this resource's node-derived
    // total, overriding the flavor-wide headroomPercentage for this resource.
    // +optional
    // +kubebuilder:validation:Minimum=0
    // +kubebuilder:validation:Maximum=100
    PercentageHeadroom *int32 `json:"percentageHeadroom,omitempty"`
}

// NodeQuotaPolicyStatus records the results of the most recent reconciliation.
type NodeQuotaPolicyStatus struct {
    // conditions describe the current state of this policy. A Ready condition
    // is set to True after the first successful write to the target object.
    // +optional
    // +listType=map
    // +listMapKey=type
    // +kubebuilder:validation:MaxItems=8
    Conditions []metav1.Condition `json:"conditions,omitempty"`

    // computedQuotas reports the nominalQuota values most recently written to
    // the target object, one entry per (flavor, resource) pair. Entries are
    // keyed by (flavorName, resourceName), giving a deterministic order and
    // stable server-side-apply merge semantics so the status does not churn on
    // every reconcile.
    // +optional
    // +listType=map
    // +listMapKey=flavorName
    // +listMapKey=resourceName
    // +kubebuilder:validation:MaxItems=256
    ComputedQuotas []ComputedFlavorQuota `json:"computedQuotas,omitempty"`
}

// ComputedFlavorQuota captures the output of one reconciliation cycle for a
// single (flavor, resource) pair.
type ComputedFlavorQuota struct {
    // flavorName is the ResourceFlavor this entry belongs to.
    FlavorName ResourceFlavorReference `json:"flavorName"`

    // resourceName is the Kubernetes resource (e.g., nvidia.com/gpu).
    ResourceName corev1.ResourceName `json:"resourceName"`

    // quantity is the nominalQuota value written to the target object,
    // after headroom subtraction.
    Quantity resource.Quantity `json:"quantity"`

    // nodeCount is the number of matching Nodes that contributed to the sum.
    NodeCount int32 `json:"nodeCount"`

    // lastUpdated is the time at which quantity last changed, not the last
    // reconcile time. It is only bumped when the computed value actually
    // changes, so it does not advance on no-op reconciles.
    LastUpdated metav1.Time `json:"lastUpdated"`
}

// +kubebuilder:object:root=true

// NodeQuotaPolicyList contains a list of NodeQuotaPolicy.
type NodeQuotaPolicyList struct {
    metav1.TypeMeta `json:",inline"`
    metav1.ListMeta `json:"metadata,omitempty"`
    Items           []NodeQuotaPolicy `json:"items"`
}

func init() {
    SchemeBuilder.Register(&NodeQuotaPolicy{}, &NodeQuotaPolicyList{})
}
```

#### ClusterQueue and Cohort API Changes

**No spec changes** to `ClusterQueueSpec`, `CohortSpec`, `FlavorQuotas`, or `ResourceQuota` in Alpha. The `nominalQuota` field is written by the `NodeQuotaPolicyReconciler` via a server-side apply patch, exactly as an HPA controller patches `spec.replicas` on a Deployment.

The only API addition is to `CohortStatus`, which gains a `conditions` list so the controller can surface a target-side `NodeQuotaManaged` condition (see [Status Reporting](#status-reporting)). `ClusterQueueStatus` already has `conditions`, so no ClusterQueue change is needed:

```go
// CohortStatus gains a conditions list (ClusterQueueStatus already has one).
type CohortStatus struct {
    // ... existing fields ...

    // conditions hold the latest available observations of the Cohort's state.
    // +optional
    // +listType=map
    // +listMapKey=type
    // +kubebuilder:validation:MaxItems=8
    Conditions []metav1.Condition `json:"conditions,omitempty"`
}
```

The Kueue scheduler continues to read `nominalQuota` from the CQ or Cohort spec as today. No scheduler changes are required.

#### Validation Rules (Alpha)

Static, single-object checks are enforced by the `NodeQuotaPolicy` admission webhook:

- `targetRef.kind` must be `ClusterQueue` or `Cohort` (enum).
- `flavorPolicies` must not contain duplicate `flavorName` entries (already enforced by the `+listMapKey=flavorName` list-map semantics).
- Within each `resources[]` rule, at most one of `nominalQuota`, `fixedHeadroom`, or `percentageHeadroom` may be set (CEL rule on the struct).
- `nodeLabels` (Beta field) must be empty in Alpha; the webhook rejects it unless the `AutomaticQuotaFromNodes` feature gate is at Beta or later.

Cross-object and cluster-state-dependent checks are **not** done in the webhook — they depend on other objects (ResourceFlavors, the target's spec, other policies) or live Node state, which a webhook cannot gate soundly, since the referenced object may be created or change moments later. (This mirrors how a ClusterQueue referencing a missing flavor is handled by the controller as `Active=False`/`FlavorNotFound`, not rejected at admission.) Instead the controller detects them at reconcile and reports them on `NodeQuotaPolicy.status.conditions`:

- **`FlavorNotFound`** — a referenced `flavorName` has no matching ResourceFlavor. *Fatal*: withhold writes, set `Ready=False`, and re-reconcile when the flavor appears.
- **`FlavorNotInTarget`** — the flavor exists but is not used by the target's `resourceGroups`. *Non-fatal*: per the intersection rule these pairs are a no-op; write the valid flavors and report the dangling one.
- **`ResourceNotCovered`** — a `resources[]` rule names a resource the target does not cover. *Non-fatal*: skip that resource, write the covered ones, and report.
- **`DuplicateTarget`** — another `NodeQuotaPolicy` already targets the same `(kind, name)`. The later policy sets `Ready=False` and withholds writes. (Handled here rather than in the webhook, which would otherwise have to list all policies.)
- **`OverlappingNodeSet`** — (Beta) another policy's effective node set overlaps this one's for the same flavor; see [Notes/Constraints/Caveats](#notesconstraintscaveats).

In short: *fatal* reasons (the policy cannot compute a value) withhold all of the policy's writes; *non-fatal* reasons (a dangling or no-op entry) still apply the valid `(flavor, resource)` pairs and surface the offending entry in the condition message.

#### Status Reporting

After each successful reconciliation the controller updates `NodeQuotaPolicy.status`:

```yaml
# kubectl get nqp gpu-team-auto-quota -o yaml
status:
  conditions:
  - type: Ready
    status: "True"
    reason: QuotaApplied
    message: "Successfully wrote nominalQuota to ClusterQueue gpu-team-cq"
    lastTransitionTime: "2026-04-30T10:00:00Z"
  computedQuotas:
  - flavorName: gpu-nodes
    resourceName: nvidia.com/gpu
    quantity: "64"        # 8 nodes × 8 GPUs
    nodeCount: 8
    lastUpdated: "2026-04-30T10:00:00Z"
```

The two timestamps in this status have distinct semantics:

- **`conditions[Ready].lastTransitionTime`** changes only when the `Ready`
  condition's `status` transitions (e.g. `True`↔`False`). The controller sets it
  via the standard `apimeta.SetStatusCondition` helper, so a quota recompute that
  leaves `Ready=True`, or a `reason`/`message`-only update, does **not** move it.
- **`computedQuotas[].lastUpdated`** records when that entry's `quantity` last
  changed, and is bumped only on an actual value change (never on a no-op reconcile).

Status carries current observed state only — the computed values plus `nodeCount`
(how many Nodes contributed). The per-reconcile narrative (e.g. "evaluated 47
Nodes, 3 filtered as NotReady") belongs in controller logs/events, not in status.

Administrators can inspect computed values without reading the CQ:

```bash
kubectl get nqp gpu-team-auto-quota -o jsonpath='{.status.computedQuotas}'
```

**Target-side visibility (ClusterQueue / Cohort).** So operators can see — on the
managed object itself — that its quota is controller-managed and by which policy,
the controller also writes a lightweight `NodeQuotaManaged` condition onto the
target ClusterQueue or Cohort:

```yaml
# kubectl get clusterqueue gpu-team-cq -o yaml
status:
  conditions:
  - type: NodeQuotaManaged
    status: "True"
    reason: NodeQuotaPolicyApplied
    message: "nominalQuota managed by NodeQuotaPolicy gpu-team-auto-quota; last changed 2026-04-30T10:00:00Z"
    lastTransitionTime: "2026-04-30T10:00:00Z"
```

This is deliberately a pointer, not a mirror: the condition names the managing
policy and last-change time, while the detailed per-(flavor, resource) values stay
on `NodeQuotaPolicy.status.computedQuotas` (no duplication, and no extra status
churn on the target). Two complementary signals come for free: the SSA
`managedFields` on the target already record that `nominalQuota` is owned by the
`kueue.x-k8s.io/node-quota-policy` field manager, and a printer column on
`NodeQuotaPolicy` surfaces the policy→target mapping.

### Beta: Fine-Grained Node Label Intersection

#### API Changes (Beta)

The `nodeLabels` field in `FlavorNodeQuotaPolicy` (already present in the Alpha struct definition above) becomes active in Beta. No additional Go struct changes are needed; the field is gated by the feature gate at validation time.

The intersection logic is:

```
effectiveNodes = {nodes matching RF.nodeLabels} ∩ {nodes matching NQP flavorPolicy.nodeLabels}
```

Each `FlavorNodeQuotaPolicy` computes its node set independently, so two `NodeQuotaPolicy` objects targeting different CQs but referencing the same ResourceFlavor can each narrow to a different regional subset.

#### Intersection Semantics

```
# Example: RF covers all A100 nodes; NQP narrows per region
RF.nodeLabels      = {gpu-type: a100}
NQP.nodeLabels     = {region: us-east}
effectiveNodes     = nodes where gpu-type=a100 AND region=us-east
```

If a key appears in both `RF.nodeLabels` and `NQP flavorPolicy.nodeLabels` with **different** values, no nodes can satisfy both selectors simultaneously, resulting in `quantity: "0"` and a `Condition` warning on the `NodeQuotaPolicy`.

#### Validation Rules (Beta)

- `nodeLabels` in `FlavorNodeQuotaPolicy` requires the `AutomaticQuotaFromNodes` feature gate at Beta or later.
- A warning condition is emitted on `NodeQuotaPolicy` when the intersection of `RF.nodeLabels` and `flavorPolicy.nodeLabels` produces an empty node set.
- Beta adds conflict detection: if two `NodeQuotaPolicy` objects targeting different CQs or Cohorts within the same Cohort tree have overlapping effective node sets for the same flavor, the controller sets `Ready=False` with reason `OverlappingNodeSet` on both policies and withholds their writes (double-counting risk).

### Controller Design

#### Node Watcher

A new controller `NodeQuotaPolicyReconciler` watches:

- `NodeQuotaPolicy` objects (primary reconcile trigger)
- `Node` objects via a shared informer (reusing the same informer as TAS to avoid duplicate Node watches)
- `ResourceFlavor` objects (to pick up `nodeLabels` changes)
- `ClusterQueue` and `Cohort` objects (to detect when the target is created or deleted)

The controller maintains an in-memory reverse index:

```
effectiveLabelSet → set of NodeQuotaPolicy names that use this label set
```

When a Node event arrives, the controller determines which label sets match the node, looks up the affected `NodeQuotaPolicy` names, and enqueues them for reconciliation. Node events are predicate-filtered so that only changes to `Allocatable`, labels, or readiness trigger work (not routine heartbeats), and enqueueing is coalesced via `AddAfter(req, batchPeriod)` with the workqueue deduplicating by key — so a burst of Node changes collapses into a single reconcile per policy. This is the same pattern TAS uses (`constants.UpdatesBatchPeriod`) and avoids waiting inside `Reconcile`, which would block a worker.

#### Reconciliation Loop

```
Trigger: NodeQuotaPolicy / Node / ResourceFlavor / ClusterQueue / Cohort event
         (Node events are coalesced at enqueue time — see Node Watcher)
         ↓
1.  Load NodeQuotaPolicy spec (targetRef + flavorPolicies).
2.  For each FlavorNodeQuotaPolicy:
    a. Fetch referenced ResourceFlavor → read nodeLabels (base selector).
    b. (Beta) Intersect base selector with flavorPolicy.nodeLabels.
    c. List all Nodes matching the effective label set.
    d. Filter: exclude non-Ready nodes unless includeNotReady=true.
    e. Sum node.Status.Allocatable for each resource.
    f. Apply per-resource rules: pin nominalQuota where set, else subtract
       fixedHeadroom / percentageHeadroom (falling back to the flavor-wide
       headroomPercentage), flooring at zero.
3.  Compare computed quota map against NodeQuotaPolicy.status.computedQuotas.
    If unchanged, stop (no write to target object).
4.  Pre-check target invariants: the computed nominalQuota for each
    (flavor, resource) must be >= any existing lendingLimit on the target
    (webhook-enforced). On violation, withhold the write and set Ready=False
    (reason LendingLimitExceedsComputedQuota) instead of emitting a patch the
    target's webhook would reject.
5.  If mode is Recommend, stop here (values are recorded in status only). If mode
    is Apply, patch the target ClusterQueue or Cohort spec via server-side apply:
    - Write nominalQuota for each (flavor, resource) pair.
    - Apply with field manager "kueue.x-k8s.io/node-quota-policy" and
      ForceOwnership. The field manager scopes the write to just these
      nominalQuota fields (leaving borrowingLimit, other flavors, labels
      untouched); ForceOwnership makes it authoritative, so a manual edit to a
      managed field is reclaimed on the next reconcile.
6.  Update NodeQuotaPolicy.status (computedQuotas, conditions) and the
    NodeQuotaManaged condition on the target.
7.  If any nominalQuota decreased, the Kueue scheduler cache is updated. The
    lower quota constrains future admission only; already-admitted workloads are
    not proactively evicted (an over-quota ClusterQueue simply admits nothing new
    until usage drops below the new quota).
```

### Interaction with Existing Features

| Feature | Interaction |
|---|---|
| Borrowing/Lending | The controller writes only `nominalQuota`; `borrowingLimit` and `lendingLimit` are absolute, admin-set values it does not modify. Borrowing/lending math then uses the new `nominalQuota` together with those unchanged limits. Caveat: `lendingLimit ≤ nominalQuota` is webhook-enforced, so a computed decrease below an existing `lendingLimit` would be rejected — the controller pre-checks this and instead sets `Ready=False`/`LendingLimitExceedsComputedQuota`, withholding the write (see Reconciliation Loop). `borrowingLimit` has no such bound. |
| Hierarchical Cohorts | SubtreeQuota accumulation uses the `nominalQuota` written by the controller at both Cohort and CQ levels. No changes needed in `resource_node.go`. |
| Quota decrease | If `nominalQuota` decreases (nodes removed), the scheduler cache is updated and the lower quota constrains **future** admission only; already-admitted workloads are **not** proactively evicted (an over-quota ClusterQueue simply admits nothing new until usage drops). This matches existing behavior when an admin manually reduces `nominalQuota`. Kueue preemption is admission-driven — triggered by an incoming workload, not by a quota change. |
| Fair Sharing | Share calculations use the `nominalQuota` as written. No changes needed. |
| ProvisioningRequest | Do not use both for the same resource. ProvisioningRequest is an AdmissionCheck that runs only *after* a workload reserves quota, so it needs `nominalQuota` set to the provisionable max; `NodeQuotaPolicy` instead pins `nominalQuota` to current physical capacity, so a workload exceeding current capacity never reserves quota, never reaches the provisioning check, and the nodes are never created — a deadlock. Use static `nominalQuota` with ProvisioningRequest (reactive autoscaling) and `NodeQuotaPolicy` for fixed/predictable pools. This interaction must be documented for users (see Graduation Criteria). |
| TAS (Topology Aware Scheduling) | TAS already watches Nodes via `rfReconciler`. The `NodeQuotaPolicyReconciler` reuses the same shared Node informer to avoid duplicate watches. |
| Cohort quota | A `NodeQuotaPolicy` may target a Cohort directly. Cohort `nominalQuota` represents a shared pool on top of CQ quotas. A CQ and its parent Cohort may each be managed by separate `NodeQuotaPolicy` objects as long as their effective node sets do not overlap. |
| Manual edits to managed nominalQuota | In `Apply` mode, if an administrator manually patches a managed `nominalQuota`, the controller reclaims and overwrites it on the next reconcile (force server-side apply, scoped to the `nominalQuota` fields it manages). Intentional, mirrors HPA. In `Recommend` mode the controller does not write the target, so manual edits stand. |

**Quota vending / external source of truth.** Large organizations often own the
desired quota state in an external system (a database, or GitOps/Terraform) and
vend capacity to teams from there. For them, a controller that authoritatively
writes `nominalQuota` would fight that system — drift on every plan, a re-write
after every apply. The `mode` field addresses this: in `Recommend` mode the
controller computes and records the proposed values in `status.computedQuotas`
(and sets the `NodeQuotaManaged` condition) but does **not** modify the target, so
the external system can diff the proposal against the live `nominalQuota` and
actuate through its own pipeline; in `Apply` mode (the default) the controller
writes directly. This mirrors VPA's recommendation-only (`updateMode: Off`) vs.
auto modes. Exposing the proposed values and letting consumers diff against the
live object is preferred over persisting a diff artifact that can go stale.

### Test Plan

#### Prerequisite testing updates

Existing unit tests for `resourceNode.available()` and `potentialAvailable()` should be extended to verify correct behavior when `nominalQuota` is dynamically patched (simulating the controller write path).

#### Unit Tests

- `pkg/controller/core/nodequotapolicy_controller.go`:
  - Quota computation from `ResourceFlavor.nodeLabels` (Alpha).
  - Headroom: flavor-wide `headroomPercentage` and per-resource `fixedHeadroom`/`percentageHeadroom`, floor-at-zero.
  - Node readiness filtering (`includeNotReady=false` default).
  - Per-resource `nominalQuota` rules pin the value, bypassing node-derived computation.
  - Enqueue coalescing: rapid node events collapse into one reconcile per policy.
  - No-op when computed quota equals current `nominalQuota`.
  - Status update: `computedQuotas` and `Ready` condition set correctly.
  - Conflict detection: a second `NodeQuotaPolicy` for the same target sets the reactive `DuplicateTarget` condition (not a webhook rejection).
  - `LendingLimitExceedsComputedQuota`: a computed decrease below an existing `lendingLimit` withholds the write and sets `Ready=False`.
  - `Recommend` mode records `computedQuotas` without patching the target.
  - (Beta) Label intersection across `ResourceFlavor` and `flavorPolicy.nodeLabels`.
  - (Beta) Empty intersection emits warning condition, writes `quantity: "0"`.
- API validation webhook (static checks only):
  - `targetRef.kind` enum accepted/rejected.
  - Per-resource rule CEL: at most one of `nominalQuota`/`fixedHeadroom`/`percentageHeadroom` set.
  - Duplicate `flavorName` entries rejected (list-map key).
  - `nodeLabels` (Beta field) rejected in Alpha.
- Reactive controller conditions (not webhook):
  - `FlavorNotFound` / `FlavorNotInTarget` / `ResourceNotCovered` set with the documented fatal/non-fatal behavior.
  - `DuplicateTarget` when a second policy targets the same `(kind, name)`.
  - (Beta) `OverlappingNodeSet` for overlapping effective node sets.

#### Integration Tests

**Alpha:**

- `NodeQuotaPolicy` targeting a `ClusterQueue` correctly reflects node capacity.
- `NodeQuotaPolicy` targeting a `Cohort` correctly reflects shared node capacity; CQs with `nominalQuota: 0` borrow from it.
- Adding/removing Nodes updates the `nominalQuota` in the target CQ/Cohort.
- Workload admission respects the controller-written `nominalQuota`.
- `nominalQuota` decrease constrains future admission and does not evict already-admitted workloads.
- Per-resource `nominalQuota` rules correctly bypass node-derived values for named resources.
- Interaction with `borrowingLimit` and `lendingLimit`.
- Controller does not write when computed quota is unchanged (no spurious updates).

**Beta:**

- `flavorPolicy.nodeLabels` narrows the node set (intersection with `ResourceFlavor.nodeLabels`).
- Two `NodeQuotaPolicy` objects referencing the same `ResourceFlavor` but with different `flavorPolicy.nodeLabels` produce independent quota values for each target CQ.
- Conflicting labels (same key, different value) produce `quantity: "0"` and a warning condition.
- Overlapping effective node sets across two `NodeQuotaPolicy` objects in the same Cohort tree produce warning conditions on both.

#### e2e Tests

- End-to-end with a multi-node cluster: `NodeQuotaPolicy` targeting a CQ dynamically adjusts `nominalQuota` as Nodes join and leave.
- `NodeQuotaPolicy` targeting a Cohort: shared pool updates and CQs borrow correctly.
- (Beta) Multi-region cluster: two `NodeQuotaPolicy` objects with different `flavorPolicy.nodeLabels` independently track regional node capacity.

### Graduation Criteria

#### Alpha

- Feature gate `AutomaticQuotaFromNodes` (default disabled).
- `NodeQuotaPolicy` CRD implemented with full Go types, CRD manifest, and admission webhook.
- `NodeQuotaPolicyReconciler` implemented, sharing the Node informer with TAS.
- Server-side apply patch writes `nominalQuota` to target ClusterQueue or Cohort.
- Per-resource `resources` rules supported (fixed `nominalQuota` for resources not reported by nodes, plus fixed/percentage headroom).
- `NodeQuotaPolicy.status` reports `computedQuotas` and `Ready` condition; the target carries a `NodeQuotaManaged` condition.
- Unit and integration tests covering core Alpha functionality.
- Documentation of the new CRD and controller behavior, including the `mode` (Apply/Recommend) semantics and the interactions with ProvisioningRequest (do not combine on one resource) and borrowing/lending.

#### Beta

- Feature gate default enabled.
- `flavorPolicy.nodeLabels` field active for fine-grained intersection-based node selection.
- Intersection semantics implemented, tested, and documented.
- Conflict detection for overlapping node sets across policies in the same Cohort tree (`OverlappingNodeSet` condition with the write withheld).
- Metrics:
  - `kueue_node_quota_policy_computed_quota` (gauge): current `nominalQuota` per policy/flavor/resource.
  - `kueue_node_quota_policy_reconcile_duration_seconds` (histogram): reconcile latency per policy.
- Performance testing in large clusters (1000+ Nodes, 100+ NodeQuotaPolicy objects).

#### GA

- Feature gate removed (always enabled).
- Stable API with no breaking changes since Beta.
- Production usage feedback incorporated.
- Resolve the open question on `nominalQuota` initial value (see [Open Questions](#open-questions)).

## Implementation History

- 2026-04-23: KEP created based on issue [#10270](https://github.com/kubernetes-sigs/kueue/issues/10270).
- 2026-04-24: Revised based on review feedback:
  - Moved `autoDetect` from resource level to flavor level; uses ResourceFlavor `nodeLabels` directly (no duplicated `nodeSelector` field).
  - Extended support to Cohort level.
  - Split into Alpha (coarse-grained, flavor-level flag) and Beta (fine-grained, label intersection across Cohort/CQ/ResourceFlavor scopes).
  - Clarified headroom vs. Allocatable semantics and ProvisioningRequest interaction.
- 2026-04-30: Revised based on architectural feedback (@mwielgus):
  - Replaced `autoDetect` flag embedded in `FlavorQuotas` with a standalone `NodeQuotaPolicy` CRD and dedicated controller.
  - ClusterQueue and Cohort APIs are no longer modified; `nominalQuota` remains the single authoritative value, written by the controller.
  - Controller uses server-side apply with a dedicated field manager, mirroring the HPA pattern for writing `spec.replicas`.
  - Moved `nodeLabels` (Beta fine-grained intersection) from `FlavorQuotas` into `FlavorNodeQuotaPolicy` within the new CRD.

## Open Questions

### OQ-1: Initial value of `nominalQuota` when managed by NodeQuotaPolicy

When a `NodeQuotaPolicy` targets a ClusterQueue or Cohort, the administrator must still provide an initial value for `nominalQuota` in the CQ/Cohort spec, because the field is currently `+required` and typed as `resource.Quantity` (a non-pointer value type — `"0"` and "unset" are indistinguishable at the Go level). Two options are under consideration:

---

**Option A: Keep `nominalQuota` required; administrator writes `"0"` as a placeholder (Alpha approach)**

The administrator declares `nominalQuota: "0"` as a placeholder for every `(flavor, resource)` pair that will be managed by a `NodeQuotaPolicy`. The controller overwrites this with the real computed value on the first successful reconcile.

```yaml
# ClusterQueue authored by the administrator
spec:
  resourceGroups:
  - coveredResources: ["nvidia.com/gpu"]
    flavors:
    - name: gpu-nodes
      resources:
      - name: nvidia.com/gpu
        nominalQuota: "0"    # placeholder; controller will overwrite

---
# NodeQuotaPolicy authored separately
spec:
  targetRef:
    kind: ClusterQueue
    name: gpu-team-cq
  flavorPolicies:
  - flavorName: gpu-nodes
```

Pros:
- No API change required; fully backward compatible.
- Identical to the pattern used by HPA writing `spec.replicas: 1` as a placeholder.
- Simple to implement in Alpha.

Cons:
- The CQ has `nominalQuota: "0"` between creation and the first controller reconcile. During this window workloads targeting it are not admitted — they wait as inadmissible and are requeued, then admitted once the first quota is written. The window is typically short (seconds) but non-zero.
- Reading the CQ in isolation, `nominalQuota: "0"` gives no indication that the field is controller-managed, which can confuse operators.

---

**Option B: Change `nominalQuota` to a pointer type (`*resource.Quantity`), allowing `nil` to mean "externally managed" (Beta/GA approach)**

Change `ResourceQuota.NominalQuota` from `resource.Quantity` to `*resource.Quantity`. A `nil` value signals that the field is managed by an external controller (e.g., `NodeQuotaPolicy`); the scheduler treats `nil` as "wait for controller to populate". A non-nil zero value (`"0"`) retains its current meaning of "no quota".

```go
// Before (current)
// +required
NominalQuota resource.Quantity `json:"nominalQuota,omitempty"`

// After (proposed for Beta)
// +optional
NominalQuota *resource.Quantity `json:"nominalQuota,omitempty"`
```

```yaml
# ClusterQueue — no nominalQuota written at all; controller populates it
spec:
  resourceGroups:
  - coveredResources: ["nvidia.com/gpu"]
    flavors:
    - name: gpu-nodes
      resources:
      - name: nvidia.com/gpu
        # nominalQuota omitted; NodeQuotaPolicy controller will write it
```

Pros:
- Cleanest semantics: `nil` unambiguously means "controller-managed".
- Consistent with `spec.replicas *int32` in Deployment (used by HPA).
- Allows future tooling to distinguish managed vs. manual fields without requiring an annotation.

Cons:
- Breaking API change (`resource.Quantity` → `*resource.Quantity`). Requires a CRD version bump or a conversion webhook and careful migration path for existing users who set `nominalQuota` explicitly.
- More complex webhook validation: `nil` is only valid when a `NodeQuotaPolicy` targeting this CQ/Cohort/flavor/resource exists; otherwise it is a configuration error.
- Scheduler must be updated to handle `nil` gracefully (treat as 0 with a "pending population" condition on the CQ).

---

**Recommendation**: Use Option A for Alpha (no API change, fast to implement) and plan to adopt Option B in Beta or GA once the API version transition story is clear. The `NodeQuotaPolicy` status `computedQuotas` field provides observability in both options. A `Ready=False` condition with reason `TargetNotPopulated` on the `NodeQuotaPolicy` will surface the "quota not yet written" state in Option A.
