---
title: "Use Custom Preemption Configurations"
date: 2026-09-22
weight: 7
description: >
  Set up and verify declarative preemption configurations for hero workloads and topology defragmentation using PreemptionConfig.
---

{{< feature-state state="alpha" for_version="v0.20" >}}

This guide demonstrates how to configure and verify [Configurable Preemptions](/docs/concepts/preemption/configurable_preemption) in Kueue. You will learn how to:
1. Enable the `ConfigurablePreemptions` and `PrioritizePreemptorWorkloads` feature gates.
2. Configure a dedicated **Hero Workload Preemption** configuration for an access-restricted `ClusterQueue`.
3. Configure a **Topology Defragmentation** configuration for general-purpose workloads.
4. Verify and observe preemption outcomes via eviction stats and status conditions.

## Before you begin

Make sure the following conditions are met:
- A Kubernetes cluster running Kubernetes 1.30 or higher.
- Kueue v0.20.0 or higher installed.
- The `ConfigurablePreemptions` feature gate enabled in the Kueue controller manager configuration. For hero workload scenarios, enabling `PrioritizePreemptorWorkloads` is also recommended. (Note: `TopologyAwareScheduling` is Beta and enabled by default since v0.14).

To enable the feature gates in your `kueue-manager-config`:

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
featureGates:
  ConfigurablePreemptions: true
  PrioritizePreemptorWorkloads: true
```

---

## Configuration Scenarios

Rather than combining multiple concerns into a single configuration, it is recommended to define distinct `PreemptionConfig` resources tailored to specific queue purposes and operational privileges:

1. **Hero Workloads**: Assigned to an access-restricted `ClusterQueue` for emergency or highest-priority jobs, granting elevated preemption privileges across queues.
2. **Topology Defragmentation**: Attached to general training `ClusterQueues`, allowing large distributed jobs with feasible quota to preempt smaller fragmenting workloads when contiguous topology domains are unavailable.

---

### Scenario 1: Hero Workload Preemption (Restricted Queue)

In this scenario, a dedicated, access-restricted `ClusterQueue` is established for top-priority distributed workloads ("hero workloads"). When hero workloads lack quota, they are permitted to preempt lower-priority workloads across the entire cohort hierarchy, while respecting protection labels on mission-critical queues.

#### 1. Define the PreemptionConfig

```yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: PreemptionConfig
metadata:
  name: "hero-workloads-preemption-config"
spec:
  rules:
  - name: "hero-preempt-lower-priority"
    activationPolicy:
      trigger: "InsufficientQuota"
    candidateSelectors:
    - scope: "WithinCohortTree"
      priority:
        mode: "Base"
        comparison: "LessThan"
      clusterQueueSelector:
        matchExpressions:
        - key: "example.com/protection-tier"
          operator: "NotIn"
          values: ["mission-critical"]
```

**How it works:**
- **Trigger**: `InsufficientQuota` activates candidate search when the hero workload cannot be admitted due to insufficient quota.
- **Scope**: `WithinCohortTree` restricts preemption search to the cohort tree. (Workloads cannot borrow quota outside their cohort hierarchy).
- **Elevated Privileges**: Workloads in this queue can evict lower-priority workloads across queues even if those target workloads are running within their nominal quota (subject to overall cohort borrowing limits).
- **Protection Guardrail**: `clusterQueueSelector` ensures that ClusterQueues labeled `example.com/protection-tier: mission-critical` are never selected for preemption.

#### 2. Attach to the Restricted ClusterQueue

Attach the `PreemptionConfig` to your dedicated hero `ClusterQueue`:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: "hero-jobs-cq"
  annotations:
    kueue.x-k8s.io/preemption-config-name: "hero-workloads-preemption-config"
spec:
  preemption:
    reclaimWithinCohort: Never
    withinClusterQueue: Never
  # ... resource groups, flavors, and quotas ...
```

{{% alert title="Important: Disabling Classical Preemption on Protected Queues" color="warning" %}}
In Alpha, candidates selected by `PreemptionConfig` are merged with candidates selected by `spec.preemption`. If you configure `spec.preemption.reclaimWithinCohort: LowerPriority`, classical preemption will evaluate cohort candidates **without** checking the `clusterQueueSelector` in your `PreemptionConfig`. To ensure that protection labels are strictly honored, set `reclaimWithinCohort: Never` and `withinClusterQueue: Never`.
{{% /alert %}}

---

### Scenario 2: Topology Defragmentation (General Queue)

In this scenario, large distributed jobs require contiguous physical topology (such as full host blocks or racks under [Topology-Aware Scheduling](/docs/concepts/topology_aware_scheduling)). A large workload may have feasible quota, but cannot be scheduled because smaller workloads are fragmenting the physical topology.

#### 1. Define the PreemptionConfig

```yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: PreemptionConfig
metadata:
  name: "topology-defrag-preemption-config"
spec:
  rules:
  - name: "evict-smaller-jobs-for-topology"
    activationPolicy:
      trigger: "QuotaFeasibleAndInsufficientTopology"
    candidateSelectors:
    - scope: "WithinCohortTree"
      priority:
        mode: "Base"
        comparison: "LessThanOrEqual"
      numericLabels:
      - key: "example.com/node-count"
        comparison: "LessThan"
      labelSelector:
        matchExpressions:
        - key: "example.com/workload-tier"
          operator: "NotIn"
          values: ["mission-critical"]
```

**How it works:**
- **Trigger**: `QuotaFeasibleAndInsufficientTopology` activates only when quota is already feasible for the incoming job under at least one eligible flavor assignment (after baseline preemption and any applicable `InsufficientQuota` rules), but placement is blocked by physical topology constraints.
  > [!NOTE]
  > `QuotaFeasibleAndInsufficientTopology` does **not** reclaim missing quota—it only resolves topology fragmentation once quota feasibility has been satisfied.
- **Scope**: `WithinCohortTree` evaluates candidates within the same cohort hierarchy.
- **Asymmetric Defragmentation**: `numericLabels` with `comparison: LessThan` ensures that a larger workload (e.g., `example.com/node-count: 32`) can preempt smaller workloads (e.g., `example.com/node-count: 4`), but a 4-node workload cannot preempt a 32-node workload in return. Omitting `fallbackValue` ensures unlabeled workloads are treated as incomparable and protected from eviction.
- **Protecting Mission-Critical Workloads**: Without explicit exclusion, defragmentation rules could evict smaller mission-critical workloads. The `labelSelector` prevents evicting workloads labeled `example.com/workload-tier: mission-critical`.

#### 2. Attach to the ClusterQueue

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: "general-training-cq"
  annotations:
    kueue.x-k8s.io/preemption-config-name: "topology-defrag-preemption-config"
spec:
  preemption:
    reclaimWithinCohort: LowerPriority
    withinClusterQueue: LowerPriority
  # ... resource groups, flavors, and quotas ...
```

---

## Common Pitfalls

- **Classical Preemption Bypassing Custom Guardrails**: In Alpha, candidate sets from `spec.preemption` and `PreemptionConfig` are merged. If your custom rules protect specific workloads using `labelSelector` or `clusterQueueSelector`, classical preemption in `spec.preemption` will still evaluate and evict those workloads unless you set `spec.preemption.reclaimWithinCohort: Never` and `spec.preemption.withinClusterQueue: Never`.
- **Preemption Flapping from Symmetric Rules**: When defining rules across queues (with `WithinCohortTree` or `AnyClusterQueue`), ensure rules are strictly asymmetric (e.g., using `priority.comparison: LessThan` or `numericLabels.comparison: LessThan`) to avoid cascading preemptions where workloads repeatedly evict each other.
- **Label Propagation**: Custom numeric labels or tier labels on Jobs are not copied to Kueue `Workload` resources unless added to `integrations.labelKeysToCopy` in your [Kueue Configuration](/docs/reference/kueue-config.v1beta2).
- **Topology Defragmentation Requires Quota Feasibility**: A workload blocked by both quota exhaustion and topology fragmentation cannot activate `QuotaFeasibleAndInsufficientTopology` until its quota requirement is satisfied by baseline preemption or an `InsufficientQuota` rule.

---

## Verification & Observability

### 1. Inspect Workload Eviction Stats

When a preemption occurs, Kueue records detailed diagnostic information in `Workload.status.schedulingStats.evictions` on the preempted workload:

```bash
kubectl get workload <preempted-workload-name> -o yaml
```

In the output, locate `status.schedulingStats.evictions`:

```yaml
status:
  schedulingStats:
    evictions:
    - count: 1
      reason: ConfigurablePreemption
      underlyingCause: "Preempted by default/hero-job-xyz because of preemption config hero-workloads-preemption-config rule hero-preempt-lower-priority/0"
```

The `underlyingCause` string records the preemptor workload name, the active `PreemptionConfig`, the rule name, and the index of the matching candidate selector.

### 2. Inspect Status Conditions

Kueue also sets conditions in `Workload.status.conditions` on the preempted workload:

```yaml
status:
  conditions:
  - type: Evicted
    status: "True"
    reason: Preempted
    message: "Preempted by rule 'hero-preempt-lower-priority' in PreemptionConfig 'hero-workloads-preemption-config' to accommodate workload default/hero-job-xyz"
  - type: Preempted
    status: "True"
    reason: ConfigurablePreemption
    message: "Preempted by rule 'hero-preempt-lower-priority' in PreemptionConfig 'hero-workloads-preemption-config'"
```

### 3. Check Metrics

Kueue exports Prometheus metrics broken down by queue and reason:
- `kueue_preempted_workloads_total{reason="ConfigurablePreemption"}`: Counts workloads preempted by `PreemptionConfig` rules.
- `kueue_admission_attempts_total{result="inadmissible"}`: Counts failed admission attempts. Inspect `Workload.status.conditions` to determine whether a workload was blocked by quota or topology.
