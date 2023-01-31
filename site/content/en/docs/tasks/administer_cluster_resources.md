---
title: "Administer Cluster Quotas"
date: 2017-01-05
weight: 4
description: >
  Manage your cluster resource quotas and to establish fair sharing rules among the tenants.
---

This page shows you how to manage your cluster resource quotas and to establish
fair sharing rules among the tenants.

The intended audience for this page are [batch administrators](/docs/tasks#batch-administrator).

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster is running.
- The kubectl command-line tool has communication with your cluster.
- [Kueue is installed](/docs/setup/install.md).

## Single ClusterQueue and single ResourceFlavor setup

In the following steps, you will create a queuing system with a single
ClusterQueue and a single [ResourceFlavor](/docs/concepts/cluster_queue#resourceflavor-object)
to govern the quota of your cluster.

You can perform all these steps at once by applying [github.com/kubernetes-sigs/kueue/blob/main/config/samples/single-clusterqueue-setup.yaml](https://github.com/kubernetes-sigs/kueue/blob/main/config/samples/single-clusterqueue-setup.yaml):

```shell
kubectl apply -f config/samples/single-clusterqueue-setup.yaml
```

### 1. Create a [ClusterQueue](/docs/concepts/cluster_queue)

Create a single ClusterQueue to represent the resource quotas for your entire
cluster.

Write the manifest for the ClusterQueue. It should look similar to the following:

```yaml
# cluster-total.yaml
apiVersion: kueue.x-k8s.io/v1alpha2
kind: ClusterQueue
metadata:
  name: cluster-total
spec:
  namespaceSelector: {} # match all.
  resources:
  - name: "cpu"
    flavors:
    - name: default
      quota:
        min: 9
  - name: "memory"
    flavors:
    - name: default
      quota:
        min: 36Gi
```

To create the ClusterQueue, run the following command:

```shell
kubectl apply -f cluster-total.yaml
```

This ClusterQueue governs the usage of [resource types](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/#resource-types)
`cpu` and `memory`. Each resource type has a single [resource flavor](/docs/concepts/cluster_queue#resourceflavor-object),
named `default` with a min quota.

The empty `namespaceSelector` allows any namespace to use these resources.

### 2. Create a [ResourceFlavor](/docs/concepts/cluster_queue#resourceflavor-object)

The ClusterQueue is not ready to be used yet, as the `default` flavor is not
defined.

Typically, a resource flavor has node labels and/or taints to scope which nodes
can provide it. However, since we are using a single flavor to represent all the
resources available in the cluster, you can create an empty ResourceFlavor.

Write the manifest for the ResourceFlavor. It should look similar to the following:

```yaml
# default-flavor.yaml
apiVersion: kueue.x-k8s.io/v1alpha2
kind: ResourceFlavor
metadata:
  name: default
```

To create the ResourceFlavor, run the following command:

```shell
kubectl apply -f default-flavor.yaml
```

The `.metadata.name` matches the `.spec.resources[*].flavors[0].resourceFlavor`
field in the ClusterQueue.

### 3. Create [LocalQueues](/docs/concepts/local_queue)

Users cannot directly send [workloads](/docs/concepts/workload) to
ClusterQueues. Instead, users need to send their workloads to a Queue in their
namespace.
Thus, for the queuing system to be complete, you need to create a Queue in
each namespace that needs access to the ClusterQueue.

Write the manifest for the LocalQueue. It should look similar to the following:

```yaml
# default-user-queue.yaml
apiVersion: kueue.x-k8s.io/v1alpha2
kind: LocalQueue
metadata:
  namespace: default
  name: user-queue
spec:
  clusterQueue: cluster-total
```

To create the LocalQueue, run the following command:

```shell
kubectl apply -f default-user-queue.yaml
```

## Multiple ResourceFlavors setup

You can define quotas for different [resource flavors](/docs/concepts/cluster_queue#resourceflavor-object).

For the rest of this section, assume that your cluster has nodes with two CPU
architectures, namely `x86` and `arm`, specified in the node label `cpu-arch`.

### Limitations

- Using the same flavors in multiple `.resources` of a ClusterQueue
  is [not supported](https://github.com/kubernetes-sigs/kueue/issues/167).

### 1. Create ResourceFlavors

Write the manifests for the ResourceFlavors. They should look similar to the
following:

```yaml
# flavor-x86.yaml
apiVersion: kueue.x-k8s.io/v1alpha2
kind: ResourceFlavor
metadata:
  name: x86
nodeSelector:
  cpu-arch: x86
```

```yaml
# flavor-arm.yaml
apiVersion: kueue.x-k8s.io/v1alpha2
kind: ResourceFlavor
metadata:
  name: arm
nodeSelector:
  cpu-arch: arm
```

To create the ResourceFlavors, run the following command:

```shell
kubectl apply -f flavor-x86.yaml -f flavor-arm.yaml
```

The labels set in the ResourceFlavors should match the labels in your nodes.
If you are using [cluster autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler)
(or equivalent controllers), make sure it is configured to add those labels when
adding new nodes.

### 2. Create a ClusterQueue referencing the flavors

Write the manifest for the ClusterQueue that references the flavors. It should
look similar to the following:

```yaml
# cluster-total.yaml
apiVersion: kueue.x-k8s.io/v1alpha2
kind: ClusterQueue
metadata:
  name: cluster-total
spec:
  namespaceSelector: {}
  resources:
  - name: "cpu"
    flavors:
    - name: x86
      quota:
        min: 9
    - name: arm
      quota:
        min: 12
  - name: "memory"
    flavors:
    - name: default
      quota:
        min: 84Gi
```

The flavor names in the fields `.spec.resources[*].flavors[*].resourceFlavor`
should match the names of the ResourceFlavors created earlier.

Note that `memory` is referencing the `default` flavor created in the [single flavor setup.](#single-clusterqueue-and-single-resourceflavor-setup)
This means that you don't want to distinguish if the memory is given from `x86`
or `arm` nodes.

To create the ClusterQueue, run the following command:

```shell
kubectl apply -f cluster-total.yaml
```

## Multiple ClusterQueues and borrowing cohorts

Two or more ClusterQueues can borrow unused quota from other ClusterQueues in
the same [cohort](/docs/concepts/cluster_queue#cohort).

Using the following example, you can establish a cohort `team-ab` that includes
ClusterQueues `team-a-cq` and `team-b-cq`.

```yaml
# team-a-cq.yaml
apiVersion: kueue.x-k8s.io/v1alpha2
kind: ClusterQueue
metadata:
  name: team-a-cq
spec:
  namespaceSelector: {}
  cohort: team-ab
  resources:
  - name: "cpu"
    flavors:
    - name: default
      quota:
        min: 9
        max: 15
  - name: "memory"
    flavors:
    - name: default
      quota:
        min: 36Gi
        max: 60Gi
```

```yaml
# team-b-cq.yaml
apiVersion: kueue.x-k8s.io/v1alpha2
kind: ClusterQueue
metadata:
  name: team-b-cq
spec:
  namespaceSelector: {}
  cohort: team-ab
  resources:
  - name: "cpu"
    flavors:
    - name: default
      quota:
        min: 12
  - name: "memory"
    flavors:
    - name: default
      quota:
        min: 48Gi
```

Note that the ClusterQueue `team-a-cq` also defines [max quotas](/docs/concepts/cluster_queue#max-quotas).
This restricts the ability of the ClusterQueue to borrow the unused quota from
the cohort up to the configured `max`, even if the quota is completely unused.

To create these ClusterQueues, save the preceding manifests and run the
following command:

```shell
kubectl apply -f team-a-cq.yaml -f team-b-cq.yaml
```

## Multiple ClusterQueue with dedicated and fallback flavors

A ClusterQueue can borrow resources from the [cohort](/docs/concepts/cluster_queue#cohort)
even if the ClusterQueue has zero min quota for a flavor. This allows you to
give dedicated quota for a flavor and fallback to quota for a different flavor,
shared with other tenants.

Such setup can be accomplished with a ClusterQueue for each tenant and an extra
ClusterQueue for the shared resources. For example, the manifests for two
tenants look like the following:

```yaml
# team-a-cq.yaml
apiVersion: kueue.x-k8s.io/v1alpha2
kind: ClusterQueue
metadata:
  name: team-a-cq
spec:
  namespaceSelector: {}
  cohort: team-ab
  resources:
  - name: "cpu"
    flavors:
    - name: arm
      quota:
        min: 9
        max: 9
    - name: x86
      quota:
        min: 0
  - name: "memory"
    flavors:
    - name: default
      quota:
        min: 36Gi
```

```yaml
# team-b-cq.yaml
apiVersion: kueue.x-k8s.io/v1alpha2
kind: ClusterQueue
metadata:
  name: team-b-cq
spec:
  namespaceSelector: {}
  cohort: team-ab
  resources:
  - name: "cpu"
    flavors:
    - name: arm
      quota:
        min: 12
        max: 12
    - name: x86
      quota:
        min: 0
  - name: "memory"
    flavors:
    - name: default
      quota:
        min: 48Gi
```

```yaml
# shared-cq.yaml
apiVersion: kueue.x-k8s.io/v1alpha2
kind: ClusterQueue
metadata:
  name: shared-cq
spec:
  namespaceSelector: {}
  cohort: team-ab
  resources:
  - name: "cpu"
    flavors:
    - name: x86
      quota:
        min: 6
  - name: "memory"
    flavors:
    - name: default
      quota:
        min: 24Gi
```

Note the following setup:

- `team-a-cq` and `team-b-cq` define a `max` equal to their `min`
  quota for the `arm` flavor. Therefore, they can't borrow this flavor from each
  other.
- `team-a-cq` and `team-b-cq` define `min: 0` for the `x86` flavor.
  Therefore, they don't have any dedicated quota for the flavor and they can
  only borrow it from `shared-cq`.

To create these ClusterQueues, save the preceding manifests and run the
following command:

```shell
kubectl apply -f team-a-cq.yaml -f team-b-cq.yaml -f shared-cq.yaml
```
