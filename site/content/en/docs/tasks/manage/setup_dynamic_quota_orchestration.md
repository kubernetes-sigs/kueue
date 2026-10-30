---
title: "Set Up Dynamic Quota Orchestration"
linkTitle: "Dynamic Quota Orchestration"
date: 2026-10-30
weight: 8
description: >
  Configure Kueue to set ClusterQueue and Cohort quotas from reported capacity.
---

{{< feature-state state="alpha" for_version="v0.20" >}}

This page shows how to set up
[Dynamic Quota Orchestration](/docs/concepts/dynamic_quota_orchestration) (DQO)
for a Cohort and its ClusterQueues.

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster is running.
- The kubectl command-line tool has communication with your cluster.
- [Kueue is installed](/docs/installation) in version 0.20 or later.
- You can create cluster-scoped objects and write their status.

## Enable the feature gate

`DynamicQuotaOrchestration` is an alpha feature and is disabled by default.

Follow the
[feature gates configuration instructions](/docs/installation/#change-the-feature-gates-configuration)
to enable the `DynamicQuotaOrchestration` feature gate in the Kueue controller
manager.

## Create the queues and a CapacityProvider

This example has one Cohort, `org`, with two ClusterQueues. `team-a` has a
nominal quota of 10 CPUs and 40Gi of memory. `team-b` has twice as much. The
CapacityProvider reports capacity for `default-flavor`.

{{< include "examples/admin/dqo-setup.yaml" "yaml" >}}

Create the setup:

```shell
kubectl apply -f https://kueue.sigs.k8s.io/examples/admin/dqo-setup.yaml
```

## Report capacity

In a real setup, a provider controller writes the CapacityProvider status. For
this example, you write it by hand.

First, get the generation of the CapacityProvider:

```shell
kubectl get capacityprovider demo-provider -o jsonpath='{.metadata.generation}'
```

The output is similar to the following:

```
1
```

Then write the status. Set `observedGeneration` on the condition to the
generation from the previous step. If it does not match, Kueue treats the
status as stale.

```shell
kubectl patch capacityprovider demo-provider --subresource=status --type=merge -p '{
  "status": {
    "capacity": {"flavors": [{"name": "default-flavor", "resources": {"cpu": "60", "memory": "300Gi"}}]},
    "conditions": [{
      "type": "CapacitySynchronized",
      "status": "True",
      "reason": "Synchronized",
      "message": "Capacity written by hand for the demo",
      "observedGeneration": 1,
      "lastTransitionTime": "2026-10-30T00:00:00Z"
    }]
  }
}'
```

## Check the effective capacity

Start in discovery-only mode. Kueue computes the effective capacity but does
not change any quota.

{{< include "examples/admin/dqo-discovery-only.yaml" "yaml" >}}

```shell
kubectl apply -f https://kueue.sigs.k8s.io/examples/admin/dqo-discovery-only.yaml
kubectl get dynamicquotaorchestrator org-dqo -o yaml
```

The status is similar to the following:

```yaml
status:
  conditions:
  - lastTransitionTime: "2026-10-30T18:22:27Z"
    message: Aggregated capacity successfully computed
    observedGeneration: 1
    reason: Computed
    status: "True"
    type: EffectiveCapacityComputed
  effectiveCapacity:
    flavors:
    - name: default-flavor
      resources:
        cpu: "48"
        memory: 240Gi
```

The effective capacity is the reported capacity times the multiplier `0.8`.

{{% alert title="Note" color="primary" %}}
A multiplier can give a value that is not a whole number of units, for example
memory in millibytes. Kueue rounds down to whole units when it distributes the
capacity.
{{% /alert %}}

## Distribute the capacity

Add `capacityDistribution` and point it at the Cohort:

{{< include "examples/admin/dqo-distribution.yaml" "yaml" >}}

```shell
kubectl apply -f https://kueue.sigs.k8s.io/examples/admin/dqo-distribution.yaml
```

Check the conditions:

```shell
kubectl get dynamicquotaorchestrator org-dqo -o jsonpath='{range .status.conditions[*]}{.type}{"\t"}{.status}{"\t"}{.reason}{"\n"}{end}'
```

The output is similar to the following:

```
EffectiveCapacityComputed	True	Computed
Distributed	True	QuotasDistributed
```

Check the effective quotas on `team-a`:

```shell
kubectl get clusterqueue team-a -o yaml
```

The status is similar to the following:

```yaml
status:
  effectiveQuotas:
    orchestratorRef:
      apiGroup: kueue.x-k8s.io
      kind: DynamicQuotaOrchestrator
      name: org-dqo
    resourceGroups:
    - coveredResources:
      - cpu
      - memory
      flavors:
      - name: default-flavor
        resources:
        - name: cpu
          nominalQuota: "16"
        - name: memory
          nominalQuota: 80Gi
```

`team-b` gets 32 CPUs and 160Gi of memory. The 48 CPUs are split 1:2, the same
ratio as the nominal quotas in `spec.resourceGroups`. Kueue now admits
workloads against these values.

The Cohort `org` has no `spec.resourceGroups`, so its effective quotas are an
empty list:

```shell
kubectl get cohort org -o jsonpath='{.status.effectiveQuotas}'
```

```
{"orchestratorRef":{"apiGroup":"kueue.x-k8s.io","kind":"DynamicQuotaOrchestrator","name":"org-dqo"},"resourceGroups":[]}
```

## Observe

The `kueue_cluster_queue_info` and `kueue_cohort_info` metrics have a
`dynamic_quota_orchestrator` label that names the orchestrator. The quota
metrics report the effective values. `kueue_cluster_queue_nominal_quota` needs
`metrics.enableClusterQueueResources: true` in the Kueue configuration.
See [Prometheus Metrics](/docs/reference/metrics/).

```
kueue_cluster_queue_info{cluster_queue="team-a",dynamic_quota_orchestrator="org-dqo",parent_cohort="org",replica_role="standalone",root_cohort="org"} 1
kueue_cluster_queue_nominal_quota{cluster_queue="team-a",cohort="org",flavor="default-flavor",replica_role="standalone",resource="cpu"} 16
kueue_cluster_queue_nominal_quota{cluster_queue="team-b",cohort="org",flavor="default-flavor",replica_role="standalone",resource="cpu"} 32
kueue_cohort_subtree_quota{cohort="org",flavor="default-flavor",replica_role="standalone",resource="cpu"} 48
```

## Troubleshooting

| Condition | Reason | What to check |
|---|---|---|
| `EffectiveCapacityComputed=False` | `ProviderNotReady` | The provider's `CapacitySynchronized` condition is not `True`, or its `observedGeneration` does not match `metadata.generation`. The message names the provider. |
| `EffectiveCapacityComputed=False` | `Misconfigured` | A name in `spec.capacityDiscovery.providers` does not match a CapacityProvider. |
| `EffectiveCapacityComputed=False` | `AggregationFailed` | The total has more than 128 flavors, or more than 64 resources in one flavor. |
| `Distributed=False` | `EffectiveCapacityNotComputed` | Fix discovery first. The existing effective quotas stay in place. |
| `Distributed=False` | `ConflictingDynamicQuotaOrchestrator` | Another orchestrator has a higher root, or the same root and an older `creationTimestamp`. |
| `Distributed=False` | `EffectiveQuotasConflict` | A ClusterQueue or Cohort in the subtree is managed by another orchestrator. |
| `Distributed=False` | `Misconfigured` | The `subtreeRootQuotaRef` does not exist. |

For example, if the provider condition is stale, the output is similar to the
following:

```
EffectiveCapacityComputed	False	ProviderNotReady
Distributed	False	EffectiveCapacityNotComputed
```

The message is `CapacityProvider "demo-provider" synchronization is stale (observedGeneration=0, currentGeneration=1)`.

## Clean up

Kueue does not clear effective quotas when you delete a DynamicQuotaOrchestrator.
The ClusterQueues and the Cohort keep the last values, and Kueue keeps
admitting workloads against them. Delete the orchestrator, then clear the
effective quotas by hand:

```shell
kubectl delete dynamicquotaorchestrator org-dqo
kubectl patch clusterqueue team-a --subresource=status --type=json -p '[{"op":"remove","path":"/status/effectiveQuotas"}]'
kubectl patch clusterqueue team-b --subresource=status --type=json -p '[{"op":"remove","path":"/status/effectiveQuotas"}]'
kubectl patch cohort org --subresource=status --type=json -p '[{"op":"remove","path":"/status/effectiveQuotas"}]'
```

Kueue then uses `spec.resourceGroups` again. Delete the rest of the example:

```shell
kubectl delete -f https://kueue.sigs.k8s.io/examples/admin/dqo-setup.yaml
```

## What's next?

- Read about [Dynamic Quota Orchestration](/docs/concepts/dynamic_quota_orchestration).
- See the [v1alpha1 API reference](/docs/reference/kueue.v1alpha1/).
