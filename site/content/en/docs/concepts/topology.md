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
- **Restricted mutability**:
  - **Kueue v0.21 and later**: `.spec.topologyName` can be unset in place (and
    set again later), while changing it directly from one `Topology` to another
    remains disallowed. To switch a `ResourceFlavor` to a different `Topology`,
    unset `.spec.topologyName` first and then set the new name; if the new
    `Topology` defines different levels, wait for workloads admitted under the
    earlier `Topology` to finish first so existing `TopologyAssignment` domains
    remain compatible.
  - **Kueue v0.20 and later**: `.spec.nodeLabels` (as long as at least one
    label remains), `.spec.nodeTaints`, and `.spec.tolerations` can be updated
    in place.
  - **Kueue v0.18 and v0.19**: `.spec.nodeLabels`, `.spec.tolerations`, and
    `.spec.topologyName` are immutable once `.spec.topologyName` is set.

While `TopologyAwareScheduling` is enabled, Kueue adds the
`kueue.x-k8s.io/resource-in-use` finalizer to each `Topology` and removes it
only once no `ResourceFlavor` references that `Topology`. The TAS controllers
that manage this finalizer do not run while `TopologyAwareScheduling` is
disabled, so a `Topology` that already has the finalizer stays in `Terminating`
if it is deleted while TAS is disabled, until TAS is re-enabled. When
`TopologyAwareScheduling` is enabled, Kueue also marks any `ClusterQueue` that
uses a flavor whose `Topology` does not exist as inactive (`TopologyNotFound`),
so create the `Topology` before referencing it from a `ResourceFlavor`.

### Disabling TAS on an existing ResourceFlavor

Starting in Kueue v0.21, you can disable TAS on an existing `ResourceFlavor` in
place by unsetting `.spec.topologyName` (as an alternative to blue/green
`ResourceFlavor` replacement):

- Admitted workloads keep running and are not evicted by unsetting
  `.spec.topologyName`.
- New unannotated workloads admitted on that flavor receive no
  `TopologyAssignment`. In a multi-flavor `ClusterQueue`, unsetting
  `.spec.topologyName` on one flavor turns the `ClusterQueue` into a
  [mixed-flavor `ClusterQueue`](/docs/concepts/topology_aware_scheduling/#implicit-tas-and-mixed-flavor-clusterqueues),
  which disables implicit TAS on any remaining TAS flavors. New workloads with
  explicit TAS annotations cannot use the flavor without `.spec.topologyName`
  and stay pending (if no other TAS flavor fits) with
  `Flavor "<name>" does not support TopologyAwareScheduling`.
- For workloads admitted earlier through TAS, recreated Pods continue to be
  ungated by the TAS topology ungater onto the node recorded in
  `status.admission`; a non-TAS Pod admitted in the meantime may already occupy
  capacity on that node. If an assigned node fails while `.spec.topologyName` is
  unset (`TASFailedNodeReplacement`), Kueue cannot compute a replacement domain:
  - An explicit-TAS workload is evicted immediately when
    `TASFailedNodeReplacementFailFast` is enabled, or retried in the
    second-pass queue otherwise.
  - For an implicit-TAS workload, the second pass keeps the existing
    `TopologyAssignment` referencing the failed node and clears
    `status.unhealthyNodes`. Any recreated Pods are therefore ungated onto that
    failed node and cannot run until the workload is recovered (for example,
    via `waitForPodsReady`).
- Setting `.spec.topologyName` again restores TAS domain reservations for
  still-running workloads that retain a `TopologyAssignment` (for compatible
  topology levels). Workloads admitted while `.spec.topologyName` was unset have
  no `TopologyAssignment`: their bound running Pods are accounted on their nodes
  as non-TAS usage, while unbound Pods consume only `ClusterQueue` quota.

When disabling TAS cluster-wide via feature gates instead (by setting
`TopologyAwareScheduling: false` and disabling dependent `TAS*` feature gates,
see [Topology-Aware Scheduling](/docs/concepts/topology_aware_scheduling)), the
TAS topology ungater stops running. Jobs already admitted through TAS retain
the `kueue.x-k8s.io/topology` scheduling gate in their PodTemplate, so Pods
created or recreated while the gate is off remain `SchedulingGated` until the
Workload is evicted (for example, via `waitForPodsReady.recoveryTimeout`) and
re-admitted.

## What's next?

- Learn how to use [Topology Aware Scheduling](/docs/concepts/topology_aware_scheduling)
- Read the [API reference](/docs/reference/kueue.v1beta2/#kueue-x-k8s-io-v1beta2-Topology) for `Topology`
