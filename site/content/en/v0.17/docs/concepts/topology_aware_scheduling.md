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
[Installation guide](/v0.17/docs/installation/#change-the-feature-gates-configuration)
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

{{< include "v0.17/examples/tas/sample-queues.yaml" "yaml" >}}

An example for managing GPUs:
{{< include "v0.17/examples/tas/sample-gpu-queues.yaml" "yaml" >}}

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
- `kueue.x-k8s.io/podset-group-name` - indicates the name of the group of PodSets. PodSet Group
    is a unit of flavor assignment and topology domain fitting. This is useful when you want to
    ensure that multiple PodSets are scheduled in the same topology domain.
- `kueue.x-k8s.io/podset-slice-required-topology-constraints` - defines multi-layer slice
    topology constraints as a JSON-encoded array. Each entry specifies a topology level and
    slice size, ordered from the outermost (coarsest) to the innermost (finest) layer. At most
    3 layers are supported. This annotation is mutually exclusive with
    `kueue.x-k8s.io/podset-slice-required-topology` and `kueue.x-k8s.io/podset-slice-size`.
    Requires the `TASMultiLayerTopology` feature gate.

#### Example

Here is an example Job a user might submit to use TAS. It assumes there exists
a LocalQueue named `tas-user-queue` which refernces the ClusterQueue pointing
to a TAS ResourceFlavor.

{{< include "v0.17/examples/tas/sample-job-preferred.yaml" "yaml" >}}

### ClusterAutoscaler support

TAS integrates with the [Kubernetes ClusterAutoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler)
through the [Provisioning AdmissionCheck](/v0.17/docs/concepts/admission_check/provisioning_request/).

When a workload is assigned to the TAS ResourceFlavor with Provisioning
AdmissionCheck, then its admission flow has the following stages:
1. **Quota reservation**: quota is reserved, and the Workload obtains the
  `QuotaReserved` condition. Preemptions are evaluated if configured.
2. **Admission checks**: Kueue waits for all AdmissionChecks, including the
  Provisioning one, to report `Ready` inside the Workload's
  `status.admissionChecks` field.
3. **Topology assignment**: Kueue sets topology assignment, on the Workload
  object, calculated taking into account any newly provisioned nodes.

Check also [PodSet updates in ProvisioningRequestConfig](/v0.17/docs/concepts/admission_check/provisioning_request/#podset-updates)
to see how you can configure Kueue if you want to restrict scheduling to the
newly provisioned nodes (assuming the provisioning class supports it).

### Hot swap support
{{< feature-state state="beta" for_version="v0.14" >}}
{{% alert title="Note" color="primary" %}}
`TASFailedNodeReplacement` is currently a beta feature and is enabled by default.

You can disable it by editing the `TASFailedNodeReplacement` feature gate. Refer to the
[Installation guide](/v0.17/docs/installation/#change-the-feature-gates-configuration)
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
[Installation guide](/v0.17/docs/installation/#change-the-feature-gates-configuration)
for instructions on configuring feature gates.
{{% /alert %}}

By default, the node is assumed to have failed if it is missing (removed from the
cluster), or if its `conditions.Status.Ready` is not `True` and the workload's Pods
scheduled on that node are either terminated or terminating. A node with
still-running Pods is not considered failed, no matter how long its
`conditions.Status.Ready` has not been `True`: nodes can recover after an arbitrary
amount of time, and reassigning while the Pods are alive makes the stored assignment
diverge from where the Pods actually run.

Before v0.19, a node was additionally assumed to have failed once its
`conditions.Status.Ready` was not `True` for at least 30 seconds, regardless of Pod
state. This fixed-time marking is deprecated: it remains available behind the
`TASReplaceNodeDueToNotReadyOverFixedTime` feature gate (enabled by default in
v0.17–v0.18, disabled by default and deprecated since v0.19) and is planned for
removal. Disabling `TASReplaceNodeOnPodTermination` retains the pre-v0.14 behavior of
marking the node after the fixed time regardless of Pod state.

Note that finding a replacement node that meets all the requirements (e.g. the same type of machine placed in the rack that Kueue had previously assigned to the workload) may not always be possible.
If a workload is big enough to cover the whole topology domain (e.g. block or rack) it's inevitable that there will be no replacement within the same domain.
Hence, we recommend using FailFast mode described below or [WaitForPodsReady](/v0.17/docs/tasks/manage/setup_wait_for_pods_ready/)
and configuring `waitForPodsReady.recoveryTimeout`, to prevent the workloads from
waiting for the replacement indefinitely.

#### Fast Hot swap
{{< feature-state state="beta" for_version="v0.14" >}}
{{% alert title="Note" color="primary" %}}
`TASFailedNodeReplacementFailFast` is currently a beta feature and is enabled by default.

You can disable it by editing the `TASFailedNodeReplacementFailFast` feature gate. Refer to the
[Installation guide](/v0.17/docs/installation/#change-the-feature-gates-configuration)
for instructions on configuring feature gates.
{{% /alert %}}

By default, Kueue tries to find a replacement for a failed node until it succeeds or until the workload is evicted (for example, by `waitForPodsReady.recoveryTimeout`). To prevent Kueue from retrying indefinitely, you can enable the `TASFailedNodeReplacementFailFast` feature gate. When enabled, Kueue will only attempt to find a replacement node once. If it fails, it will not try again, and the workload will get evicted and requeued.

#### Skip reassignment for Workloads owned by a single Pod
{{< feature-state state="alpha" for_version="v0.17" >}}
{{% alert title="Note" color="primary" %}}
`SkipReassignmentForPodOwnedWorkloads` is an alpha feature, disabled by default in v0.17.

You can disable it by editing the `SkipReassignmentForPodOwnedWorkloads` feature gate. Refer to the
[Installation guide](/v0.17/docs/installation/#change-the-feature-gates-configuration)
for instructions on configuring feature gates.
{{% /alert %}}

Node replacement assumes that the workload's controller re-creates pods within the
same Workload (as Job, JobSet or LeaderWorkerSet do), so that the re-created pods can
follow the updated topology assignment. A Workload owned by a single Pod — bare pods
and, for example, Deployment replicas managed through the
[pod integration](/v0.17/docs/tasks/run/plain_pods/) — does not have this property: the
Workload is deleted together with its pod, so no future pod can consume a replacement
assignment. Replacing a node for such a Workload only makes its stored assignment
diverge from the node its still-running pod occupies, which corrupts the per-node
capacity accounting: the pod's real node is treated as free and gets over-admitted,
while the replacement node is blocked for other admissions.

With the `SkipReassignmentForPodOwnedWorkloads` feature gate enabled, Kueue keeps the
existing topology assignment of a Workload owned by exactly one Pod instead of
computing a replacement. The failed node is still tracked in
`.status.unhealthyNodes` and the field is cleared on the next successful scheduling
pass. Recovery happens through the pod lifecycle: when the pod terminates, its owning
controller (for example a ReplicaSet) creates a new pod, which arrives as a new
Workload and is admitted with a fresh assignment. Workloads with any other owner,
including pod groups (which can receive user-created replacement pods into the same
Workload), keep the replacement behavior described above.

The gate also covers the eviction requeue path. Without it, a pod-owned Workload
evicted by preemption is requeued and re-admitted with a freshly computed
assignment while its pod is still draining the termination grace period on the
original node — an assignment no pod can consume, blocking the newly assigned
node's capacity for the full grace period. With the gate enabled, evicted pod-owned
Workloads get `Requeued=False` instead: the Workload finishes with its pod, and the
owning controller's replacement pod arrives as a new Workload.

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
| `true` | *any* | `false` | **Default Hot Swap**<ul><li>**Trigger**: Node is `NotReady` AND its workload pods are terminating or terminated.</li><li>**Behavior**: Retries replacement until it succeeds or the workload is evicted.</li></ul> |
| `true` | *any* | `true` | **Fast Hot Swap**<ul><li>**Trigger**: Node is `NotReady` AND its workload pods are terminating or terminated.</li><li>**Behavior**: Attempts replacement **only once**. Evicts the workload if it fails.</li></ul> |

With the deprecated `TASReplaceNodeDueToNotReadyOverFixedTime` gate enabled, the
not-ready trigger depends on `TASReplaceNodeOnPodTermination` (`RNO`):
- `RNO` enabled: the node is treated as failed when its workload Pods are terminating
  or terminated, or after 30 seconds of `NotReady`, whichever comes first.
- `RNO` disabled: the node is treated as failed only after 30 seconds of `NotReady`
  (the pre-v0.14 behavior), regardless of Pod state.

With the gate disabled (the default since v0.19), Pod termination is the only
not-ready trigger while `RNO` remains enabled (its default); disabling `RNO`
retains the fixed-time marking. Nodes that are deleted or lack a `Ready`
condition are treated as failed immediately in all configurations. To disable node
replacement entirely, use the `TASFailedNodeReplacement` feature gate instead.

**Recommended configuration**

We recommend keeping all three feature gates enabled to ensure the fastest feedback loop for workloads affected by node failures.

#### Balanced Placement
{{< feature-state state="alpha" for_version="v0.15" >}}
{{% alert title="Note" color="primary" %}}
`TASBalancedPlacement` is currently an alpha feature and is disabled by default.

You can enable it by editing the `TASBalancedPlacement` feature gate. Refer to the
[Installation guide](/v0.17/docs/installation/#change-the-feature-gates-configuration)
for instructions on configuring feature gates.
{{% /alert %}}

The balanced placement algorithm provides an alternative to the greedy packing strategies. Instead of
iterating over the domains sorted from largest to smallest available space (or based on some other criteria)
and trying to pack as many pods/slices as possible to each domain until the request fits, it first finds the
optimal set of domains that fit the request and then distributes the pods/slices as evenly as possible across
these domains.

Greedy placement strategies (such as `BestFit` and `LeastFreeCapacity`) might result in a placement with a small
number of pods assigned to the last considered domain. For example 12 pods distributed among domains with
capacities (10,10) will be placed (10,2). However, in some applications, a more balanced placement (6,6) would
be more efficient. Some examples of such cases would be all-to-all communication procedures (e.g. Allgather)
since more balanced placement leads to more efficient cross-domain traffic.

To use this feature, use the `kueue.x-k8s.io/podset-preferred-topology` annotation on the Job. Kueue TAS makes 
sure that the minimum number of pods (or slices) placed on any domain **one level below** the indicated level
will be maximied. Also (as a second criterion) the number of domains used on the indicated level will be
minimized. However, if the Job would not fit within a single domain **one level above** the indicated level,
Kueue will not perform the balanced placement and will fallback to the standard TAS algorithm.

#### Multi-Layer Topology
{{< feature-state state="alpha" for_version="v0.17" >}}
{{% alert title="Note" color="primary" %}}
`TASMultiLayerTopology` is currently an alpha feature and is disabled by default.

You can enable it by editing the `TASMultiLayerTopology` feature gate. Refer to the
[Installation guide](/v0.17/docs/installation/#change-the-feature-gates-configuration)
for instructions on configuring feature gates.
{{% /alert %}}

Multi-layer topology allows you to define up to 3 layers of slice topology constraints,
enabling fine-grained placement across deep topology hierarchies. This is useful in data
centers with multiple topology levels (e.g., datacenter, block, rack) where you need
different slice sizes at each level.

With the single-layer approach (`kueue.x-k8s.io/podset-slice-required-topology` +
`kueue.x-k8s.io/podset-slice-size`), you can only specify one topology level and one
slice size. Multi-layer topology extends this by letting you specify constraints at
multiple levels simultaneously.

For example, if you have 64 pods and want groups of 32 pods within the same block and
groups of 16 pods within the same rack, you can express this with a single annotation:

```yaml
kueue.x-k8s.io/podset-slice-required-topology-constraints: |
  [
    {"topology": "cloud.provider.com/topology-block", "size": 32},
    {"topology": "cloud.provider.com/topology-rack", "size": 16}
  ]
```

The constraints are specified as a JSON array ordered from the outermost (coarsest) to
the innermost (finest) topology layer. Each inner slice size must evenly divide the
outer slice size.

This annotation is mutually exclusive with `kueue.x-k8s.io/podset-slice-required-topology`
and `kueue.x-k8s.io/podset-slice-size`.

## Drawbacks

When enabling the feature Kueue starts to keep track of all Pods and all nodes
in the system, which results in larger memory requirements for Kueue.
Additionally, Kueue will take longer to schedule the workloads as it needs to
take the topology information into account.
