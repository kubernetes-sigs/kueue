# Cluster Queue

A ClusterQueue is a cluster-scoped object that governs a pool of resources
such as CPU, memory, and hardware accelerators. A ClusterQueue defines:
- The [resource _flavors_](#resourceflavor-object) that the ClusterQueue manages,
  with usage limits and order of consumption.
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

This ClusterQueue admits [workloads](workload.md) if and only if:
- The sum of the CPU requests is less than or equal to 9.
- The sum of the memory requests is less than or equal to 36Gi.

You can specify the quota as a [quantity](https://kubernetes.io/docs/reference/kubernetes-api/common-definitions/quantity/).

## Resources

In a ClusterQueue, you can define quotas for multiple [compute resources](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/#resource-types)
(CPU, memory, GPUs, etc.).

For each resource, you can define quotas for multiple _flavors_.
Flavors represent different variations of a resource (for example, different GPU
models). A flavor is defined using a [ResourceFlavor object](#resourceflavor-object).

In a process called [admission](.#admission), Kueue assigns to the
[Workload pod sets](workload.md#pod-sets) a flavor for each resource the pod set
requests.
Kueue assigns the first flavor in the ClusterQueue's `.spec.resources[*].flavors`
list that has enough unused `min` quota in the ClusterQueue or the
ClusterQueue's [cohort](#cohort).

### Codepedent resources

It is possible that multiple resources in a ClusterQueue have the same flavors.
This is typical for `cpu` and `memory`, where the flavors are generally tied to
a machine family or VM availability policies. When two or more resources in a
ClusterQueue match their flavors, they are said to be codependent resources.

To manage codependent resources, you should list the flavors in the ClusterQueue
resources in the same order. During admission, for each pod set in a Workload,
Kueue assigns the same flavor to the codependent resources that the pod set requests.

An example of a ClusterQueue with codependent resources looks like the following:

```yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: ClusterQueue
metadata:
  name: cluster-total
spec:
  namespaceSelector: {}
  resources:
  - name: "cpu"
    flavors:
    - name: spot
      quota:
        min: 18
    - name: on_demand
      quota:
        min: 9
  - name: "memory"
    flavors:
    - name: spot
      quota:
        min: 72Gi
    - name: on_demand
      quota:
        min: 36Gi
  - name: "gpu"
    flavors:
    - name: vendor1
      quota:
        min: 10
    - name: vendor2
      quota:
        min: 10
```

In the example above, `cpu` and `memory` are codependent resources, while `gpu`
is independent.

If two resources are not codependent, they must not have any flavors in common.

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

## Queueing strategy

You can set different queueing strategies in a ClusterQueue using the
`.spec.queueingStrategy` field. The queueing strategy determines how workloads
are ordered in the ClusterQueue and how they are re-queued after an unsuccessful
[admission](.#admission) attempt.

The following are the supported queueing strategies:

- `StrictFIFO`: Workloads are ordered first by [priority](workload.md#priority)
  and then by `.metadata.creationTimestamp`. Older workloads that can't be
  admitted will block newer workloads, even if the newer workloads fit in the
  available quota.
- `BestEffortFIFO`: Workloads are ordered the same way as `StrictFIFO`. However,
  older workloads that can't be admitted will not block newer workloads that
  fit in the available quota.

The default queueing strategy is `BestEffortFIFO`.

## ResourceFlavor object

Resources in a cluster are typically not homogeneous. Resources could differ in:
- pricing and availability (ex: spot vs on-demand VMs)
- architecture (ex: x86 vs ARM CPUs)
- brands and models (ex: Radeon 7000 vs Nvidia A100 vs T4 GPUs)

A ResourceFlavor is an object that represents these resource variations and
allows you to associate them with node labels and taints.

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

You can use the `.metadata.name` to reference a ResourceFlavor from a
ClusterQueue in the `.spec.resources[*].flavors[*].name` field.

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
   fields in the PodSpecs of your [Workload](workload.md) against the
   ResourceFlavor labels.
2. Once the workload is admitted, Kueue adds the ResourceFlavor labels to the
  `.nodeSelector` of the underlying workload Pod templates, if the workload
   didn't specify them already.

   For example, for a [batch/v1.Job](https://kubernetes.io/docs/concepts/workloads/controllers/job/),
   Kueue adds the labels to the `.spec.template.spec.nodeSelector` field. This
   guarantees that the Workload's Pods can only be scheduled on the nodes
   targeted by the flavor that Kueue assigned to the Workload.

### ResourceFlavor taints

To restrict the usage of a ResourceFlavor, you can configure the `.taints` field
with taints.

Taints on the ResourceFlavor work similarly to [node taints](https://kubernetes.io/docs/concepts/scheduling-eviction/taint-and-toleration/).
For Kueue to admit a workload to use the ResourceFlavor, the PodSpecs in the
workload should have a toleration for it. As opposed to the behavior for
[ResourceFlavor labels](#resourceflavor-labels), Kueue does not add tolerations
for the flavor taints.

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
same cohort can borrow unused quota from each other.

To add a ClusterQueue to a cohort, specify the name of the cohort in the
`.spec.cohort` field. All ClusterQueues that have a matching `spec.cohort` are
part of the same cohort. If the `spec.cohort` field is empty, the ClusterQueue
doesn't belong to any cohort, and thus it cannot borrow quota from any other
ClusterQueue.

### Flavors and borrowing semantics

When a ClusterQueue is part of a cohort, Kueue satisfies the following admission
semantics:

- When assigning flavors, Kueue goes through the list of flavors in the
  ClusterQueue's `.spec.resources[*].flavors`. For each flavor, Kueue attempts
  to fit a Workload's pod set according to the quota defined in the
  ClusterQueue for the flavor and the unused quota in the cohort.
  If the workload doesn't fit, Kueue evaluates the next flavor in the list.
- A Workload's pod set resource fits in a flavor defined for a ClusterQueue
  resource if the sum of requests for the resource:
  1. Is less than or equal to the unused `.quota.min` for the flavor in the
     ClusterQueue; or
  2. Is less than or equal to the sum of unused `.quota.min` for the flavor in
     the ClusterQueues in the cohort, and
  3. Is less than or equal to the unused `.quota.max` for the flavor in the
     ClusterQueue.
  In Kueue, when (2) and (3) are satisfied, but not (1), this is called
  _borrowing quota_.
- A ClusterQueue can only borrow quota for flavors that the ClusterQueue defines.
- For each pod set resource in a Workload, a ClusterQueue can only borrow quota
  for one flavor.

### Borrowing example

Assume you created the following two ClusterQueues:

```yaml
apiVersion: kueue.x-k8s.io/v1alpha1
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
  - name: "memory"
    flavors:
    - name: default
      quota:
        min: 36Gi
```

```yaml
apiVersion: kueue.x-k8s.io/v1alpha1
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

ClusterQueue `team-a-cq` can admit workloads depending on the following
scenarios:

- If ClusterQueue `team-b-cq` has no admitted workloads, then ClusterQueue
  `team-a-cq` can admit workloads with resources adding up to `12+9=21` CPUs and
  `48+36=84Gi` of memory.
- If ClusterQueue `team-b-cq` has pending workloads and the ClusterQueue
  `team-a-cq` has all its `min` quota used, Kueue will admit workloads in
  ClusterQueue `team-b-cq` before admitting any new workloads in `team-a-cq`.
  Therefore, Kueue ensures the `min` quota for `team-b-cq` is met.

**Note**: Kueue [does not support preemption](https://github.com/kubernetes-sigs/kueue/issues/83).
No admitted workloads will be stopped to make space for new workloads.

### Max quotas

To limit the amount of resources that a ClusterQueue can borrow from others,
you can set the `.spec.resources[*].flavors[*].quota.max`
[quantity](https://kubernetes.io/docs/reference/kubernetes-api/common-definitions/quantity/) field.
`max` must be greater than or equal to `min`.

If, for a given flavor, the `max` field is empty or null, a ClusterQueue can
borrow up to the sum of min quotas from all the ClusterQueues in the cohort.

## What's next?

- Learn how to [administer cluster quotas](/docs/tasks/administer_cluster_quotas.md).
