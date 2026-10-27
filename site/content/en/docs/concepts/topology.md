---
title: "Topology"
date: 2026-01-29
weight: 6
description: >
  A cluster-scoped resource that represents the hierarchical topology of nodes in a data center.
---

A `Topology` is a cluster-scoped object that defines the hierarchical structure
of nodes in a data center. It enables
[Topology Aware Scheduling](/docs/concepts/topology_aware_scheduling) by
providing a model for representing the hierarchy of organizational units
(such as zones, blocks, and racks) using node labels.

The `Topology` object is referenced from a [ResourceFlavor](/docs/concepts/resource_flavor)
via the `.spec.topologyName` field to associate the flavor with a specific
topology structure.

A `Topology` definition looks like the following:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: Topology
metadata:
  name: "default"
spec:
  levels:
  - nodeLabel: "topology.kubernetes.io/zone"
  - nodeLabel: "cloud.provider.com/topology-block"
  - nodeLabel: "cloud.provider.com/topology-rack"
  - nodeLabel: "kubernetes.io/hostname"
```

## Topology levels

The `.spec.levels` field defines the hierarchy of topology levels, ordered from
the highest (coarsest) level to the lowest (finest) level. Each level is
identified by a node label that nodes in your cluster must have.

For example, in a typical data center:
- **Zone level**: Regions or zones, identified by a label like
  `topology.kubernetes.io/zone`
- **Block level**: Groups of racks, identified by a label like
  `cloud.provider.com/topology-block`
- **Rack level**: Individual racks within blocks, identified by a label like
  `cloud.provider.com/topology-rack`
- **Node level**: Individual nodes, typically identified by `kubernetes.io/hostname`

Pods running within the same topology domain (for example, the same rack) have
better network bandwidth than Pods on different domains.

### Validation rules

The `levels` field has the following constraints:

- **Minimum items**: 1
- **Maximum items**: 16
- **Mutability**: The field can only be changed when `kubernetes.io/hostname`
  is the lowest (last) level both before and after the change; otherwise it is
  immutable
- **Uniqueness**: Each level must have a unique `nodeLabel`
- **Hostname restriction**: The `kubernetes.io/hostname` label can only be used
  at the lowest (last) level

### Changing the levels of a Topology in use

When `kubernetes.io/hostname` is the lowest level, levels above it can be
renamed, reordered, added or removed while workloads are admitted: their
recorded usage is keyed by hostname and is preserved across the change, and
already admitted workloads are not evicted. Pending workloads are automatically
retried against the new levels. Keep in mind:

- **When adding a level, label all nodes first, then add the level.** The
  scheduler only considers nodes that carry the labels of every topology level,
  so nodes missing the new label are excluded (together with their usage) until
  they are labeled, which temporarily shrinks the available capacity.
- **Removing a level makes it unschedulable for workloads that request it.**
  Workloads with a required or preferred topology request for a removed level
  can no longer be nominated (or re-nominated, for example during failed node
  replacement) until the level is restored. Already admitted workloads keep
  running; admission-time constraints are not re-validated retroactively.

## Referencing a Topology from a ResourceFlavor

To enable Topology Aware Scheduling, reference a `Topology` from a
`ResourceFlavor` using the `.spec.topologyName` field:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ResourceFlavor
metadata:
  name: "tas-flavor"
spec:
  nodeLabels:
    cloud.provider.com/node-group: "tas-group"
  topologyName: "default"
```

When a ResourceFlavor references a Topology:

- **At least one nodeLabel is required**: The ResourceFlavor must have at least
  one entry in `.spec.nodeLabels`.
- **Restricted mutability**: Once a ResourceFlavor has a `topologyName` set,
  the `.spec.topologyName` field is immutable (it cannot be changed or unset in
  place). Since Kueue v0.20, `.spec.nodeLabels` (as long as at least one label
  remains), `.spec.nodeTaints`, and `.spec.tolerations` can still be updated in
  place; in Kueue v0.18 and v0.19, `.spec.nodeLabels` and `.spec.tolerations`
  are also immutable once `.spec.topologyName` is set.

When the `TopologyAwareScheduling` feature gate is enabled, Kueue retains the
`kueue.x-k8s.io/resource-in-use` finalizer on any `Topology` object referenced
by a `ResourceFlavor` to prevent deletion, and marks any `ClusterQueue`
referencing a flavor whose `Topology` does not exist as inactive
(`TopologyNotFound`). Create the `Topology` object before referencing it from a
`ResourceFlavor`.

Because `.spec.topologyName` cannot be unset in place, disabling TAS on an
existing flavor without performing a blue/green `ResourceFlavor` replacement
requires setting `TopologyAwareScheduling: false` and setting every enabled
feature gate that depends on it (mostly the `TAS*` gates) to `false` (see
[Topology-Aware Scheduling](/docs/concepts/topology_aware_scheduling)). Jobs
already admitted through TAS before the gate is disabled retain the
`kueue.x-k8s.io/topology` scheduling gate in their PodTemplate; any Pods created
or recreated for those Jobs while TAS is disabled remain `SchedulingGated` until
the Workload is evicted (for example, via `waitForPodsReady.recoveryTimeout`)
and re-admitted.

## What's next?

- Learn how to use [Topology Aware Scheduling](/docs/concepts/topology_aware_scheduling)
- Read the [API reference](/docs/reference/kueue.v1beta2/#kueue-x-k8s-io-v1beta2-Topology) for `Topology`
