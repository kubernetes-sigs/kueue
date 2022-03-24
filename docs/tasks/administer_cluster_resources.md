# Administer Cluster resources

This page shows you how to represent your cluster resource quotas and to
establish fair sharing rules among the tenants.

The intended audience for this page are cluster administrators.

## Before you begin

You need to have a Kubernetes cluster, the kubectl command-line tool
must be configured to communicate with your cluster, and [Kueue installed](/README.md#installation).

## Single ClusterQueue setup

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
        ceiling: 9
  - name: "memory"
    flavors:
    - resourceFlavor: default
      quota:
        guaranteed: 36Gi
        ceiling: 36Gi
```

This ClusterQueue governs the usage of [resource types](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/#resource-types)
`cpu` and `memory`. Each resource type has a single [resource flavor](/docs/concepts/cluster_queue.md#resource-flavors),
named `default` with a guaranteed quota.
As there is a single ClusterQueue in the cluster, it is not possible for the
ClusterQueue to borrow resources from anywhere. Thus, the `ceiling` is equal
to the `guaranteed` quota.

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