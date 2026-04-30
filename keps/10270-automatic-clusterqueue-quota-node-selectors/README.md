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
    headroom:
      percentage: 5                # reserve 5% for DaemonSets
    resourceOverrides:
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
- **Headroom and Allocatable**: `node.Status.Allocatable` already equals total node capacity minus kubelet's `kube-reserved` and `system-reserved`, so those system reservations are implicitly handled. The `headroom` field is for DaemonSets and other non-Kueue pods that consume node resources beyond what kubelet reserves.
- **Cohort + CQ NodeQuotaPolicy overlap**: A Cohort and a CQ within it may each be targeted by a `NodeQuotaPolicy` only if their respective ResourceFlavors' effective node sets (after label intersection) are **disjoint**. Overlapping node sets cause double-counting. Beta will add validation warnings for overlapping selectors.
- **Node readiness filtering**: By default only Nodes with condition `Ready=True` are counted. The `includeNotReady` field in `FlavorNodeQuotaPolicy` can override this.
- **Rate limiting**: The controller aggregates node events and re-computes quotas at a bounded frequency (e.g., every 15 seconds) to avoid excessive API writes to ClusterQueue or Cohort.
- **Feature gate**: This feature is gated behind `AutomaticQuotaFromNodes` (disabled by default in Alpha).
- **Recommended use case**: This feature targets predictable, known capacity (fixed node pools, periodic onboarding). For reactive autoscaling via ProvisioningRequest, static `nominalQuota` remains recommended.

### Risks and Mitigations

- **Stale quota if Node watch is delayed**: Kueue already relies on informer caches for correctness. Risk is no different from existing Workload/Pod watches.
- **Resource churn causing frequent quota updates**: Mitigation: batch node events with a bounded reconciliation interval (e.g., 15 seconds). Only write to the target object when the computed quota actually changes.
- **Conflicting selectors across ClusterQueues or between Cohort and CQ**: Overlapping node sets lead to over-committed quotas, same as manually overlapping quotas today. Beta will add validation warnings.
- **Concurrent writes**: If an administrator manually edits `nominalQuota` on a CQ that is managed by a `NodeQuotaPolicy`, the controller will overwrite it on the next reconcile. This is the same behavior as HPA overwriting `spec.replicas`. Administrators should be aware that managed fields are authoritative in `NodeQuotaPolicy`, not in the CQ.
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
    // the base node selector. The referenced ResourceFlavor must define at
    // least one nodeLabel; otherwise the controller marks the policy as invalid.
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

    // headroom reserves capacity from the computed total for DaemonSets and
    // other non-Kueue pods. node.Status.Allocatable already accounts for
    // kubelet kube-reserved and system-reserved; headroom is additional.
    // At most one of percentage or fixed may be set.
    // +optional
    Headroom *QuotaHeadroom `json:"headroom,omitempty"`

    // includeNotReady controls whether Nodes without the Ready=True condition
    // contribute to quota computation. Defaults to false (only Ready nodes).
    // +optional
    IncludeNotReady *bool `json:"includeNotReady,omitempty"`

    // resourceOverrides pins specific resources to a fixed nominalQuota,
    // bypassing node-based computation for those resources. Use this for
    // custom resources that are not reported in node.Status.Allocatable
    // (e.g., example.com/token). Takes precedence over node-derived values.
    // +optional
    // +listType=map
    // +listMapKey=name
    // +kubebuilder:validation:MaxItems=64
    ResourceOverrides []ResourceQuotaOverride `json:"resourceOverrides,omitempty"`
}

// ResourceQuotaOverride pins a specific resource to a fixed nominalQuota,
// bypassing node-based computation for that resource.
type ResourceQuotaOverride struct {
    // name is the Kubernetes resource name (e.g., example.com/token).
    // +required
    Name corev1.ResourceName `json:"name"`

    // nominalQuota is the fixed quota value for this resource.
    // +required
    NominalQuota resource.Quantity `json:"nominalQuota"`
}

