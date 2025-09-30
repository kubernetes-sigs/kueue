---
title: "Topology Aware Scheduling"
date: 2024-04-11
weight: 6
description: >
  Allows scheduling of Pods based on the topology of nodes in a data center.
---

{{< feature-state state="beta" for_version="v0.14" >}}
{{% alert title="Note" color="primary" %}}
`TopologyAwareScheduling` is currently a beta feature and is enabled by default.

You can disable it by editing the `TopologyAwareScheduling` feature gate. Refer to the
[Installation guide](/docs/installation/#change-the-feature-gates-configuration)
for instructions on configuring feature gates.
{{% /alert %}}

It is common that AI/ML workloads require a significant amount of pod-to-pod
communication. Therefore the network bandwidth between the running Pods
translates into the workload execution time, and the cost of running
such workloads. The available bandwidth between the Pods depends on the placement
of the Nodes, running the Pods, in the data center.

We observe that the data centers have a hierarchical structure of their
organizational units, like racks and blocks, where there are multiple nodes
within a rack, and there are multiple racks within a block. Pods running within
the same organizational unit have better network bandwidth than Pods on
different units. We say that nodes placed in different racks are more distant
than nodes placed within the same rack. Similarly, nodes placed in different
blocks are more distant than two nodes within the same block.

In this feature (called Topology Aware Scheduling, or TAS for short) we
introduce a convention to represent the
[hierarchical node topology information](#node-topology-information), and a set
of APIs for Kueue administrators and users to utilize the information
to optimize the Pod placement.

### Node topology information

We propose a lightweight model for representing the hierarchy of nodes within a
data center by using node labels. In this model the node labels are set up by a
cloud provider, or set up manually by administrators of on-premise clusters.

Additionally, we assume that every node used for TAS has a set of the labels
which identifies uniquely its location in the tree structure. We do not assume
global uniqueness of labels on each level, i.e. there could be two nodes with
the same "rack" label, but in different "blocks".

For example, this is a representation of the data center hierarchy;

|  node  | cloud.provider.com/topology-block | cloud.provider.com/topology-rack |
| :----: | :-------------------------------: | :------------------------------: |
| node-1 |              block-1              |              rack-1              |
| node-2 |              block-1              |              rack-2              |
| node-3 |              block-2              |              rack-1              |
| node-4 |              block-2              |              rack-3              |

Note that, there is a pair of nodes, node-1 and node-3, with the same value of
the "cloud.provider.com/topology-rack" label, but in different blocks.

### Capacity calculation

For each PodSet TAS determines the current free capacity per each topology
domain (like a given rack) by:
- including Node allocatable capacity (based on the `.status.allocatable` field)
  of only ready (with `Ready=True` condition) and schedulable (with `.spec.unschedulable=false`) Nodes,
- subtracting the usage coming from all other admitted TAS workloads,
- subtracting the usage coming from all other non-TAS Pods (owned mainly by
  DaemonSets, but also including static Pods, Deployments, etc.).

### Admin-facing APIs

As an admin, in order to enable the feature you need to:
1. create at least one instance of the `Topology` API
2. reference the `Topology` API from a dedicated ResourceFlavor by the
   `.spec.topologyName` field

#### Example

{{< include "examples/tas/sample-queues.yaml" "yaml" >}}

An example for managing GPUs:
{{< include "examples/tas/sample-gpu-queues.yaml" "yaml" >}}

### User-facing APIs

Once TAS is configured and ready to be used, you can create Jobs with the
following annotations set at the PodTemplate level:
- `kueue.x-k8s.io/podset-preferred-topology` - indicates that a PodSet requires
	Topology Aware Scheduling, but scheduling all pods within pods on nodes
	within the same topology domain is a preference rather than requirement.
	The levels are evaluated one-by-one going up from the level indicated by
	the annotation. If the PodSet cannot fit within a given topology domain
	then the next topology level up is considered. If the PodSet cannot fit
	at the highest topology level, then it gets admitted as distributed
	among multiple topology domains.
- `kueue.x-k8s.io/podset-required-topology` - indicates that a PodSet
  requires Topology Aware Scheduling, and requires scheduling all pods on nodes
	within the same topology domain corresponding to the topology level
	indicated by the annotation value (e.g. within a rack or within a block).
- `kueue.x-k8s.io/podset-unconstrained-topology` - indicates that a PodSet requires
    Topology Aware Scheduling, and requires scheduling all pods on any nodes without
    topology considerations. In other words, this considers if all pods could be accommodated 
    within any nodes which helps to minimize fragmentation by filling the small gaps
    on nodes across the cluster.

#### Example

Here is an example Job a user might submit to use TAS. It assumes there exists
a LocalQueue named `tas-user-queue` which refernces the ClusterQueue pointing
to a TAS ResourceFlavor.

{{< include "examples/tas/sample-job-preferred.yaml" "yaml" >}}

### ClusterAutoscaler support

TAS integrates with the [Kubernetes ClusterAutoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler)
through the [Provisioning AdmissionCheck](/docs/admission-check-controllers/provisioning/).

When a workload is assigned to the TAS ResourceFlavor with Provisioning
AdmissionCheck, then its admission flow has the following stages:
1. **Quota reservation**: quota is reserved, and the Workload obtains the
  `QuotaReserved` condition. Preemptions are evaluated if configured.
2. **Admission checks**: Kueue waits for all AdmissionChecks, including the
  Provisioning one, to report `Ready` inside the Workload's
  `status.admissionChecks` field.
3. **Topology assignment**: Kueue sets topology assignment, on the Workload
  object, calculated taking into account any newly provisioned nodes.

Check also [PodSet updates in ProvisioningRequestConfig](site/content/en/docs/admission-check-controllers/provisioning.md)
to see how you can configure Kueue if you want to restrict scheduling to the
newly provisioned nodes (assuming the provisioning class supports it).

### Hot swap support
{{< feature-state state="beta" for_version="v0.14" >}}
{{% alert title="Note" color="primary" %}}
`TASFailedNodeReplacement` is currently a beta feature and is enabled by default.

You can disable it by editing the `TASFailedNodeReplacement` feature gate. Refer to the
[Installation guide](/docs/installation/#change-the-feature-gates-configuration)
for instructions on configuring feature gates.
{{% /alert %}}

When the lowest level of Topology is set to node, TAS finds a fixed assignment
of pods to nodes and injects a NodeSelector to make sure the pods get scheduled
on the selected nodes. But this means that in case
of any node failures or deletions, which occur during the runtime of a workload,
the workload cannot run on any other nodes. In order to avoid costly re-scheduling
of the entire TAS workload we introduce the node hot swap feature.

With this feature, TAS tries to find a replacement of the failed or deleted node for
all the affected workloads, without changing the rest of the topology assignment.
Currently this works only for a single node failure at the time and in case of multiple failures,
the workload gets evicted.

#### Replace Node on Pod termination 

{{< feature-state state="beta" for_version="v0.14" >}}
{{% alert title="Note" color="primary" %}}
`TASReplaceNodeOnPodTermination` is currently a beta feature and is enabled by default.

You can disable it by editing the `TASReplaceNodeOnPodTermination` feature gate. Refer to the
[Installation guide](/docs/installation/#change-the-feature-gates-configuration)
for instructions on configuring feature gates.
{{% /alert %}}

By default, the node is assumed to have failed if its `conditions.Status.Ready`
is not `True` for at least 30 seconds or if the node is missing (removed from the cluster).
Since Kueue v0.13, the `TASReplaceNodeOnPodTermination` feature, introduced an additional heuristic:
a node is also considered failed if it is `NotReady` and the workload's Pods scheduled on that node are either terminated or terminating.
If this happens Kueue will immediately look for replacement without waiting 30 seconds.

Note that those two heuristics are mutually exclusive and depend on the value of the `TASReplaceNodeOnPodTermination` feature gate.

Note that finding a replacement node that meets all the requirements (e.g. the same type of machine placed in the rack that Kueue had previously assigned to the workload) may not always be possible.
If a workload is big enough to cover the whole topology domain (e.g. block or rack) it's inevitable that there will be no replacement within the same domain.
Hence, we recommend using FailFast mode described below or [WaitForPodsReady](/docs/tasks/manage/setup_wait_for_pods_ready/)
and configuring `waitForPodsReady.recoveryTimeout`, to prevent the workloads from
waiting for the replacement indefinitely.

#### Fast Hot swap
{{< feature-state state="beta" for_version="v0.14" >}}
{{% alert title="Note" color="primary" %}}
`TASFailedNodeReplacementFailFast` is currently a beta feature and is enabled by default.

You can disable it by editing the `TASFailedNodeReplacementFailFast` feature gate. Refer to the
[Installation guide](/docs/installation/#change-the-feature-gates-configuration)
for instructions on configuring feature gates.
{{% /alert %}}

By default, Kueue tries to find a replacement for a failed node until it succeeds or until the workload is evicted (for example, by `waitForPodsReady.recoveryTimeout`). To prevent Kueue from retrying indefinitely, you can enable the `TASFailedNodeReplacementFailFast` feature gate. When enabled, Kueue will only attempt to find a replacement node once. If it fails, it will not try again, and the workload will get evicted and requeued.

#### Usage Scenarios

Here are a few scenarios that can happen when both `TASReplaceNodeOnPodTermination` and `TASFailedNodeReplacementFailFast` are enabled:

1. **Node becomes `NotReady`, pods are terminated, and a replacement is found:**
   - A node running a pod from a TAS workload becomes `NotReady`.
   - The pods on that node are terminated.
   - With `TASReplaceNodeOnPodTermination` enabled, Kueue immediately looks for a replacement.
   - A replacement node is available, and Kueue successfully swaps the failed node.
   - The workload continues running on the new node.

2. **Node becomes `NotReady`, pods are terminated, and no replacement is found:**
   - A node running a pod from a TAS workload becomes `NotReady`.
   - The pods on that node are terminated.
   - Kueue immediately looks for a replacement but cannot find one.
   - With `TASFailedNodeReplacementFailFast` enabled, Kueue will not retry.
   - The workload immediately gets evicted and requeued (doesn't wait 30s or until `waitForPodsReady.recoveryTimeout` expires)

3. **Node gets deleted**
   - Same scenarios apply as in 1. and 2.

4. **The Workload requires the whole rack and one of the nodes becomes `NotReady`:**
   - A node running a pod from a TAS workload becomes `NotReady`.
   - The pods on that node are terminated.
   - Kueue immediately looks for a replacement but since the workload requires the whole rack, it cannot find the replacement.
   - With `TASFailedNodeReplacementFailFast` enabled, Kueue does not retry the replacement search.
   - The workload immediately gets evicted and requeued (doesn't wait 30s or until `waitForPodsReady.recoveryTimeout` expires)

##### Feature Gate Interaction Matrix

The following table summarizes the behavior based on the combination of the feature gates. If `TASFailedNodeReplacement` is `false`, the other two gates have no effect.

**Feature Gate Legend:**
- **FNR**: `TASFailedNodeReplacement`
- **RNO**: `TASReplaceNodeOnPodTermination`
- **FNFF**: `TASFailedNodeReplacementFailFast`

| `FNR` | `RNO` | `FNFF` | End Behavior |
| :---- | :---- | :---- | :----------- |
| `false` | *any* | *any* | **Hot swap is disabled.**<br>Workloads will not have failed nodes replaced and may get stuck. |
| `true` | `false` | `false` | **Default Hot Swap**<ul><li>**Trigger**: Node is `NotReady` for > 30 seconds.</li><li>**Behavior**: Retries replacement until it succeeds or the workload is evicted.</li></ul> |
| `true` | `true` | `false` | **Hot Swap with Pod Termination Trigger**<ul><li>**Trigger**: Node is `NotReady` for > 30s, OR a workload pod is terminating.</li><li>**Behavior**: Retries replacement until it succeeds or the workload is evicted.</li></ul> |
| `true` | `false` | `true` | **Fast Hot Swap**<ul><li>**Trigger**: Node is `NotReady` for > 30 seconds.</li><li>**Behavior**: Attempts replacement **only once**. Evicts the workload if it fails.</li></ul> |
| `true` | `true` | `true` | **Fast Hot Swap with Pod Termination Trigger**<ul><li>**Trigger**: Node is `NotReady`, AND a workload pod is terminating.</li><li>**Behavior**: Attempts replacement **only once**. Evicts the workload if it fails.</li></ul> |

**Recommended configuration**

We recommend keeping all three feature gates enabled to ensure the fastest feedback loop for workloads affected by node failures.

### Limitations

Currently, there are limitations for the compatibility of TAS with other
features, including:
- some scheduling directives (e.g. pod affinities and anti-affinities) are ignored,
- the "podset-required-topology" annotation may fail if the underlying
  ClusterAutoscaler cannot provision nodes that satisfy the domain constraint,
- a ClusterQueue for [MultiKueue](multikueue.md) referencing a ResourceFlavor
with Topology name (`.spec.topologyName`) is marked as inactive.
- The taints on the nodes are not respected unless `kubernetes.io/hostname` is on the lowest topology level.

These usage scenarios are considered to be supported in the future releases
of Kueue.

## Drawbacks

When enabling the feature Kueue starts to keep track of all Pods and all nodes
in the system, which results in larger memory requirements for Kueue.
Additionally, Kueue will take longer to schedule the workloads as it needs to
take the topology information into account.
