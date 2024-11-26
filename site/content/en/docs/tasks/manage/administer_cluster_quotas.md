---
title: "Administer Cluster Quotas"
date: 2022-03-14
weight: 2
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
- [Kueue is installed](/docs/installation).

## Single ClusterQueue and single ResourceFlavor setup

In the following steps, you will create a queuing system with a single
ClusterQueue and a single [ResourceFlavor](/docs/concepts/cluster_queue#resourceflavor-object)
to govern the quota of your cluster.

You can perform all these steps at once by
applying [examples/admin/single-clusterqueue-setup.yaml](/examples/admin/single-clusterqueue-setup.yaml):

```shell
kubectl apply -f examples/admin/single-clusterqueue-setup.yaml
```

### 1. Create a [ClusterQueue](/docs/concepts/cluster_queue)

Create a single ClusterQueue to represent the resource quotas for your entire
cluster.

Write the manifest for the ClusterQueue. It should look similar to the following:

```yaml
# cluster-queue.yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "cluster-queue"
spec:
  namespaceSelector: {} # match all.
  resourceGroups:
  - coveredResources: ["cpu", "memory"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "cpu"
        nominalQuota: 9
      - name: "memory"
        nominalQuota: 36Gi
```

To create the ClusterQueue, run the following command:

```shell
kubectl apply -f cluster-queue.yaml
```

This ClusterQueue governs the usage of [resource types](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/#resource-types)
`cpu` and `memory`. Each resource type has a single [resource flavor](/docs/concepts/cluster_queue#resourceflavor-object),
named `default` with a nominal quota.

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
apiVersion: kueue.x-k8s.io/v1beta1
kind: ResourceFlavor
metadata:
  name: "default-flavor"
```

To create the ResourceFlavor, run the following command:

```shell
kubectl apply -f default-flavor.yaml
```

The `.metadata.name` matches the `.spec.resourceGroups[0].flavors[0].name`
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
apiVersion: kueue.x-k8s.io/v1beta1
kind: LocalQueue
metadata:
  namespace: "default"
  name: "user-queue"
spec:
  clusterQueue: "cluster-queue"
```

To create the LocalQueue, run the following command:

```shell
kubectl apply -f default-user-queue.yaml
```

## Multiple ResourceFlavors setup

You can define quotas for different [resource flavors](/docs/concepts/cluster_queue#resourceflavor-object).

For the rest of this section, assume that your cluster has nodes with two CPU
architectures, namely `x86` and `arm`, specified in the node label `cpu-arch`.


### 1. Create ResourceFlavors

Write the manifests for the ResourceFlavors. They should look similar to the
following:

```yaml
# flavor-x86.yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ResourceFlavor
metadata:
  name: "x86"
spec:
  nodeLabels:
    cpu-arch: x86
```

```yaml
# flavor-arm.yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ResourceFlavor
metadata:
  name: "arm"
spec:
  nodeLabels:
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
# cluster-queue.yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "cluster-queue"
spec:
  namespaceSelector: {} # match all.
  resourceGroups:
  - coveredResources: ["cpu"]
    flavors:
    - name: "x86"
      resources:
      - name: "cpu"
        nominalQuota: 9
    - name: "arm"
      resources:
      - name: "cpu"
        nominalQuota: 12
  - coveredResources: ["memory"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "memory"
        nominalQuota: 84Gi
```

The flavor names in the fields `.spec.resourceGroups[*].flavors[*].name`
should match the names of the ResourceFlavors created earlier.

Note that `memory` is referencing the `default-flavor` flavor created in the [single flavor setup.](#single-clusterqueue-and-single-resourceflavor-setup)
This means that you don't want to distinguish if the memory is given from `x86`
or `arm` nodes.

To create the ClusterQueue, run the following command:

```shell
kubectl apply -f cluster-queue.yaml
```

## Multiple ClusterQueues and borrowing cohorts

Two or more ClusterQueues can borrow unused quota from other ClusterQueues in
the same [cohort](/docs/concepts/cluster_queue#cohort).

Using the following example, you can establish a cohort `team-ab` that includes
ClusterQueues `team-a-cq` and `team-b-cq`.

```yaml
# team-a-cq.yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "team-a-cq"
spec:
  namespaceSelector: {} # match all.
  cohort: "team-ab"
  resourceGroups:
  - coveredResources: ["cpu", "memory"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "cpu"
        nominalQuota: 9
        borrowingLimit: 6
      - name: "memory"
        nominalQuota: 36Gi
        borrowingLimit: 24Gi
```

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "team-b-cq"
spec:
  namespaceSelector: {}
  cohort: "team-ab"
  resourceGroups:
  - coveredResources: ["cpu", "memory"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "cpu"
        nominalQuota: 12
      - name: "memory"
        nominalQuota: 48Gi
```

Note that the ClusterQueue `team-a-cq` also defines [borrowingLimit](/docs/concepts/cluster_queue#borrowingLimit).
This restricts the ability of the ClusterQueue to borrow the unused quota from
the cohort up to the configured `borrowingLimit`, even if the quota is completely unused.

To create these ClusterQueues, save the preceding manifests and run the
following command:

```shell
kubectl apply -f team-a-cq.yaml -f team-b-cq.yaml
```

## Multiple ClusterQueue with dedicated and fallback flavors

A ClusterQueue can borrow resources from the [cohort](/docs/concepts/cluster_queue#cohort)
even if the ClusterQueue has zero nominalQuota for a flavor. This allows you to
give dedicated quota for a flavor and fallback to quota for a different flavor,
shared with other tenants.

Such setup can be accomplished with a ClusterQueue for each tenant and an extra
ClusterQueue for the shared resources. For example, the manifests for two
tenants look like the following:

```yaml
# team-a-cq.yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "team-a-cq"
spec:
  namespaceSelector: {} # match all.
  cohort: "team-ab"
  resourceGroups:
  - coveredResources: ["cpu"]
    flavors:
    - name: "arm"
      resources:
      - name: "cpu"
        nominalQuota: 9
        borrowingLimit: 0
    - name: "x86"
      resources:
      - name: "cpu"
        nominalQuota: 0
  - coveredResources: ["memory"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "memory"
        nominalQuota: 36Gi
```

```yaml
# team-b-cq.yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "team-b-cq"
spec:
  namespaceSelector: {} # match all.
  cohort: "team-ab"
  resourceGroups:
  - coveredResources: ["cpu"]
    flavors:
    - name: "arm"
      resources:
      - name: "cpu"
        nominalQuota: 12
        borrowingLimit: 0
    - name: "x86"
      resources:
      - name: "cpu"
        nominalQuota: 0
  - coveredResources: ["memory"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "memory"
        nominalQuota: 48Gi
```

```yaml
# shared-cq.yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "shared-cq"
spec:
  namespaceSelector: {} # match all.
  cohort: "team-ab"
  resourceGroups:
  - coveredResources: ["cpu"]
    flavors:
    - name: "x86"
      resources:
      - name: "cpu"
        nominalQuota: 6
  - coveredResources: ["memory"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "memory"
        nominalQuota: 24Gi
```

Note the following setup:

- `team-a-cq` and `team-b-cq` define a `borrowingLimit: 0` 
  for the `arm` flavor. Therefore, they can't borrow this flavor from each
  other.
- `team-a-cq` and `team-b-cq` define `nominalQuota: 0` for the `x86` flavor.
  Therefore, they don't have any dedicated quota for the flavor and they can
  only borrow it from `shared-cq`.

To create these ClusterQueues, save the preceding manifests and run the
following command:

```shell
kubectl apply -f team-a-cq.yaml -f team-b-cq.yaml -f shared-cq.yaml
```

## Exclude arbitrary resources in the quota management 
By default, administrators must specify all resources required by Pods in the ClusterQueues `.spec.resourceGroups[*]`.
If you want to exclude some resources in the ClusterQueues quota management and admission process, 
you can specify the resource prefixes in the Kueue Configuration as a cluster-level setting.

Follow the [installation instructions for using a custom configuration](/docs/installation#install-a-custom-configured-released-version) 
and extend the configuration with fields similar to the following:

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta1
kind: Configuration
resources:
  excludeResourcePrefixes:
  - "example.com"
```

## Transform resources for quota management

{{< feature-state state="beta" for_version="v0.10" >}}
{{% alert title="Note" color="primary" %}}

`ConfigurableResourceTransformation` is a Beta feature that is enabled by default.

You can disable it by setting the `ConfigurableResourceTransformation` feature gate. Check the [Installation](/docs/installation/#change-the-feature-gates-configuration) guide for details on feature gate configuration.
{{% /alert %}}

An administrator may customize how the resources requested by Pods are
converted into Workload resource requests. This enables
the admission and quota calculations done by ClusterQueues to be performed
on a transformed set of resources requirements without changing
the resource requests and limits that the Pods created by the Workload
will present to the Kubernetes Scheduler when the Workload is admitted by
a ClusterQueue. Customizations are defined by specifying resource transformations
in the Kueue configuration as a cluster-level setting.

The supported transformations enable mapping an input resource into one or more
output resources by multiplying the input resource quantity by a scaling factor.
The input resource may either be retained (default) or removed from the transformed resources. If no transformation is defined for an input resource, it is retained without change.

Follow the [installation instructions for using a custom configuration](/docs/installation#install-a-custom-configured-released-version)
and extend the Kueue configuration with fields similar to the following:

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta1
kind: Configuration
resources:
  transformations:
  - input: example.com/gpu-type1:
    strategy: Replace
    outputs:
      example.com/gpu-memory: 5Gi
      example.com/credits: 10
  - input: example.com/gpu-type2:
    strategy: Replace
    outputs:
      example.com/gpu-memory: 20Gi
      example.com/credits: 40
  - input: cpu
    strategy: Retain
    outputs:
      example.com/credits: 1
```

With this example configuration, a Pod that requests:
```yaml
    resources:
      requests:
        cpu: 1
        memory: 100Gi
      limits:
        example.com/gpu-type1: 2
        example.com/gpu-type2: 1
```

The Workload obtains an effective resource requests for quota purposes:

```yaml
    resources:
      requests:
        cpu: 1
        memory: 100Gi
        example.com/gpu-memory: 30Gi
        example.com/credits: 61
```
