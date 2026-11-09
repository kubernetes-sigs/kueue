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
- [Kueue is installed](/docs/installation) in version 0.20.4 or later.
- You can create cluster-scoped objects and write their status.

## Install the alpha APIs

CapacityProvider and DynamicQuotaOrchestrator are alpha APIs. Kueue does not
install their CRDs by default. Install them before you enable the feature gate.

If you installed Kueue with kubectl, run:

```shell
kubectl apply --server-side -f https://github.com/kubernetes-sigs/kueue/releases/download/{{< param "version" >}}/alpha-crds.yaml
```

If you installed Kueue with Helm, set `enableAlphaAPIs` to `true`:

```shell
helm upgrade kueue https://github.com/kubernetes-sigs/kueue/releases/download/{{< param "version" >}}/kueue-{{< param "chart_version" >}}.tgz \
  --namespace kueue-system \
  --reuse-values \
  --set enableAlphaAPIs=true
```

## Enable the feature gate

`DynamicQuotaOrchestration` is an alpha feature and is disabled by default.

Follow the
[feature gates configuration instructions](/docs/installation/#change-the-feature-gates-configuration)
to enable the `DynamicQuotaOrchestration` feature gate in the Kueue controller
manager. If you installed Kueue with Helm, add the gate to
`controllerManager.featureGates` in your values instead, so that the next
`helm upgrade` keeps it.

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
GENERATION=$(kubectl get capacityprovider demo-provider -o jsonpath='{.metadata.generation}')
echo $GENERATION
```

The output is similar to the following:

```
1
```

Then write the status. `observedGeneration` on the condition must match this
generation. If it does not match, Kueue treats the status as stale.

```shell
kubectl patch capacityprovider demo-provider --subresource=status --type=merge -p '{
  "status": {
    "capacity": {"flavors": [{"name": "default-flavor", "resources": {"cpu": "60", "memory": "300Gi"}}]},
    "conditions": [{
      "type": "CapacitySynchronized",
      "status": "True",
      "reason": "Synchronized",
      "message": "Capacity written by hand for the demo",
      "observedGeneration": '"$GENERATION"',
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
kueue_cluster_queue_info{cluster_queue="team-a",dynamic_quota_orchestrator="org-dqo",parent_cohort="org",replica_role="leader",root_cohort="org"} 1
kueue_cluster_queue_nominal_quota{cluster_queue="team-a",cohort="org",flavor="default-flavor",replica_role="leader",resource="cpu"} 16
kueue_cluster_queue_nominal_quota{cluster_queue="team-b",cohort="org",flavor="default-flavor",replica_role="leader",resource="cpu"} 32
kueue_cohort_subtree_quota{cohort="org",flavor="default-flavor",replica_role="leader",resource="cpu"} 48
```

## Derive capacity from Nodes

Instead of writing the CapacityProvider status yourself, you can let Kueue
derive the capacity from the Nodes of the cluster. This uses the built-in
`kueue.x-k8s.io/local-capacity` provider, which is behind the alpha
`DQOLocalCapacityProvider` feature gate. Enable it together with
`DynamicQuotaOrchestration`. Kueue does not start if only the first one is
enabled.

Create a CapacityProvider with `controllerName: kueue.x-k8s.io/local-capacity`
and list the ResourceFlavors to track:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ResourceFlavor
metadata:
  name: h100
spec:
  nodeLabels:
    example.com/gpu-type: h100
---
apiVersion: kueue.x-k8s.io/v1alpha1
kind: CapacityProvider
metadata:
  name: nodes
spec:
  controllerName: kueue.x-k8s.io/local-capacity
  orchestratedFlavors:
  - name: h100
```

For each listed flavor, Kueue sums `status.allocatable` of the Nodes that match
the flavor's `nodeLabels` and publishes the totals in the CapacityProvider
status. A DynamicQuotaOrchestrator then distributes them as usual.

- A Node counts only if it is `Ready`, not cordoned (`unschedulable`) and not
  being deleted. For a flavor with a `topologyName`, the Node also needs a label
  for every level of the Topology.
- Taints do not exclude a Node. A workload can use a tainted Node only if its
  tolerations cover the taints.
- A flavor without eligible Nodes is published with no resources, so its quota
  becomes 0.
- Running workloads are not subtracted. To keep headroom, set
  `effectiveCapacityMultiplier` on the provider in the DynamicQuotaOrchestrator's
  `spec.capacityDiscovery.providers`.
- Set the flavor's `nodeLabels` only from a trusted source, for example labels
  under the `node-restriction.kubernetes.io/` prefix. A kubelet that can label
  its own Node can move that Node's capacity into a flavor.
- This provider is for fixed or externally scaled node pools. Quota that never
  exceeds the current Nodes cannot trigger a ProvisioningRequest scale-up.

If the provider cannot compute a correct result, it keeps the last published
capacity and sets `CapacitySynchronized=False`, so quota does not change:

| Reason | What to check |
|---|---|
| `SourceUnavailable` | Kueue could not list Nodes. |
| `Misconfigured` | A flavor or its Topology does not exist, a flavor is listed by more than one local-capacity CapacityProvider, a Node matches more than one orchestrated flavor, a flavor has more than 64 resources, or `spec.parameters` is set. The message names the flavor or Node. |

## Troubleshooting

| Condition | Reason | What to check |
|---|---|---|
| `EffectiveCapacityComputed=False` | `ProviderNotReady` | The provider's `CapacitySynchronized` condition is not `True`, or its `observedGeneration` does not match `metadata.generation`. The message names the provider. |
| `EffectiveCapacityComputed=False` | `Misconfigured` | A name in `spec.capacityDiscovery.providers` does not match a CapacityProvider. |
| `EffectiveCapacityComputed=False` | `AggregationFailed` | The total has more than 128 flavors, or more than 64 resources in one flavor. |
| `Distributed=False` | `EffectiveCapacityNotComputed` | Fix discovery first. The existing effective quotas stay in place. |
| `Distributed=False` | `ConflictingDynamicQuotaOrchestrator` | Another orchestrator has a higher root, or the same root and an earlier `creationTimestamp`. With equal timestamps, the smaller UID wins. |
| `Distributed=False` | `EffectiveQuotasConflict` | A ClusterQueue or Cohort in the subtree has effective quotas from another active orchestrator, or from a controller that is not DQO. Usually this is a leftover after the object moved or the other orchestrator's `subtreeRootQuotaRef` changed. Clear the object's `status.effectiveQuotas`, or delete the other orchestrator. |
| `Distributed=False` | `Misconfigured` | The ClusterQueue or Cohort object named in `subtreeRootQuotaRef` does not exist. A Cohort that is only named in a ClusterQueue's `spec.cohortName` does not count. Create a Cohort object for it. |

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
