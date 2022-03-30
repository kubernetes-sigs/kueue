# Administer Cluster resources

This page shows you how to manage your cluster resource quotas and to establish
fair sharing rules among the tenants.

The intended audience for this page are [batch administrators](/docs/tasks#batch-administrator).

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster is running.
- The kubectl command-line tool has communication with your cluster.
- [Kueue is installed](/README.md#installation).

## Single ClusterQueue, single flavor setup {#single-queue-single-flavor}

In the following steps, you will create a queuing system with a single
ClusterQueue to govern the quota of your cluster.

You can perform all these steps at once by applying [config/samples/single-clusterqueue-setup.yaml](/config/samples/single-clusterqueue-setup.yaml):

```shell
kubectl apply -f config/samples/single-clusterqueue-setup.yaml
```

### 1. Create a [ClusterQueue](/docs/concepts/cluster_queue.md)

Create a single ClusterQueue to represent the resource quotas for your entire
cluster.

```shell
kubectl apply -f cluster-total.yaml
```

```yaml
# cluster-total.yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: ClusterQueue
metadata:
  name: cluster-total
spec:
  namespaceSelector: {} # match all.
  requestableResources:
  - name: "cpu"
    flavors:
    - resourceFlavor: default
      quota:
        guaranteed: 9
  - name: "memory"
    flavors:
    - resourceFlavor: default
      quota:
        guaranteed: 36Gi
```

This ClusterQueue governs the usage of [resource types](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/#resource-types)
`cpu` and `memory`. Each resource type has a single [resource flavor](/docs/concepts/cluster_queue.md#resource-flavors),
named `default` with a guaranteed quota.

The empty `namespaceSelector` allows any namespace to use these resources.

### 2. Create a [ResourceFlavor](/docs/concepts/cluster_queue.md#resource-flavors)

The ClusterQueue is not ready to be used yet, as the `default` flavor is not
defined.

Typically, a resource flavor has node labels and/or taints to scope which nodes
can provide it. However, since we are using a single flavor to represent all the
resources available in the cluster, you can create an empty ResourceFlavor.

```shell
kubectl apply -f default-flavor.yaml
```

```yaml
# default-flavor.yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: ResourceFlavor
metadata:
  name: default
```

The `.metadata.name` matches the `.spec.requestableResources[*].flavors[0].resourceFlavor`
field in the ClusterQueue.

### 3. Create [Queues](/docs/concepts/queue.md)

Users cannot directly send [workloads](/docs/concepts/queued_workload.md) to
ClusterQueues. Instead, users need to send their workloads to a Queue in their
namespace.
Thus, for the queuing system to be complete, you need to create a Queue in
each namespace that needs access to the ClusterQueue.

```shell
kubectl apply -f default-user-queue.yaml
```

```yaml
# default-user-queue.yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: Queue
metadata:
  namespace: default
  name: user-queue
spec:
  clusterQueue: cluster-total
```

## Multiple ResourceFlavors setup

You can define quotas for different [resource flavors](/docs/concepts/cluster_queue.md#resource-flavors).

For the rest of this section, assume that your cluster has nodes with two CPU
architectures, namely `x86` and `arm`, specified in the node label `cpu-arch`

### 1. Create ResourceFlavors

To create the ResourceFlavors, run the following command:

```shell
kubectl apply -f flavor-x86.yaml flavor-arm.yaml
```

```yaml
# flavor-x86.yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: ResourceFlavor
metadata:
  name: x86
labels:
  cpu-arch: x86
```

```yaml
# flavor-arm.yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: ResourceFlavor
metadata:
  name: arm
labels:
  cpu-arch: arm
```

### 2. Create a ClusterQueue referencing the flavors

To create the ResourceFlavors, run the following command:

```shell
kubectl apply -f cluster-total.yaml
```

```yaml
# cluster-total.yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: ClusterQueue
metadata:
  name: cluster-total
spec:
  namespaceSelector: {}
  requestableResources:
  - name: "cpu"
    flavors:
    - resourceFlavor: x86
      quota:
        guaranteed: 9
    - resourceFlavor: arm
      quota:
        guaranteed: 12
  - name: "memory"
    flavors:
    - resourceFlavor: default
      quota:
        guaranteed: 84Gi
```

The flavor names in the fields `.spec.requestableResources[*].flavors[*].resourceFlavor`
should match the names of the ResourceFlavors created earlier.

Note that `memory` is referencing the `default` flavor created in the [single flavor setup](#single-queue-single-flavor).
This means that you don't want to distinguish if the memory is given from `x86`
or `arm` nodes.

**Warning**

Using the same flavors in multiple resources is [not supported](https://github.com/kubernetes-sigs/kueue/issues/167).

## What's next?

- Learn how to [run jobs](run_jobs.md).