// QuotaHeadroom configures how much capacity to reserve from the computed total.
// +kubebuilder:validation:XValidation:rule="!(has(self.percentage) && has(self.fixed))",message="at most one of percentage or fixed may be set"
type QuotaHeadroom struct {
    // percentage of total Allocatable to subtract (0–100).
    // +optional
    // +kubebuilder:validation:Minimum=0
    // +kubebuilder:validation:Maximum=100
    Percentage *int32 `json:"percentage,omitempty"`

    // fixed is a fixed quantity to subtract from the computed total.
    // +optional
    Fixed *resource.Quantity `json:"fixed,omitempty"`
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
    // the target object, one entry per (flavor, resource) pair.
    // +optional
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

    // lastUpdated is the timestamp of the most recent successful write.
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

**No changes** to `ClusterQueueSpec`, `CohortSpec`, `FlavorQuotas`, or `ResourceQuota` in Alpha. The `nominalQuota` field is written by the `NodeQuotaPolicyReconciler` via a server-side apply patch, exactly as an HPA controller patches `spec.replicas` on a Deployment.

The Kueue scheduler continues to read `nominalQuota` from the CQ or Cohort spec as today. No scheduler changes are required.

#### Validation Rules (Alpha)

Enforced by the `NodeQuotaPolicy` admission webhook:

- `targetRef.kind` must be `ClusterQueue` or `Cohort`.
- `flavorPolicies` must not contain duplicate `flavorName` entries.
- The referenced ResourceFlavor (via `flavorName`) must define at least one `nodeLabel`; otherwise the webhook returns a validation error since there is no node selector to work with.
- Within `headroom`, at most one of `percentage` or `fixed` may be set (enforced by CEL rule on the struct).
- `nodeLabels` (Beta field) must be empty in Alpha; the webhook rejects it if the `AutomaticQuotaFromNodes` feature gate is not at Beta or later.
- Only one `NodeQuotaPolicy` may target a given `(kind, name)` pair. The webhook rejects creation of a second policy that conflicts with an existing one.

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

Administrators can inspect computed values without reading the CQ:

```bash
kubectl get nqp gpu-team-auto-quota -o jsonpath='{.status.computedQuotas}'
```

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
- Beta adds conflict detection: if two `NodeQuotaPolicy` objects targeting different CQs or Cohorts within the same Cohort tree have overlapping effective node sets for the same flavor, a warning condition is emitted on both policies (double-counting risk).

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

When a Node event arrives, the controller determines which label sets match the node, looks up the affected `NodeQuotaPolicy` names, and enqueues them for reconciliation.

#### Reconciliation Loop

```
Trigger: NodeQuotaPolicy / Node / ResourceFlavor / ClusterQueue / Cohort event
         ↓
1.  Load NodeQuotaPolicy spec (targetRef + flavorPolicies).
2.  For each FlavorNodeQuotaPolicy:
    a. Fetch referenced ResourceFlavor → read nodeLabels (base selector).
    b. (Beta) Intersect base selector with flavorPolicy.nodeLabels.
    c. List all Nodes matching the effective label set.
    d. Filter: exclude non-Ready nodes unless includeNotReady=true.
    e. Sum node.Status.Allocatable for each resource.
    f. Apply headroom (percentage or fixed subtraction, floor at zero).
    g. Apply resourceOverrides: replace computed value for overridden resources.
3.  Compare computed quota map against NodeQuotaPolicy.status.computedQuotas.
    If unchanged, stop (no write to target object).
4.  Debounce: coalesce rapid node events within a ~15-second window before
    writing to avoid excessive API calls.
5.  Patch target ClusterQueue or Cohort spec via server-side apply:
    - Write nominalQuota for each (flavor, resource) pair in flavorPolicies.
    - Only touch fields owned by this NodeQuotaPolicy (field manager:
      "kueue.x-k8s.io/node-quota-policy").
6.  Update NodeQuotaPolicy.status.computedQuotas and conditions.
7.  If any nominalQuota decreased, the Kueue scheduler cache is invalidated
    and a scheduling cycle runs to evaluate potential preemptions.
```

### Interaction with Existing Features

| Feature | Interaction |
|---|---|
| Borrowing/Lending | Works unchanged. The controller-written `nominalQuota` is the value used by all borrowing/lending calculations. `borrowingLimit` and `lendingLimit` remain relative to the effective quota. |
| Hierarchical Cohorts | SubtreeQuota accumulation uses the `nominalQuota` written by the controller at both Cohort and CQ levels. No changes needed in `resource_node.go`. |
| Preemption | If `nominalQuota` decreases (nodes removed), this may trigger preemption of workloads exceeding the new quota. Consistent with existing behavior when an admin manually reduces `nominalQuota`. |
| Fair Sharing | Share calculations use the `nominalQuota` as written. No changes needed. |
| ProvisioningRequest | These features target different scenarios. `NodeQuotaPolicy` reflects **current** physical capacity for predictable node pools. ProvisioningRequest is for **reactive** autoscaling. Using both simultaneously for the same resource is not recommended. Once nodes provisioned by ProvisioningRequest come online, the `NodeQuotaPolicy` controller naturally picks them up on the next reconcile. |
| TAS (Topology Aware Scheduling) | TAS already watches Nodes via `rfReconciler`. The `NodeQuotaPolicyReconciler` reuses the same shared Node informer to avoid duplicate watches. |
| Cohort quota | A `NodeQuotaPolicy` may target a Cohort directly. Cohort `nominalQuota` represents a shared pool on top of CQ quotas. A CQ and its parent Cohort may each be managed by separate `NodeQuotaPolicy` objects as long as their effective node sets do not overlap. |
| Manual edits to managed nominalQuota | If an administrator manually patches `nominalQuota` on a CQ managed by a `NodeQuotaPolicy`, the controller will overwrite it at the next reconcile cycle. This is intentional and mirrors HPA behavior. |

### RBAC Requirements

Kueue already has the necessary RBAC for Node watching:

```go
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
```

The `NodeQuotaPolicyReconciler` additionally needs patch access on ClusterQueue and Cohort, which existing Kueue controllers already have:

```go
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=clusterqueues,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=cohorts,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=nodequotapolicies,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=nodequotapolicies/status,verbs=get;update;patch
```

No additional RBAC changes are needed beyond the new CRD's own verbs.

### Test Plan

#### Prerequisite testing updates

Existing unit tests for `resourceNode.available()` and `potentialAvailable()` should be extended to verify correct behavior when `nominalQuota` is dynamically patched (simulating the controller write path).

#### Unit Tests

- `pkg/controller/core/nodequotapolicy_controller.go`:
  - Quota computation from `ResourceFlavor.nodeLabels` (Alpha).
  - Headroom: percentage and fixed subtraction, floor-at-zero.
  - Node readiness filtering (`includeNotReady=false` default).
  - `resourceOverrides` take precedence over node-derived values.
  - Debouncing: rapid node events coalesced before write.
  - No-op when computed quota equals current `nominalQuota`.
  - Status update: `computedQuotas` and `Ready` condition set correctly.
  - Conflict detection: two `NodeQuotaPolicy` objects for the same target rejected by webhook.
  - (Beta) Label intersection across `ResourceFlavor` and `flavorPolicy.nodeLabels`.
  - (Beta) Empty intersection emits warning condition, writes `quantity: "0"`.
- API validation webhook:
  - `flavorName` references a `ResourceFlavor` with at least one `nodeLabel`.
  - `headroom` CEL rule: `percentage` and `fixed` mutually exclusive.
  - Duplicate `flavorName` entries rejected.
  - Second `NodeQuotaPolicy` targeting same CQ rejected.

#### Integration Tests

**Alpha:**

- `NodeQuotaPolicy` targeting a `ClusterQueue` correctly reflects node capacity.
- `NodeQuotaPolicy` targeting a `Cohort` correctly reflects shared node capacity; CQs with `nominalQuota: 0` borrow from it.
- Adding/removing Nodes updates the `nominalQuota` in the target CQ/Cohort.
- Workload admission respects the controller-written `nominalQuota`.
- `nominalQuota` decrease triggers preemption of over-quota workloads.
- `resourceOverrides` correctly bypass node-derived values for named resources.
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
- `resourceOverrides` supported for resources not reported by nodes.
- `NodeQuotaPolicy.status` reports `computedQuotas` and `Ready` condition.
- Unit and integration tests covering core Alpha functionality.
- Documentation of the new CRD and controller behavior.

#### Beta

- Feature gate default enabled.
- `flavorPolicy.nodeLabels` field active for fine-grained intersection-based node selection.
- Intersection semantics implemented, tested, and documented.
- Conflict detection warnings for overlapping node sets across policies in the same Cohort tree.
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
- The CQ has `nominalQuota: "0"` between creation and the first controller reconcile, during which all workloads are rejected. This window is typically short (seconds) but non-zero.
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
- Eliminates the admission gap where `nominalQuota: "0"` briefly blocks workloads.
- Allows future tooling to distinguish managed vs. manual fields without requiring an annotation.

Cons:
- Breaking API change (`resource.Quantity` → `*resource.Quantity`). Requires a CRD version bump or a conversion webhook and careful migration path for existing users who set `nominalQuota` explicitly.
- More complex webhook validation: `nil` is only valid when a `NodeQuotaPolicy` targeting this CQ/Cohort/flavor/resource exists; otherwise it is a configuration error.
- Scheduler must be updated to handle `nil` gracefully (treat as 0 with a "pending population" condition on the CQ).

---

**Recommendation**: Use Option A for Alpha (no API change, fast to implement) and plan to adopt Option B in Beta or GA once the API version transition story is clear. The `NodeQuotaPolicy` status `computedQuotas` field provides observability in both options. A `Ready=False` condition with reason `TargetNotPopulated` on the `NodeQuotaPolicy` will surface the "quota not yet written" state in Option A.
