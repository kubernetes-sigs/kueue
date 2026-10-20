---
title: "Run Workloads with Topology-Aware Scheduling"
linkTitle: "Topology-Aware Scheduling"
date: 2026-05-17
weight: 8
description: >
  Run workloads that are placed across nodes using Topology-Aware Scheduling
  for reduced network latency and improved throughput.
---

This page shows you how to run workloads that use
[Topology-Aware Scheduling (TAS)](/docs/concepts/topology_aware_scheduling)
in a Kubernetes cluster with Kueue enabled. The examples use a batch Job, but
the same annotations work with any
[workload type that Kueue supports](/docs/concepts/workload).

This page is intended for [batch users](/docs/tasks#batch-user). If you are a [batch administrator](/docs/tasks#batch-administrator) looking to configure cluster topology and queues, see [Setup Topology-Aware Scheduling](/docs/tasks/manage/setup_topology_aware_scheduling/).

For conceptual details about how Kueue models cluster topology and how the
TAS scheduling algorithm works, see
[Topology-Aware Scheduling concepts](/docs/concepts/topology_aware_scheduling).

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster is running.
- The kubectl command-line tool has communication with your cluster.
- [Kueue is installed](/docs/installation).
- The `TopologyAwareScheduling` feature gate is enabled (beta, on by default
  since Kueue v0.14).
- Your administrator has
  [configured TAS](/docs/tasks/manage/setup_topology_aware_scheduling/)
  by creating a `Topology` object, a `ResourceFlavor` that references it via
  `spec.topologyName`, and a `ClusterQueue` that uses that flavor.

If you do not already have a running cluster with TAS enabled, see [Setup Topology-Aware Scheduling](/docs/tasks/manage/setup_topology_aware_scheduling/) for a step-by-step administrator guide on setting up a local `kind` cluster and configuring TAS queues.

## 1. Identify the queues available in your namespace

Run the following command to list the `LocalQueues` available in your namespace.

```shell
kubectl -n default get localqueues
```

The output is similar to the following:

```
NAME             CLUSTERQUEUE        PENDING WORKLOADS
tas-user-queue   tas-cluster-queue   0
```

The [ClusterQueue](/docs/concepts/cluster_queue) defines the quotas for the
Queue, and its `ResourceFlavor` is what binds the queue to a `Topology`.

## 2. Choose a topology scheduling mode

TAS exposes three Pod-template annotations, each expressing a different
placement intent for the PodSet. Pick the one that matches your workload:

- Use **required** when pods must communicate tightly and any cross-domain
  placement would degrade performance unacceptably.
- Use **preferred** when same-domain placement is desirable but the workload
  can still make progress when distributed.
- Use **unconstrained** when you do not care about topology but you want
  Kueue's TAS bookkeeping to fill small gaps on existing nodes and reduce
  fragmentation.

You set exactly one of these annotations on the Pod template's
`metadata.annotations` (not on the Job's top-level metadata).

### Required: same-domain placement

The `kueue.x-k8s.io/podset-required-topology` annotation requires Kueue to
schedule all pods of the PodSet within a single topology domain at the
indicated level. If no single domain has enough capacity, the workload waits.

```yaml
metadata:
  annotations:
    kueue.x-k8s.io/podset-required-topology: "cloud.provider.com/topology-rack"
```

A full example:

{{< include "examples/tas/sample-job-required.yaml" "yaml" >}}

### Preferred: best-effort same-domain placement

The `kueue.x-k8s.io/podset-preferred-topology` annotation asks Kueue to fit
the PodSet within a single topology domain at the indicated level. If that
fails, Kueue evaluates levels above the indicated one, one by one. If the
PodSet does not fit even at the highest level, it is admitted distributed
across multiple domains.

```yaml
metadata:
  annotations:
    kueue.x-k8s.io/podset-preferred-topology: "cloud.provider.com/topology-block"
```

A full example:

{{< include "examples/tas/sample-job-preferred.yaml" "yaml" >}}

### Unconstrained: minimize fragmentation

The `kueue.x-k8s.io/podset-unconstrained-topology` annotation tells Kueue to
schedule pods on any nodes without topology considerations. Kueue still
tracks placement so it can pack pods into existing partially-used nodes,
which helps minimize fragmentation across the cluster.

The annotation value is the literal string `"true"`, not a topology label:

```yaml
metadata:
  annotations:
    kueue.x-k8s.io/podset-unconstrained-topology: "true"
```

A full example:

{{< include "examples/tas/sample-job-unconstrained.yaml" "yaml" >}}

## 3. Run the workload

Submit any of the examples above using `kubectl create`:

```shell
kubectl create -f https://kueue.sigs.k8s.io/examples/tas/sample-job-required.yaml
```

```shell
kubectl create -f https://kueue.sigs.k8s.io/examples/tas/sample-job-preferred.yaml
```

```shell
kubectl create -f https://kueue.sigs.k8s.io/examples/tas/sample-job-unconstrained.yaml
```

Internally, Kueue creates a corresponding [Workload](/docs/concepts/workload)
for the Job and runs the TAS scheduling algorithm against the matching
`ResourceFlavor`'s `Topology`.

## 4. (Optional) Monitor the topology assignment

List Workloads in the namespace:

```shell
kubectl -n default get workloads.kueue.x-k8s.io
```

Check whether the Workload was admitted:

```shell
kubectl -n default describe workload <workload-name>
```

Look for the `Admitted` condition in the `Conditions` section and any
scheduling events in `Events`.

To see the concrete topology placement Kueue chose, inspect the
`status.admission.podSetAssignments[].topologyAssignment` field:

```shell
kubectl -n default get workloads.kueue.x-k8s.io <workload-name> -o yaml
```

That field lists the topology levels and domain values into which each PodSet
was placed. It is the authoritative signal that TAS was applied.

## Co-locating multiple PodSets (PodSet Grouping)

Some distributed workloads, such as [LeaderWorkerSet](/docs/tasks/run/leaderworkerset), define their computing requirements across multiple, distinct Pod templates (e.g., one template for the Leader, and another for the Workers). Kueue translates these templates into separate `PodSets` within a single `Workload`.

By default, Kueue evaluates TAS constraints for each `PodSet` independently, which could result in the Leader and Workers being scheduled into completely different topology domains, drastically increasing network latency.

To prevent this, you can use the `kueue.x-k8s.io/podset-group-name` annotation in conjunction with a required or preferred topology annotation. By assigning the exact same group name to multiple Pod templates, you instruct Kueue to treat them as a single atomic unit and co-locate them in the same topology domain (strictly enforced for required placement, and best-effort for preferred placement).

### Example: LeaderWorkerSet Rack-Level Co-location

The following example requires Kueue to schedule both the Leader PodSet and the Worker PodSet into the exact same rack by setting both the `kueue.x-k8s.io/podset-required-topology` and `kueue.x-k8s.io/podset-group-name: "lws-group"` annotations on both templates.

{{< include "examples/serving-workloads/sample-leaderworkerset-tas.yaml" "yaml" >}}

Submit the LeaderWorkerSet using `kubectl create`:

```shell
kubectl create -f https://kueue.sigs.k8s.io/examples/serving-workloads/sample-leaderworkerset-tas.yaml
```

#### Verify rack co-location

After the LeaderWorkerSet is admitted, list all Pods and the node they were assigned to, along with the rack label of that node:

```shell
kubectl get pods -l leaderworkerset.sigs.k8s.io/name=nginx-leaderworkerset \
  -o custom-columns='NAME:.metadata.name,NODE:.spec.nodeName,GROUP:.metadata.labels.leaderworkerset\.sigs\.k8s\.io/group-index,WORKER:.metadata.labels.leaderworkerset\.sigs\.k8s\.io/worker-index'
```

The output is similar to the following:

```text
NAME                             NODE           GROUP   WORKER
nginx-leaderworkerset-0          kind-worker    0       0
nginx-leaderworkerset-0-1        kind-worker2   0       1
nginx-leaderworkerset-0-2        kind-worker3   0       2
nginx-leaderworkerset-1          kind-worker5   1       0
nginx-leaderworkerset-1-1        kind-worker6   1       1
nginx-leaderworkerset-1-2        kind-worker7   1       2
```

To confirm the topology of the assigned nodes, list their labels:

```shell
kubectl get nodes -l cloud.provider.com/node-group=tas-group \
  -L cloud.provider.com/topology-block,cloud.provider.com/topology-rack
```

The output is similar to the following:

```text
NAME           STATUS   ROLES    AGE   VERSION   TOPOLOGY-BLOCK   TOPOLOGY-RACK
kind-worker    Ready    <none>   5m    v1.31.0   b1               r1
kind-worker2   Ready    <none>   5m    v1.31.0   b1               r1
kind-worker3   Ready    <none>   5m    v1.31.0   b1               r1
kind-worker4   Ready    <none>   5m    v1.31.0   b1               r2
kind-worker5   Ready    <none>   5m    v1.31.0   b2               r1
kind-worker6   Ready    <none>   5m    v1.31.0   b2               r1
kind-worker7   Ready    <none>   5m    v1.31.0   b2               r1
kind-worker8   Ready    <none>   5m    v1.31.0   b2               r2
```

Observe that all Pods belonging to group `0` (the Leader at worker index `0` and its Workers at indices `1` and `2`) are placed on nodes within the same rack, and all Pods for group `1` are placed on nodes within a different rack. The Leader and Workers within each group are never split across racks.

To see the authoritative topology placement chosen by Kueue, inspect the `topologyAssignment` field on the corresponding `Workload`:

```shell
kubectl get workloads.kueue.x-k8s.io -o yaml | grep -A 10 topologyAssignment
```

## Advanced topics

The page above covers the three basic TAS annotations. Kueue also supports
additional placement behavior:

- **Placement strategies** - TAS uses greedy packing strategies to choose
  domains. The default strategy is `BestFit`. When the beta `TASProfileMixed`
  feature gate is enabled, which is the default since Kueue v0.15, TAS uses
  `LeastFreeCapacity` for unconstrained placement and `BestFit` for required
  and preferred placement.
- **Balanced placement** - the alpha `TASBalancedPlacement` feature gate makes
  preferred placement distribute pods or slices more evenly across the selected
  domains. This is useful for workloads with all-to-all communication patterns
  where a placement such as 6 pods in one rack and 6 pods in another rack is
  better than 10 pods in one rack and 2 pods in another. See
  [Balanced Placement](/docs/concepts/topology_aware_scheduling#balanced-placement).
- **PodSet groups** - co-locate multiple PodSets of a single workload in the
  same topology domain using `kueue.x-k8s.io/podset-group-name`. See
  [Co-locating multiple PodSets](#co-locating-multiple-podsets-podset-grouping)
  for a working example.
- **PodSet slices** - split a PodSet into fixed-size slices, each pinned to a
  single domain, using `kueue.x-k8s.io/podset-slice-required-topology` and
  `kueue.x-k8s.io/podset-slice-size`. See the
  [Topology-Aware Scheduling concepts](/docs/concepts/topology_aware_scheduling)
  page and the [JobSet TAS examples](/docs/tasks/run/jobsets#d-topology-aware-scheduling).
- **Multi-layer topology** - express slice constraints at up to three
  topology layers in one annotation using
  `kueue.x-k8s.io/podset-slice-required-topology-constraints`. This is
  controlled by the alpha `TASMultiLayerTopology` feature gate; see the
  [Multi-Layer Topology](/docs/concepts/topology_aware_scheduling#multi-layer-topology)
  section of the concepts page.

## Troubleshooting

### Workload not admitted

If the Workload stays in `Pending` state:

- Run `kubectl -n default describe workload <workload-name>` and look at the
  `Events` section for admission rejection reasons.
- Verify that the `ResourceFlavor` selected for the Workload references the
  expected `Topology` in `spec.topologyName`.
- Verify that the referenced `Topology` exists, and that every
  `spec.levels[].nodeLabel` value exists as a label key on the cluster's nodes.
- Verify that the topology label in your same-domain placement annotation, such
  as `kueue.x-k8s.io/podset-required-topology` or
  `kueue.x-k8s.io/podset-preferred-topology`, matches one of the referenced
  `Topology` object's `spec.levels[].nodeLabel` values.
- For `required` placement, verify at least one domain at the requested level
  has enough free capacity to fit the entire PodSet.

You can inspect node labels with:

```shell
kubectl get nodes --show-labels
```

### Annotation has no effect

If the topology annotation appears to be ignored entirely (pods are admitted
as if the annotation were absent), the most common cause is that the
`ResourceFlavor` selected for the workload does not have `spec.topologyName`
set. Without it, Kueue does not run TAS for that flavor.

For general troubleshooting, see the
[troubleshooting guide](/docs/tasks/troubleshooting).
