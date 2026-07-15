---
title: "Setup Topology-Aware Scheduling"
date: 2026-07-03
weight: 6
description: >
  A hands-on guide to configuring Topology-Aware Scheduling and observing how it places workloads.
---

This page shows how to set up
[Topology-Aware Scheduling (TAS)](/docs/concepts/topology_aware_scheduling) as
a cluster administrator, and how to observe the decisions Kueue makes. We use
a local [Kind](https://kind.sigs.k8s.io/) cluster so that you can follow the
steps on your machine, but the same steps apply to any cluster whose nodes
carry topology labels.

The intended audience for this page are [batch administrators](/docs/tasks#batch-administrator).
If you are a batch user looking to run workloads on an already configured
cluster, see [Run Workloads with Topology-Aware Scheduling](/docs/tasks/run/topology_aware_scheduling/).

## Before you begin

Make sure the following conditions are met:

- The kubectl command-line tool is installed.
- The jq command-line JSON processor is installed.
- [Kind](https://kind.sigs.k8s.io/) is installed.
- The `TopologyAwareScheduling` feature gate is enabled (beta, on by default).

## Step 1: A cluster with topology labels

TAS relies on node labels that describe the placement of each node in the
physical hierarchy of your data center, for example its block, rack, and
hostname. On cloud providers these labels are typically set by the provider
(for details and cloud-specific examples, see [Node topology information](/docs/concepts/topology_aware_scheduling/#node-topology-information) and [Ecosystem Resources](/docs/ecosystem/)); on-premise,
you have to label the nodes yourself.

For this guide, create a Kind cluster with 8 worker "nodes" spread over 2
blocks and 4 racks, simulated with labels:

{{< include "examples/tas/kind-cluster.yaml" "yaml" >}}

```bash
kind create cluster --config kind-cluster.yaml
```

Then [install Kueue](/docs/installation).

Verify the topology labels on the nodes:

```bash
kubectl get nodes -L cloud.provider.com/topology-block,cloud.provider.com/topology-rack
```

## Step 2: Create the Topology, ResourceFlavor and queues

The `Topology` object defines the hierarchy of the labels, from the widest
(block) to the narrowest (hostname). The `ResourceFlavor` references it via
`topologyName`, and selects the TAS nodes via `nodeLabels`:

{{< include "examples/tas/sample-queues.yaml" "yaml" >}}

```bash
kubectl apply -f https://kueue.sigs.k8s.io/examples/tas/sample-queues.yaml
```

{{% alert title="Note" color="primary" %}}
In `sample-queues.yaml`, the `nominalQuota` is intentionally set high (100 CPUs) so that ClusterQueue quota is not a limiting factor. This guarantees that scheduling rejections during topology experiments are driven strictly by Kueue's Topology-Aware Scheduling (TAS) placement constraints rather than quota exhaustion.
{{% /alert %}}

## Step 3: Run a workload with a topology constraint

Submit a Job that requests all its Pods to run within a single rack, using
the `kueue.x-k8s.io/podset-required-topology` annotation on the Pod template:

{{< include "examples/tas/sample-job-required.yaml" "yaml" >}}

```bash
kubectl create -f https://kueue.sigs.k8s.io/examples/tas/sample-job-required.yaml
```

## Step 4: Observe the placement decision

Kueue records the chosen topology domains in the Workload's admission status.
Inspect it:

```bash
kubectl get workloads
WORKLOAD=$(kubectl get workload -o name --sort-by='.metadata.creationTimestamp' | tail -n 1)
kubectl get $WORKLOAD -o jsonpath='{.status.admission.podSetAssignments[0].topologyAssignment}' | jq
```

The output enumerates the exact nodes assigned to the Pods, with a Pod count
per node, for example:

```json
{
  "levels": [
    "kubernetes.io/hostname"
  ],
  "slices": [
    {
      "domainCount": 2,
      "podCounts": {
        "individual": [
          7,
          3
        ]
      },
      "valuesPerLevel": [
        {
          "individual": {
            "prefix": "kind-worker",
            "roots": [
              "",
              "2"
            ]
          }
        }
      ]
    }
  ]
}
```

{{% alert title="Note" color="primary" %}}
Kueue uses prefix compression in `valuesPerLevel` status output. In the example above, `prefix: "kind-worker"` paired with `roots: ["", "2"]` represents the node names `kind-worker` (`kind-worker` + `""`) and `kind-worker2` (`kind-worker` + `"2"`). The exact pod distribution in this example assumes worker nodes with 8 allocatable CPUs each; if your local environment allocates a different CPU capacity per node, Kueue will adjust the pod counts across nodes while strictly enforcing the single-rack topology constraint.
{{% /alert %}}

Confirm that the Pods landed on the assigned nodes, and that those nodes are
in the same rack:

```bash
kubectl get pods -o wide
kubectl get nodes -L cloud.provider.com/topology-block,cloud.provider.com/topology-rack
```

The Pods of a TAS workload are created with the `kueue.x-k8s.io/topology`
[scheduling gate](https://kubernetes.io/docs/concepts/scheduling-eviction/pod-scheduling-readiness/),
and stay gated until the assignment is computed. Kueue then enforces the
assignment by injecting a node selector matching the assigned topology
domains into each Pod, and removes the gate.

## Step 5: Experiment with the constraint types

To build intuition, try the other annotation variants in sequence and observe how Kueue handles them:

### Preferred topology: multi-domain fallback

The `kueue.x-k8s.io/podset-preferred-topology` annotation tells Kueue that a topology level is preferred, but allows falling back to wider levels or distributing Pods across multiple topology domains if a single domain is full:

{{< include "examples/tas/sample-job-preferred.yaml" "yaml" >}}

Submit the preferred Job while the initial required Job is running:

```bash
kubectl create -f https://kueue.sigs.k8s.io/examples/tas/sample-job-preferred.yaml
```

Observe that Kueue admits the Job and distributes its 40 Pods across the remaining available racks and blocks, consuming the remaining topology capacity across the cluster.

### Required topology: admission control by topology placement (under sufficient quota)

Submit an additional required Job that requires all Pods to fit within a single rack:

```bash
kubectl create -f https://kueue.sigs.k8s.io/examples/tas/sample-job-required.yaml
```

Because existing workloads occupy capacity across the racks, no single rack has 10 CPUs available to fit all Pods together. Even though ClusterQueue quota is sufficient (50 CPUs used out of 100 CPUs configured), Kueue's TAS engine controls resource admission directly by holding the Job in `Pending` state.

Query the admission failure condition message:

```bash
PENDING_WORKLOAD=$(kubectl get workload -o name --sort-by='.metadata.creationTimestamp' | tail -n 1)
kubectl get $PENDING_WORKLOAD -o jsonpath='{.status.conditions[?(@.type=="QuotaReserved")].message}'
```

The output is similar to:

```text
couldn't assign flavors to pod set main: topology "default" allows to fit only 4 out of 10 pod(s). Total nodes: 8; excluded: resource "cpu": 6
```

This message proves that TAS actively enforces topology domain placement rules during admission, holding the workload in queue even when ample cluster quota remains.

### Unconstrained topology: fill scattered gaps & consume remaining capacity

The `kueue.x-k8s.io/podset-unconstrained-topology` annotation tells Kueue to schedule Pods on any available nodes without topology domain boundaries, while still using TAS bookkeeping to pack Pods into existing partially-filled nodes:

{{< include "examples/tas/sample-job-unconstrained.yaml" "yaml" >}}

Submit the unconstrained Job to fill the remaining node gaps:

```bash
kubectl create -f https://kueue.sigs.k8s.io/examples/tas/sample-job-unconstrained.yaml
```

Observe that Kueue admits the Job, gathering the remaining free CPU slots across scattered nodes in the cluster to consume the remaining cluster capacity:

```bash
WORKLOAD=$(kubectl get workload -o name --sort-by='.metadata.creationTimestamp' | tail -n 1)
kubectl get $WORKLOAD -o jsonpath='{.status.admission.podSetAssignments[0].topologyAssignment}' | jq
```

The output confirms that Pods were packed into the remaining non-full nodes across multiple domains, for example:

```json
{
  "levels": [
    "kubernetes.io/hostname"
  ],
  "slices": [
    {
      "domainCount": 2,
      "podCounts": {
        "individual": [
          4,
          2
        ]
      },
      "valuesPerLevel": [
        {
          "individual": {
            "prefix": "kind-worker",
            "roots": [
              "2",
              "8"
            ]
          }
        }
      ]
    }
  ]
}
```

## Cleanup

```bash
kind delete cluster
```

## What's next

- Learn about all [PodSet annotations](/docs/tasks/run/topology_aware_scheduling/) available to batch users.
- Explore advanced features like Node Hotswap and failure recovery in [Topology-Aware Scheduling Concepts](/docs/concepts/topology_aware_scheduling).
- Combine TAS with [MultiKueue](/docs/tasks/manage/setup_multikueue/#setup-with-topology-aware-scheduling-tas).
- Read about [TAS drawbacks](/docs/concepts/topology_aware_scheduling/#drawbacks).
