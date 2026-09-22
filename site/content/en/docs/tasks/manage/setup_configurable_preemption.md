---
title: "Configure Custom Preemption Policies"
date: 2026-09-22
weight: 7
description: >
  Set up and verify declarative preemption rules for topology defragmentation and hero workloads using PreemptionConfig.
---

{{< feature-state state="alpha" for_version="v0.20" >}}

This guide demonstrates how to configure and verify [Configurable Preemption](/docs/concepts/preemption/configurable_preemption) policies in Kueue. You will learn how to:
1. Enable the `ConfigurablePreemptions` feature gate.
2. Configure a multi-rule `PreemptionConfig` that combines **Topology Defragmentation** and **Hero Workload** preemption.
3. Attach the `PreemptionConfig` to a `ClusterQueue`.
4. Verify and observe preemption conditions and events.

## Before you begin

Make sure the following conditions are met:
- A Kubernetes cluster running Kubernetes 1.30 or higher.
- Kueue v0.20.0 or higher installed.
- The `ConfigurablePreemptions` feature gate enabled in the Kueue controller manager configuration. (Note: `TopologyAwareScheduling` is Beta and enabled by default since v0.14).

To enable the feature gate in your `kueue-manager-config`:

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
featureGates:
  ConfigurablePreemptions: true
```

---

## Example: Multi-Rule Preemption Policy

In this scenario, we configure a dedicated `ClusterQueue` for mission-critical, large-scale distributed training jobs. We want this queue to support two distinct preemption capabilities:

1. **Topology Defragmentation**: When a large job has sufficient quota but is blocked because no single physical topology domain (e.g., rack or block) has contiguous free nodes, allow it to preempt smaller workloads that are fragmenting the cluster.
2. **Hero Workload Preemption**: When a high-priority job arrives and the cluster lacks quota, allow it to preempt lower-priority workloads across any queue in the cluster, while respecting protection labels on mission-critical queues.

### 1. Create the PreemptionConfig

Create a `PreemptionConfig` containing both rules:

```yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: PreemptionConfig
metadata:
  name: "defrag-and-hero-preemption-config"
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
  - name: "hero-preempt-lower-priority-any-queue"
    activationPolicy:
      trigger: "InsufficientQuota"
    candidateSelectors:
    - scope: "AnyClusterQueue"
      priority:
        mode: "Base"
        comparison: "LessThan"
      clusterQueueSelector:
        matchExpressions:
        - key: "example.com/protection-tier"
          operator: "NotIn"
          values: ["mission-critical"]
```

### How each rule works

- **Rule 1 (`evict-smaller-jobs-for-large-topology`)**:
  - **Trigger**: `QuotaFeasibleAndInsufficientTopology` activates when quota is feasible for the incoming job under at least one eligible flavor assignment after baseline preemption and any applicable `InsufficientQuota` rules, but no eligible flavor assignment satisfies its topology requirements ([Topology-Aware Scheduling](/docs/concepts/topology_aware_scheduling)).
  - **Scope**: `AnyClusterQueue` searches across all queues for smaller fragmenting workloads.
  - **Asymmetry Protection**: `numericLabels` with `comparison: LessThan` ensures that a larger workload (e.g., `example.com/node-count: 32`) can preempt smaller labeled workloads (e.g., `example.com/node-count: 4`), but a 4-node workload can never preempt a 32-node workload in return. By omitting `fallbackValue`, workloads lacking the label are treated as incomparable and protected from eviction.
  - **Custom Label Propagation**: `example.com/node-count` is a custom user-defined label placed on jobs or workloads. Note that custom labels from batch Jobs are not automatically copied to the Kueue `Workload` resource unless listed in `integrations.labelKeysToCopy` in your [Kueue Configuration](/docs/reference/kueue-config.v1beta2).

- **Rule 2 (`hero-preempt-lower-priority-any-queue`)**:
  - **Trigger**: `InsufficientQuota` evaluates candidates whenever quota is insufficient to admit the hero workload.
  - **Scope**: `AnyClusterQueue` searches across all queues for lower-priority workloads.
  - **Protection Guardrail**: `clusterQueueSelector` ensures the hero workload cannot evict workloads from queues labeled with `example.com/protection-tier: mission-critical`.

---

### 2. Attach PreemptionConfig to the ClusterQueue

Link the `PreemptionConfig` to your `ClusterQueue` using the `kueue.x-k8s.io/preemption-config-name` annotation:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: "distributed-training-cq"
  annotations:
    kueue.x-k8s.io/preemption-config-name: "defrag-and-hero-preemption-config"
spec:
  preemption:
    reclaimWithinCohort: LowerPriority
    withinClusterQueue: LowerPriority
  # ... resource groups, flavors, and quotas ...
```

{{% alert title="Strategy Merging in Alpha" color="info" %}}
In Alpha, candidates selected by the referenced `PreemptionConfig` are merged with candidates selected by `spec.preemption`. To enforce *only* the rules in `PreemptionConfig`, set `spec.preemption.reclaimWithinCohort: Never` and `spec.preemption.withinClusterQueue: Never`.
{{% /alert %}}

---

## Verification & Observability

### 1. Inspect Admitted Workloads and Conditions

When a preemption occurs, check the status conditions and events on the preempted workload:

```bash
kubectl describe workload <preempted-workload-name>
```

Look for the `Evicted` and `Preempted` conditions in `Workload.status.conditions`. Their messages identify the `PreemptionConfig` rule that triggered the eviction:

```text
Normal  Preempted   workload  Preempted by rule 'evict-smaller-jobs-for-large-topology' in PreemptionConfig 'defrag-and-hero-preemption-config'
```

### 2. Check Metrics

Kueue reports preemption metrics broken down by reason and queue:
- `kueue_preempted_workloads_total`: Count of preempted workloads.
- `kueue_admission_attempts_total{result="inadmissible"}`: Counts failed admission attempts. Inspect `Workload.status.conditions` to determine whether a workload was blocked by quota or topology.
