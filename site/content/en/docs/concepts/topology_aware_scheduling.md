---
title: "Topology Aware Scheduling"
date: 2024-04-11
weight: 6
description: >
  Allows scheduling of Pods based on the topology of nodes in a data center.
---

{{< feature-state state="alpha" for_version="v0.9" >}}

It is common that AI/ML workloads require a significant amount of pod-to-pod
communication, and thus the network bendwidth between the running Pods
translates into the workload execution time, and the cost of running
such workloads. Then, the connectivity between the Pods depends on the placement
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
of APIs for Kueue administrators and users to utilize the information in order
to optimize the Pod placement.

### Node topology information

We propose a lightweight model for representing the hierarchy of nodes within a
data center by using node labels. In this model the node labels are set up by a
cloud provider, or set up manually by administrators of on-premise clusters.

Additionally, we assume that every node used for TAS has a set of the labels
which identifies uniquely its location in the tree structure. We do not assume
global uniqueness of labels on each level, i.e. there could be two nodes with
the same "rack" label, but in different "blocks".

For example, this is a representation of the dataset hierarchy;

|  node  |  cloud.provider.com/topology-block | cloud.provider.com/topology-rack |
|:------:|:----------------------------------:|:--------------------------------:|
| node-1 |               block-1              |              rack-1              |
| node-2 |               block-1              |              rack-2              |
| node-3 |               block-2              |              rack-1              |
| node-4 |               block-2              |              rack-3              |

Note that, there is a pair of nodes, node-1 and node-3, with the same value of
the "cloud.provider.com/topology-rack" label, but in different blocks.

### Admin-facing APIs

As an admin, in order to enable the feature you need to:
1. ensure the `TopologyAwareScheduling` feature gate is enabled
2. create at least one instance of the `Topology` API
3. reference the `Topology` API from a dedicated ResourceFlavor by the
   `.spec.topologyName` field

#### Example

```yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: Topology
metadata:
  name: "default"
spec:
  levels:
  - nodeLabel: "cloud.provider.com/topology-block"
  - nodeLabel: "cloud.provider.com/topology-rack"
  - nodeLabel: "kubernetes.io/hostname"
---
kind: ResourceFlavor
apiVersion: kueue.x-k8s.io/v1beta1
metadata:
  name: "tas-flavor"
spec:
  nodeLabels:
    cloud.provider.com/: "tas-node-group"
  topologyName: "default"
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "tas-cluster-queue"
spec:
  namespaceSelector: {} # match all.
  resourceGroups:
  - coveredResources: ["cpu", "memory"]
    flavors:
    - name: "tas-flavor"
      resources:
      - name: "cpu"
        nominalQuota: 100
      - name: "memory"
        nominalQuota: 100Gi
```

### User-facing APIs

Once TAS is configured and ready to be used, you can create Jobs with the
following annotations set at the PodTemplate level:
- `kueue.x-k8s.io/podset-required-topology` - indicates that a PodSet requires
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

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  generateName: tas-sample-big-preferred-host
  labels:
    kueue.x-k8s.io/queue-name: tas-user-queue
spec:
  parallelism: 40
  completions: 40
  completionMode: Indexed
  template:
    metadata:
      annotations:
        kueue.x-k8s.io/podset-preferred-topology: "cloud.provider.com/topology-block"
    spec:
      containers:
      - name: dummy-job
        image: gcr.io/k8s-staging-perf-tests/sleep:v0.1.0
        args: ["300s"]
        resources:
          requests:
            cpu: "1"
            memory: "200Mi"
      restartPolicy: Never
```

### Limitations

Currently, there are multiple limitations for the compatibility of the feature
with other features. In particular, a ClusterQueue referencing a TAS Resource
Flavor (with the `.spec.topologyName` field) is marked as inactive in the
following scenarios:
- the CQ is in cohort
- the CQ is using preemption
- the CQ is using MultiKueue or ProvisioningRequest admission checks

These usage scenarios are considered to be supported in the future releases
of Kueue.

## Drawbacks

When enabling the feature Kueue starts to keep track of all Pods and all nodes
in the system, which results in larger memory requirements for Kueue.
Additionally, Kueue will take longer to schedule the workloads as it needs to
take the topology information into account.
