# Cluster Queue

A `ClusterQueue` is a cluster-scoped object that governs a pool of resources
such as CPU, memory and hardware accelerators. A `ClusterQueue` defines:
- The resource _flavors_ that it manages, with usage limits and order of consumption.
- Fair sharing rules across the tenants of the cluster.

Only [cluster administrators](/docs/tasks#batch-administrator) should create `ClusterQueue` objects.

A sample ClusterQueue looks like the following:

```yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: ClusterQueue
metadata:
  name: cluster-total
spec:
  namespaceSelector: {}
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

This ClusterQueue admits [workloads](queued_workload.md) if and only if:
- The sum of the CPU requests is less than or equal to 9.
- The sum of the memory requests is less than or equal to 36Gi.

## Namespace selector

You can limit which namespaces can have workloads admitted in the ClusterQueue
by setting a [label selector](https://kubernetes.io/docs/reference/kubernetes-api/common-definitions/label-selector/#LabelSelector).
in the `.spec.namespaceSelector` field.

To allow workloads from all namespaces, set the empty selector `{}` to the
`spec.namespaceSelector` field.

A sample `namespaceSelector` looks like the following:

```yaml
namespaceSelector:
  matchExpressions:
  - key: team
    operator: In
    values:
    - team-a
```

## ResourceFlavor object

Resources in a cluster are typically not homogeneous. Resources could differ in:
- pricing and availability (ex: spot vs on-demand VMs)
- architecture (ex: x86 vs ARM CPUs)
- brands and models (ex: Radeon 7000 vs Nvidia A100 vs T4 GPUs)

A `ResourceFlavor` is an object that represents these variations and allows you
to associate them with node labels and taints.

**Note**: If your cluster is homogeneous, you can use an [empty ResourceFlavor](#empty-resourceflavor)
instead of adding labels to custom ResourceFlavors.

A sample ResourceFlavor looks like the following:

```yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: ResourceFlavor
metadata:
  name: spot
labels:
  instance-type: spot
taints:
- effect: NoSchedule
  key: spot
  value: "true"
```

You can use the `.metadata.name` to reference a flavor from a ClusterQueue.

### ResourceFlavor labels

To associate a ResourceFlavor with a subset of nodes of you cluster, you can
configure the `.labels` field with matching node labels that uniquely identify
the nodes. If you are using [cluster autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler)
(or equivalent controllers), make sure it is configured to add those labels when
adding new nodes.

To guarantee that the workload Pods run on the nodes associated to the flavor
that Kueue decided that the workload should use, Kueue performs the following
steps:

1. When admitting a workload, Kueue evaluates the
   [`.nodeSelector`](https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/#nodeselector)
   and [`.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution`](https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/#node-affinity)
   fields in the PodSpecs of your [QueuedWorkloads](queued_workload.md) against the
   ResourceFlavor labels.
2. Once the workload is admitted, Kueue adds the ResourceFlavor labels to the
  `.nodeSelector` of the underlying workload Pod templates, if the workload
   didn't specify them already.

   For example, for a [batch/v1.Job](https://kubernetes.io/docs/concepts/workloads/controllers/job/),
   Kueue adds the labels to `.spec.template.spec.nodeSelector`. This guarantees
   that the workload Pods run on the nodes associated to the flavor that Kueue
   decided that the workload should use.

### ResourceFlavor taints

To restrict the usage of a ResourceFlavor, you can configure the `.taints` field
with taints.

Taints on the ResourceFlavor work similarly to [node taints](https://kubernetes.io/docs/concepts/scheduling-eviction/taint-and-toleration/).
For Kueue to admit a workload to use the ResourceFlavor, the PodSpecs in the
workload should have a toleration for it. As opposed to ResourceFlavor labels,
Kueue will not add tolerations for the flavor taints.

### Empty ResourceFlavor

If your cluster has homogeneous resources, or if you don't need to manage
quotas for the different flavors of a resource separately, you can create a
ResourceFlavor without any labels or taints. Such ResourceFlavor is called an
empty ResourceFlavor and its sample looks like the following:

```yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: ResourceFlavor
metadata:
  name: default
```

## Cohort

ClusterQueues can be grouped in _cohorts_. ClusterQueues that belong to the
same cohort can borrow unused quota from each other, if they reference
matching resource flavors in
`.spec.requestableResources[*].flavors[*].resourceFlavor`.

## What's next?

- Learn how to [administer cluster quotas](/docs/tasks/administer_cluster_quotas.md).
