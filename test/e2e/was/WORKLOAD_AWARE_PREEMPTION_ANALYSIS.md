<!--
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# WorkloadPriorityClass ↔ Workload-Aware Preemption Integration Analysis

## Overview

This document analyzes how Kueue's `WorkloadPriorityClass` could integrate with
the upstream Kubernetes Workload-Aware Preemption feature
([KEP-5710](https://github.com/kubernetes/enhancements/blob/master/keps/sig-scheduling/5710-workload-aware-preemption/README.md)).

## Current State

### Kueue's WorkloadPriorityClass

Kueue has a `WorkloadPriorityClass` CRD (cluster-scoped) that defines priority
values for workloads:

```go
type WorkloadPriorityClass struct {
    Value       int32  `json:"value"`
    Description string `json:"description,omitempty"`
}
```

Jobs reference a `WorkloadPriorityClass` via the label
`kueue.x-k8s.io/workload-priority-class`. The Kueue Workload's
`spec.priorityClassRef` can reference either:
- `kueue.x-k8s.io/WorkloadPriorityClass` (Kueue-native priority)
- `scheduling.k8s.io/PriorityClass` (Kubernetes pod-level PriorityClass)

Kueue uses this priority for:
1. **Admission ordering** — higher priority workloads are admitted first
2. **Preemption decisions** — within ClusterQueue preemption policies
3. **Fair sharing** — priority influences fair share calculations

### Upstream Workload-Aware Preemption (KEP-5710)

The upstream `scheduling.k8s.io/v1alpha2` `PodGroupTemplate` has preemption-related
fields (gated behind `WorkloadAwarePreemption` feature gate):

```go
type PodGroupTemplate struct {
    // DisruptionMode: "Pod" (default) or "PodGroup"
    DisruptionMode *DisruptionMode `json:"disruptionMode,omitempty"`
    // PriorityClassName references a scheduling.k8s.io PriorityClass
    PriorityClassName string `json:"priorityClassName,omitempty"`
    // Priority value (populated by admission from PriorityClassName)
    Priority *int32 `json:"priority,omitempty"`
}
```

Key differences from Kueue:
- **DisruptionMode=PodGroup**: Preemption is all-or-nothing for the whole PodGroup
- **PriorityClassName**: References `scheduling.k8s.io/PriorityClass` (not
  Kueue's `WorkloadPriorityClass`)
- **Priority**: Integer priority value at the PodGroup level

## Integration Opportunities

### 1. Priority Propagation: WorkloadPriorityClass → PodGroup Priority

When Kueue admits a workload, it could propagate the `WorkloadPriorityClass`
value to the upstream `PodGroupTemplate.Priority` field. This would allow the
kube-scheduler to make workload-aware preemption decisions that are consistent
with Kueue's priority model.

**Approach:**
```
User creates Job with label kueue.x-k8s.io/workload-priority-class=high-priority
    → Kueue resolves WorkloadPriorityClass "high-priority" (value=1000)
    → Kueue creates/updates Workload with priorityClassRef
    → Job controller creates scheduling.k8s.io Workload with PodGroupTemplate
    → PodGroupTemplate.Priority should reflect the WorkloadPriorityClass value
```

**Challenge:** The upstream `PodGroupTemplate.PriorityClassName` expects a
`scheduling.k8s.io/PriorityClass`, not a `WorkloadPriorityClass`. Two options:

- **Option A: Create matching PriorityClass objects.** Kueue could create a
  `scheduling.k8s.io/PriorityClass` for each `WorkloadPriorityClass` with the
  same value. The PodGroupTemplate would then reference this PriorityClass.
  This keeps the upstream API contract clean but introduces object management
  overhead.

- **Option B: Set Priority directly.** If the upstream API allows setting
  `Priority` directly without `PriorityClassName`, Kueue could inject the
  priority value. This is simpler but may not work if admission controllers
  prevent direct priority setting.

### 2. DisruptionMode Alignment

Kueue already supports preemption at the workload level (via ClusterQueue
preemption policies). With `DisruptionMode=PodGroup`, the kube-scheduler would
also preempt at the PodGroup level, creating two layers of preemption:

1. **Kueue-level preemption**: Evicts entire workloads to free quota
2. **Scheduler-level preemption**: Preempts PodGroups to free node resources

For consistency, Kueue should set `DisruptionMode=PodGroup` on PodGroupTemplates
for workloads it manages, since Kueue already treats workloads as atomic units
for quota management.

### 3. Priority Consistency Verification

A key e2e test concern is ensuring priority values are consistent across:
- `WorkloadPriorityClass.Value` (Kueue)
- `Workload.spec.priority` (Kueue)
- `PodGroupTemplate.Priority` (upstream scheduling API)
- Pod-level `PriorityClass` (if also set)

### 4. Proposed Test Scenarios

#### Test: WorkloadPriorityClass propagates to PodGroup priority
```
Given: WorkloadPriorityClass "high" with value=1000
When:  Job with kueue.x-k8s.io/workload-priority-class=high is created
Then:  Kueue Workload has priorityClassRef for "high" with priority=1000
And:   scheduling.k8s.io PodGroup has Priority=1000 (or equivalent PriorityClass)
```

#### Test: Preemption respects WorkloadPriorityClass ordering
```
Given: ClusterQueue with limited quota
And:   Low-priority workload is admitted
When:  High-priority workload (via WorkloadPriorityClass) is submitted
Then:  Kueue preempts the low-priority workload
And:   The scheduler's PodGroup preemption (if any) is consistent
```

#### Test: DisruptionMode=PodGroup for Kueue-managed workloads
```
Given: GenericWorkload and WorkloadAwarePreemption feature gates are enabled
When:  Kueue admits a Job
Then:  The PodGroup's DisruptionMode should be PodGroup
```

## Current Gaps

1. **No automatic WorkloadPriorityClass → PodGroup priority mapping exists.**
   This mapping would need to be implemented in either:
   - The Job controller (upstream Kubernetes)
   - Kueue's workload reconciler (when it detects the upstream Workload/PodGroup)
   - A webhook that enriches PodGroupTemplates

2. **WorkloadPriorityClass and PriorityClass are separate concepts.** A
   workload could have both a `WorkloadPriorityClass` (for Kueue) and a
   `PriorityClass` (for pod scheduling). These may have different values,
   which could lead to inconsistent preemption behavior between Kueue and the
   scheduler.

3. **The `scheduling.k8s.io/v1alpha2` API is alpha.** The types exist in the
   vendor but the API server may not have them enabled. Tests must handle the
   case where the API is not available.

## Recommendations

1. **Short term:** Write e2e tests that verify count consistency between
   Kueue Workload PodSets and upstream PodGroups (implemented in
   `job_test.go`), and priority-based preemption ordering (implemented in
   `preemption_test.go`).

2. **Medium term:** Implement a mapping layer that translates
   `WorkloadPriorityClass` values to `PodGroupTemplate.Priority` when the
   `WorkloadAwarePreemption` feature gate is enabled.

3. **Long term:** Explore whether `WorkloadPriorityClass` and upstream
   `PriorityClass` should converge, or whether Kueue should support both
   independently with clear precedence rules.
