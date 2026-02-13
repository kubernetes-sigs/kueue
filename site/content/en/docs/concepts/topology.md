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
- **Immutability**: The field cannot be changed after creation
- **Uniqueness**: Each level must have a unique `nodeLabel`
- **Hostname restriction**: The `kubernetes.io/hostname` label can only be used
  at the lowest (last) level

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
- **Spec becomes immutable**: Once a ResourceFlavor has a `topologyName` set,
  the entire `.spec` field cannot be modified.

## What's next?

- Learn how to use [Topology Aware Scheduling](/docs/concepts/topology_aware_scheduling)
- Read the [API reference](/docs/reference/kueue.v1beta2/#kueue-x-k8s-io-v1beta2-Topology) for `Topology`
