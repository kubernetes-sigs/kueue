---
title: "Configurable Preemption"
date: 2026-09-22
weight: 8
aliases:
  - "/docs/concepts/configurable_preemption/"
description: >
  Declarative, rule-based preemption policies for custom candidate selection.
---

{{< feature-state state="alpha" for_version="v0.20" >}}

Configurable Preemption introduces a declarative mechanism to define when preemption should occur and which workloads are eligible for eviction. It complements Kueue's existing [Classic Preemption](/docs/concepts/preemption/#classic-preemption) and [Fair Sharing](/docs/concepts/preemption/#fair-sharing) algorithms by enabling policies for complex operational scenarios, such as:

- **Topology Defragmentation**: Allowing distributed workloads requiring specific physical topology domains (such as multi-node GPU or TPU training jobs under [Topology-Aware Scheduling](/docs/concepts/topology_aware_scheduling)) to preempt smaller workloads that fragment the cluster, even when all workloads are within their nominal quotas.
- **Mission-Critical "Hero" Workloads**: Allowing dedicated queues to preempt across cohorts without borrowing restrictions.
- **Granular Priority & Label Rules**: Evaluating candidates using either priorities or custom labels.

To use Configurable Preemption, enable the `ConfigurablePreemptions` [feature gate](/docs/installation/#change-the-feature-gates-configuration).

## Architecture & API Overview

Configurable Preemption is driven by the cluster-scoped **`PreemptionConfig`** custom resource (`kueue.x-k8s.io/v1alpha1`). It contains a list of rules, each defining:

1. **Activation Policy (`activationPolicy`)**: The trigger that activates the rule during a scheduling cycle.
2. **Candidate Selectors (`candidateSelectors`)**: One or more criteria that identify which running workloads may be considered for preemption.

Here is a complete example of a `PreemptionConfig` resource:

```yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: PreemptionConfig
metadata:
  name: "topology-defragmentation"
spec:
  rules:
  - name: "evict-smaller-jobs-for-large-topology"
    activationPolicy:
      trigger: "QuotaFeasibleAndInsufficientTopology"
    candidateSelectors:
    - scope: "AnyClusterQueue"
      priority:
        mode: "Base"
        comparison: "LessThanOrEqual"
      numericLabels:
      - key: "example.com/node-count"
        comparison: "LessThan"
        fallbackValue: 0
      clusterQueueSelector:
        matchExpressions:
        - key: "example.com/protection-tier"
          operator: "NotIn"
          values: ["infrastructure"]
```

## Activation Triggers

The `activationPolicy.trigger` field determines when a preemption rule becomes active during admission evaluation:

| Trigger | Description |
| :--- | :--- |
| `Always` | Matching candidates are contributed unconditionally during preemption evaluation. |
| `InsufficientQuota` | Matching candidates are contributed only if preempting baseline candidates does not yield sufficient quota to admit the incoming workload. |
| `QuotaFeasibleAndInsufficientTopology` | Matching candidates are contributed only if quota is already feasible for the preemptor under at least one eligible flavor assignment (after baseline preemption and applicable `InsufficientQuota` candidates), but the workload cannot be admitted because no placement satisfies the physical topology requirements (see: [Topology-Aware Scheduling](/docs/concepts/topology_aware_scheduling)). |


## Candidate Selectors

Each rule specifies `candidateSelectors` to filter eligible preemption victims. A candidate must satisfy all constraints specified within a selector:

### 1. Relational Scope (`scope`)

The `scope` defines the relational boundary between the preemptor and candidate workloads:

- `WithinLocalQueue`: Candidate must belong to the exact same LocalQueue as the preemptor.
- `WithinClusterQueue`: Candidate must belong to the exact same ClusterQueue as the preemptor.
- `WithinParentCohort`: Candidate belongs to a ClusterQueue sharing the immediate parent Cohort, or the preemptor's own queue.
- `WithinCohortTree`: Candidate belongs to any ClusterQueue within the same root Cohort hierarchy.
- `AnyClusterQueue`: No relationship constraint; candidates can be selected from any ClusterQueue in the cluster.

### 2. Priority Constraints (`priority`)

Defines priority comparison criteria against the incoming preemptor workload:

- **`mode`**:
  - `Base`: Compares raw priority values assigned in `Workload.spec.priority`.
  - `Boosted`: Compares effective priority values adjusted by priority boosting (e.g., queue waiting time boosting).
- **`comparison`**:
  - `LessThan`: Candidate priority < Preemptor priority.
  - `LessThanOrEqual`: Candidate priority <= Preemptor priority.
  - `GreaterThan`: Candidate priority > Preemptor priority.
  - `GreaterThanOrEqual`: Candidate priority >= Preemptor priority.

### 3. Custom Numeric Labels (`numericLabels`)

Allows candidate filtering based on integer workload labels (e.g., number of GPUs/TPUs, slice index, or node count):

- **`key`**: The workload label key containing an integer string.
- **`comparison`**: How the candidate's label value compares to the preemptor's label value (`LessThan`, `LessThanOrEqual`, `GreaterThan`, `GreaterThanOrEqual`).
- **`fallbackValue`**: Integer value assumed if a workload does not have the label or the value cannot be parsed. If omitted, workloads lacking the label are treated as incomparable and excluded.
- **`minValue` / `maxValue`**: Absolute boundaries for the candidate's label value.

{{% alert title="Important" color="warning" %}}
Custom labels from high-level jobs (e.g., Job, JobSet, RayCluster) are not automatically copied to the Kueue `Workload` resource unless their keys are listed in `integrations.labelKeysToCopy` in your [Kueue Configuration](/docs/reference/kueue-config.v1beta2). Ensure your custom numeric label keys are configured for copying.
{{% /alert %}}

### 4. Label Selectors (`labelSelector` & `clusterQueueSelector`)

- **`labelSelector`**: Standard Kubernetes label selector filtering candidate `Workload` metadata.
- **`clusterQueueSelector`**: Standard Kubernetes label selector filtering target `ClusterQueue` metadata.

---

## Referencing PreemptionConfig on a ClusterQueue

`PreemptionConfig` is attached to a `ClusterQueue` using the `kueue.x-k8s.io/preemption-config-name` annotation:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: "distributed-training-cq"
  annotations:
    kueue.x-k8s.io/preemption-config-name: "topology-defragmentation"
spec:
  preemption:
    reclaimWithinCohort: LowerPriority
    withinClusterQueue: LowerPriority
  # ... remaining ClusterQueue fields ...
```

{{% alert title="Interaction with Existing Preemption" color="info" %}}
In Alpha, `PreemptionConfig` works alongside `ClusterQueue.spec.preemption`. Candidates from both mechanisms are evaluated and merged into a single candidate set before eviction. 
{{% /alert %}}

---

## Candidate Merging and Ordering

During preemption evaluation in the scheduler, candidates from both mechanisms are gathered and combined:

1. **Dual Candidate Gathering**:
   - **Classical / Fair Sharing candidates**: Evaluated according to `ClusterQueue.spec.preemption` rules (such as borrowing reclaim in the cohort and within-queue priority preemption).
   - **Configurable Preemption candidates**: Evaluated according to the active rules in the referenced `PreemptionConfig`.
2. **Deduplication & Union**: The scheduler combines candidates from both sources into a single set, deduplicating workloads by UID.
3. **Selective Control**:
   - To use **only** `PreemptionConfig` rules and silence classical preemption, explicitly set `spec.preemption.reclaimWithinCohort: Never` and `spec.preemption.withinClusterQueue: Never`.
   - To use **only** classical preemption, simply omit the `kueue.x-k8s.io/preemption-config-name` annotation.
4. **Deterministic Ordering**: Once merged, candidates are sorted using Kueue's standard ordering heuristics to satisfy preemptor requirements:
   1. Workloads already marked for preemption (`isEvicted`).
   2. Workloads from other ClusterQueues in the cohort before workloads in the preemptor's own queue.
   3. (Admission Fair Sharing only) Workloads with lower LocalQueue fair sharing usage.
   4. Workloads with lower priority.
   5. Workloads admitted more recently (protecting long-running jobs).
   6. Workload UID as a deterministic tie-breaker.

{{% alert title="Note on Beta Evolution" color="info" %}}
In Beta+, `PreemptionConfig` will achieve full feature parity with classical and fair sharing preemption. The two strategies will become mutually exclusive via a formal API field on `ClusterQueueSpec`, and the Alpha annotation will be retired.
{{% /alert %}}

---

## Observability

When a workload is preempted by a `PreemptionConfig` rule, Kueue sets the `Evicted` and `Preempted` conditions in `Workload.status.conditions`, recording the specific rule name:

```yaml
status:
  conditions:
  - type: Evicted
    status: "True"
    reason: Preempted
    message: "Preempted by rule 'evict-smaller-jobs-for-large-topology' in PreemptionConfig 'topology-defragmentation' to accommodate workload default/training-job-xyz"
  - type: Preempted
    status: "True"
    reason: PreemptionConfigRule
    message: "Preempted by rule 'evict-smaller-jobs-for-large-topology' in PreemptionConfig 'topology-defragmentation'"
```

---

## Preventing Preemption Flapping

When authoring preemption rules across queues (especially with `AnyClusterQueue` or `WithinCohortTree`), ensure that rules are **strictly asymmetric**: if Workload A can preempt Workload B, Workload B must not be able to preempt Workload A in return.

Asymmetry can be guaranteed by:
- Requiring `priority.comparison: LessThan`.
- Enforcing `numericLabels` with `comparison: LessThan` on job sizes.
- Restricting preemption rights to dedicated high-priority queues.

---

## What's next?

- Follow the [Configure Custom Preemption Policies](/docs/tasks/manage/setup_configurable_preemption) guide for hands-on configuration steps and practical scenarios.
- Read [Preemption](/docs/concepts/preemption) to understand Classic Preemption and Fair Sharing algorithms.
- Read [Topology-Aware Scheduling](/docs/concepts/topology_aware_scheduling) to see how physical network topology and defragmentation interact.
- Learn about [Workload Priority Class](/docs/concepts/workload_priority_class) to configure workload priorities.
