---
title: "Resource Flavor"
date: 2022-03-14
weight: 2
description: >
  An object that defines available compute resources in a cluster and enables fine-grained resource management by associating workloads with specific node types.
---

Resources in a cluster are typically not homogeneous. Resources could differ in:

- Pricing and availability (for example, spot versus on-demand VMs)
- Architecture (for example, x86 versus ARM CPUs)
- Brands and models (for example, Radeon 7000 versus Nvidia A100 versus T4 GPUs)

A ResourceFlavor is an object that represents these resource variations and allows you to associate them with cluster nodes through labels, taints and tolerations.

{{% alert title="Note" color="primary" %}}
If the resources in your cluster are homogeneous, you can use an [empty ResourceFlavor](#empty-resourceflavor) instead of adding labels to custom ResourceFlavors.
{{% /alert %}}

## ResourceFlavor tolerations for automatic scheduling

**Requires Kubernetes 1.23 or newer**

This approach may be best for teams that wish to schedule pods onto the appropriate nodes automatically.
However, a limitation arises when multiple types of specialized hardware are present, such as two different nvidia.com/gpu resources present in the cluster, i.e., T4 and A100 GPUs.
The system may not differentiate between them, meaning the pods could be scheduled on any of both types of hardware.

To associate a ResourceFlavor with a subset of nodes of your cluster, you can configure the `.spec.nodeLabels` field with matching node labels that uniquely identify the nodes.
If you are using [cluster autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler) (or equivalent controllers), make sure that the controller is configured to add those labels when adding new nodes.

To guarantee that the Pods in the [Workload](/docs/concepts/workload) run on the nodes associated to the flavor that Kueue selected, Kueue performs the following steps:

1. When admitting a Workload, Kueue evaluates the [`.nodeSelector`](https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/#nodeselector) and [`.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution`](https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/#node-affinity) fields in the PodSpecs of your [Workload](/docs/concepts/workload) against the ResourceFlavor labels.
`ResourceFlavors` that don't match the node affinity of the Workload cannot be assigned to a Workload's podSet.


2. Once the Workload is admitted:
   - Kueue adds the ResourceFlavor labels to the `.nodeSelector` of the underlying Workload Pod templates.
   This occurs if the Workload didn't specify the `ResourceFlavor` labels already as part of its nodeSelector.

     For example, for a [batch/v1.Job](https://kubernetes.io/docs/concepts/workloads/controllers/job/), Kueue adds the labels to the `.spec.template.spec.nodeSelector` field.
     This guarantees that the Workload's Pods can only be scheduled on the nodes targeted by the flavor that Kueue assigned to the Workload.

   - Kueue adds the tolerations to the underlying Workload Pod templates.

     For example, for a [batch/v1.Job](https://kubernetes.io/docs/concepts/workloads/controllers/job/), Kueue adds the tolerations to the `.spec.template.spec.tolerations` field.
     This allows the Workload's Pods to be scheduled on nodes having specific taints.

A sample ResourceFlavor of this type looks like the following:

{{< include "examples/admin/resource-flavor-tolerations.yaml" "yaml" >}}

When defining a ResourceFlavor as above, you should set the following values:
- The `.metadata.name` field, which is required to reference a ResourceFlavor from a [ClusterQueue](/docs/concepts/cluster_queue) in the `.spec.resourceGroups[*].flavors[*].name` field.
- `spec.nodeLabels` associates the ResourceFlavor with a node or subset of nodes.
- `spec.tolerations` adds the specified tolerations to the pods that require GPUs.


## ResourceFlavor taints for user-selective scheduling

This approach may be best for teams that wish to schedule their Workload to a specific hardware type selectively.
An additional ResourceFlavor can be created per type of special hardware with a different set of taints and tolerations.
The user can then add the tolerations to their Workload to schedule the pods onto the appropriate node.

By adding the taint at the ResourceFlavor level, we ensure that only workloads that explicitly tolerate that taint can consume the quota.

Taints on the ResourceFlavor work similarly to [Node taints](https://kubernetes.io/docs/concepts/scheduling-eviction/taint-and-toleration/),
but only support the `NoExecute` and `NoSchedule` effects, while `PreferNoSchedule` is ignored.

For Kueue to [admit](/docs/concepts#admission) a Workload to use the ResourceFlavor, the PodSpecs in the Workload should have a toleration for it.
As opposed to the behavior for [ResourceFlavor tolerations for automatic scheduling](#ResourceFlavor-tolerations-for-automatic-scheduling), Kueue does not add tolerations for the flavor taints.

A sample ResourceFlavor looks like the following:

{{< include "examples/admin/resource-flavor-taints.yaml" "yaml" >}}

When defining a ResourceFlavor as above, you should set the following values:
- The `.metadata.name` field, which is required to reference a ResourceFlavor from a [ClusterQueue](/docs/concepts/cluster_queue) in the `.spec.resourceGroups[*].flavors[*].name` field.
- `spec.nodeLabels` associates the ResourceFlavor with a node or subset of nodes.
- `spec.nodeTaints` restricts usage of a ResourceFlavor.
These taints should typically match the taints of the Nodes associated with the ResourceFlavor.

## Empty ResourceFlavor

If your cluster has homogeneous resources, or if you don't need to manage quotas for the different flavors of a resource separately, you can create a ResourceFlavor without any labels or taints.
Such ResourceFlavor is called an empty ResourceFlavor and its definition looks like the following:

{{< include "examples/admin/resource-flavor-empty.yaml" "yaml" >}}

## What's next?

- Learn about [cluster queues](/docs/concepts/cluster_queue).
- Read the [API reference](/docs/reference/kueue.v1beta1/#kueue-x-k8s-io-v1beta1-ResourceFlavor) for `ResourceFlavor`
