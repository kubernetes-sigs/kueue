---
title: "Topology Aware Scheduling"
date: 2024-04-11
weight: 6
description: >
  Allows scheduling of Pods based on the topology of nodes in a data center.
---

{{< feature-state state="alpha" for_version="v0.9" >}}

It is common that AI/ML workloads require a significant amount of pod-to-pod
communication. Therefore the network bandwidth between the running Pods
translates into the workload execution time, and the cost of running
such workloads. The available bandwidth between the Pods depends on the placement
of the Nodes, running the Pods, in the data center.

We observe that the data centers have a hierarchical structure of their
organizational units, like racks and blocks, where there are multiple nodes
within a rack, and there are multiple racks within a block. Pods running within
the same organizational unit have better network bandwidth than Pods on
different units. We say that nods placed in different racks are more distant
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

|  node  |  cloud.provider.com/topology-block | cloud.provider.com/topology-rack |
|:------:|:----------------------------------:|:--------------------------------:|
| node-1 |               block-1              |              rack-1              |
| node-2 |               block-1              |              rack-2              |
| node-3 |               block-2              |              rack-1              |
| node-4 |               block-2              |              rack-3              |

Note that, there is a pair of nodes, node-1 and node-3, with the same value of
the "cloud.provider.com/topology-rack" label, but in different blocks.

### Capacity calculation

For each PodSet TAS determines the current free capacity per each topology
domain (like a given rack) by:
- including Node allocatable capacity (based on the `.status.allocatable` field)
  of only ready Nodes (with `Ready=True` condition),
- subtracting the usage coming from all other admitted TAS workloads,
- subtracting the usage coming from all other non-TAS Pods (owned mainly by
  DaemonSets, but also including static Pods, Deployments, etc.).

### Admin-facing APIs

As an admin, in order to enable the feature you need to:
1. ensure the `TopologyAwareScheduling` feature gate is enabled
2. create at least one instance of the `Topology` API
3. reference the `Topology` API from a dedicated ResourceFlavor by the
   `.spec.topologyName` field

#### Example

{{< include "examples/tas/sample-queues.yaml" "yaml" >}}

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
- `kueue.x-k8s.io/podset-required-topology` - indicates indicates that a PodSet
  requires Topology Aware Scheduling, and requires scheduling all pods on nodes
	within the same topology domain corresponding to the topology level
	indicated by the annotation value (e.g. within a rack or within a block).

#### Example

Here is an example Job a user might submit to use TAS. It assumes there exists
a LocalQueue named `tas-user-queue` which refernces the ClusterQueue pointing
to a TAS ResourceFlavor.

{{< include "examples/tas/sample-job-preferred.yaml" "yaml" >}}

### Limitations

Currently, there are multiple limitations for the compatibility of the feature
with other features. In particular, a ClusterQueue referencing a TAS Resource
Flavor (with the `.spec.topologyName` field) is marked as inactive in the
following scenarios:
- the CQ is in cohort (`.spec.cohort` is set)
- the CQ is using [preemption](preemption.md)
- the CQ is using [MultiKueue](multikueue.md) or
  [ProvisioningRequest](/docs/admission-check-controllers/provisioning/) admission checks

These usage scenarios are considered to be supported in the future releases
of Kueue.

## Drawbacks

When enabling the feature Kueue starts to keep track of all Pods and all nodes
in the system, which results in larger memory requirements for Kueue.
Additionally, Kueue will take longer to schedule the workloads as it needs to
take the topology information into account.
