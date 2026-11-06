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

1. When admitting a `Workload`, Kueue evaluates the [`.nodeSelector`](https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/#nodeselector) and [`.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution`](https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/#node-affinity) fields in the PodSpecs of your [`Workload`](/docs/concepts/workload) against the `ResourceFlavor`'s `.spec.nodeLabels`.
   `ResourceFlavors` that conflict with the `Workload`'s node selector or required node affinity cannot be assigned to a `Workload`'s PodSet (see [How Kueue evaluates node selectors and node affinity](#how-kueue-evaluates-node-selectors-and-node-affinity) for details).

2. Once the `Workload` is admitted:
   - Kueue adds the `ResourceFlavor`'s `.spec.nodeLabels` to the `.nodeSelector` of the underlying `Workload` Pod templates if the `Workload` did not already specify those labels as part of its `.nodeSelector`.

     For example, for a [`batch/v1.Job`](https://kubernetes.io/docs/concepts/workloads/controllers/job/), Kueue adds the labels to the `.spec.template.spec.nodeSelector` field.
     This guarantees that the `Workload`'s Pods can only be scheduled on the nodes targeted by the flavor that Kueue assigned to the `Workload` (provided flavors assigned across different resource groups do not define conflicting values for the same label key).

   - Kueue adds the `.spec.tolerations` to the underlying `Workload` Pod templates.

     For example, for a [`batch/v1.Job`](https://kubernetes.io/docs/concepts/workloads/controllers/job/), Kueue adds the tolerations to the `.spec.template.spec.tolerations` field.
     This allows the `Workload`'s Pods to be scheduled on nodes having specific taints.

### How Kueue evaluates node selectors and node affinity

Unlike `kube-scheduler`, which evaluates a `Node` against the Pod's complete `.nodeSelector` and `.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution`, Kueue evaluates each `ResourceFlavor` using **only the label keys declared in that flavor's `.spec.nodeLabels`**:

- **Per-flavor key filtering:** When evaluating a candidate `ResourceFlavor`, Kueue filters the Pod's `.nodeSelector` and `requiredDuringSchedulingIgnoredDuringExecution` `matchExpressions` down to the keys present in that `ResourceFlavor`'s `.spec.nodeLabels` (not the union of keys across all flavors in the `ClusterQueue` `resourceGroup`).
- **Non-contradiction matching:** Label keys on the Pod that are not declared in the candidate `ResourceFlavor`'s `.spec.nodeLabels` are ignored during flavor assignment. This allows a `Workload` to constrain scheduling to specific nodes within a flavor (for example, using `kubernetes.io/hostname` or rack labels) without being rejected during flavor selection. After Kueue admits the `Workload` and injects the matched flavor's `.spec.nodeLabels`, `kube-scheduler` enforces the complete `.nodeSelector` and `nodeAffinity` against actual `Node` objects.
- **Node affinity terms:** Because `nodeSelectorTerms` under `requiredDuringSchedulingIgnoredDuringExecution` are combined with a logical `OR`, if removing unrecognized keys leaves any `nodeSelectorTerm` empty, the `nodeAffinity` constraint is treated as satisfied for that `ResourceFlavor` and matching reduces to `.nodeSelector`.

For example, consider a `Workload` Pod with `.nodeSelector: {pool: gpu, kubernetes.io/hostname: node-1}`. In the table below, the Kubernetes `Node` evaluation column reflects a candidate node carrying only the labels of that candidate `ResourceFlavor`:

| `ResourceFlavor` `.spec.nodeLabels` | Kueue flavor evaluation | Kubernetes `Node` evaluation | Resulting Pod `.nodeSelector` after admission |
| --- | --- | --- | --- |
| `{pool: gpu}` | **Match** (`kubernetes.io/hostname` is not in the flavor's keys and is ignored) | Reject (missing `kubernetes.io/hostname: node-1`) | `{pool: gpu, kubernetes.io/hostname: node-1}` |
| `{pool: gpu, tier: standard}` | **Match** (`pool` matches; `tier` is not contradicted) | Reject (missing `kubernetes.io/hostname: node-1`) | `{pool: gpu, tier: standard, kubernetes.io/hostname: node-1}` |
| `{pool: gpu, kubernetes.io/hostname: node-1}` | **Match** (all flavor keys match) | **Match** | `{pool: gpu, kubernetes.io/hostname: node-1}` |
| `{pool: cpu}` | **Reject** (`pool: cpu` contradicts `pool: gpu`) | Reject (`pool` mismatch and missing `kubernetes.io/hostname`) | Not admitted to this flavor |
| `{}` ([empty `ResourceFlavor`](#empty-resourceflavor)) | **Match** (no keys checked) | Reject (missing both keys) | `{pool: gpu, kubernetes.io/hostname: node-1}` |

{{% alert title="Warning" color="warning" %}}
Because each `ResourceFlavor` ignores `.nodeSelector` keys that it does not declare in its own `.spec.nodeLabels`, avoid using **different label keys** to distinguish mutually exclusive sibling flavors in the same `resourceGroup`.

For example, if `flavor-on-demand` defines `.spec.nodeLabels: {gpu: l4, on-demand: "true"}` and `flavor-spot` defines `.spec.nodeLabels: {gpu: l4, spot: "true"}`, a `Workload` with `.nodeSelector: {gpu: l4, spot: "true"}` will still match `flavor-on-demand` (because `flavor-on-demand` does not declare the `spot` key and therefore ignores `spot: "true"`).

To ensure a `Workload` only matches the intended `ResourceFlavor`, use one of the following patterns:
- **Share a common label key across sibling flavors** with distinct values, such as `provisioning: on-demand` on `flavor-on-demand` and `provisioning: spot` on `flavor-spot`. Because both flavors declare the `provisioning` key, `flavor-on-demand` evaluates `provisioning: spot` and rejects the mismatch.
- **Configure `.spec.nodeTaints` on unwanted sibling flavors** with `NoSchedule` or `NoExecute` taints that neither the `Workload` nor those flavors' `.spec.tolerations` tolerate. Add matching tolerations to the `Workload` for taints on the intended flavor. See [ResourceFlavor taints for user-selective scheduling](#resourceflavor-taints-for-user-selective-scheduling).
{{% /alert %}}

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
On the other hand, when the ResourceFlavor has also set the matching tolerations in `.spec.tolerations`, 
then the taints are not considered during [admission](/docs/concepts#admission).
As opposed to the behavior for [ResourceFlavor tolerations for automatic scheduling](#resourceflavor-tolerations-for-automatic-scheduling), Kueue does not add tolerations for the flavor taints.

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
- Read the [API reference](/docs/reference/kueue.v1beta2/#kueue-x-k8s-io-v1beta2-ResourceFlavor) for `ResourceFlavor`
