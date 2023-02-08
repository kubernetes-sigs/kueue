# Cluster Queue

A ClusterQueue is a cluster-scoped object that governs a pool of resources
such as CPU, memory, and hardware accelerators. A ClusterQueue defines:
- The quotas for the [resource _flavors_](/docs/concepts/resource_flavor.md) that the ClusterQueue manages,
  with usage limits and order of consumption.
- Fair sharing rules across the multiple ClusterQueues in the cluster.

Only [cluster administrators](/docs/tasks#batch-administrator) should create `ClusterQueue` objects.

A sample ClusterQueue looks like the following:

```yaml
apiVersion: kueue.x-k8s.io/v1alpha2
kind: ClusterQueue
metadata:
  name: cluster-queue
spec:
  namespaceSelector: {}
  resources:
  - name: "cpu"
    flavors:
    - name: default-flavor
      quota:
        min: 9
  - name: "memory"
    flavors:
    - name: default-flavor
      quota:
        min: 36Gi
```

This ClusterQueue admits [Workloads](workload.md) if and only if:
- The sum of the CPU requests is less than or equal to 9.
- The sum of the memory requests is less than or equal to 36Gi.

You can specify the quota as a [quantity](https://kubernetes.io/docs/reference/kubernetes-api/common-definitions/quantity/).

## Resources

In a ClusterQueue, you can define quotas for multiple [compute resources](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/#resource-types)
(CPU, memory, GPUs, etc.).

For each resource, you can define quotas for multiple _flavors_.
Flavors represent different variations of a resource (for example, different GPU
models). You can define a flavor using a [ResourceFlavor object](/docs/concepts/resource_flavor.md).

In a process called [admission](README.md#admission), Kueue assigns to the
[Workload pod sets](workload.md#pod-sets) a flavor for each resource the pod set
requests.
Kueue assigns the first flavor in the ClusterQueue's `.spec.resources[*].flavors`
list that has enough unused `min` quota in the ClusterQueue or the
ClusterQueue's [cohort](#cohort).

### Codependent resources

It is possible that multiple resources in a ClusterQueue have the same flavors.
This is typical for `cpu` and `memory`, where the flavors are generally tied to
a machine family or VM availability policies. When two or more resources in a
ClusterQueue match their flavors, they are said to be codependent resources.

To manage codependent resources, you should list the flavors in the ClusterQueue
resources in the same order. During admission, for each pod set in a Workload,
Kueue assigns the same flavor to the codependent resources that the pod set requests.

An example of a ClusterQueue with codependent resources looks like the following:

```yaml
apiVersion: kueue.x-k8s.io/v1alpha2
kind: ClusterQueue
metadata:
  name: cluster-queue
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

You can limit which namespaces can have Workloads admitted in the ClusterQueue
by setting a [label selector](https://kubernetes.io/docs/reference/kubernetes-api/common-definitions/label-selector/#LabelSelector).
in the `.spec.namespaceSelector` field.

To allow Workloads from all namespaces, set the empty selector `{}` to the
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
`.spec.queueingStrategy` field. The queueing strategy determines how Workloads
are ordered in the ClusterQueue and how they are re-queued after an unsuccessful
[admission](README.md#admission) attempt.

The following are the supported queueing strategies:

- `StrictFIFO`: Workloads are ordered first by [priority](Workload.md#priority)
  and then by `.metadata.creationTimestamp`. Older Workloads that can't be
  admitted will block newer Workloads, even if the newer Workloads fit in the
  available quota.
- `BestEffortFIFO`: Workloads are ordered the same way as `StrictFIFO`. However,
  older Workloads that can't be admitted will not block newer Workloads that
  fit in the available quota.

The default queueing strategy is `BestEffortFIFO`.

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
  If the Workload doesn't fit, Kueue evaluates the next flavor in the list.
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
    - name: default-flavor
      quota:
        min: 9
  - name: "memory"
    flavors:
    - name: default-flavor
      quota:
        min: 36Gi
```

```yaml
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
    - name: default-flavor
      quota:
        min: 12
  - name: "memory"
    flavors:
    - name: default-flavor
      quota:
        min: 48Gi
```

ClusterQueue `team-a-cq` can admit Workloads depending on the following
scenarios:

- If ClusterQueue `team-b-cq` has no admitted Workloads, then ClusterQueue
  `team-a-cq` can admit Workloads with resources adding up to `12+9=21` CPUs and
  `48+36=84Gi` of memory.
- If ClusterQueue `team-b-cq` has pending Workloads and the ClusterQueue
  `team-a-cq` has all its `min` quota used, Kueue will admit Workloads in
  ClusterQueue `team-b-cq` before admitting any new Workloads in `team-a-cq`.
  Therefore, Kueue ensures the `min` quota for `team-b-cq` is met.

**Note**: Kueue [does not support preemption](https://github.com/kubernetes-sigs/kueue/issues/83).
No admitted Workloads will be stopped to make space for new Workloads.

### Max quotas

To limit the amount of resources that a ClusterQueue can borrow from others,
you can set the `.spec.resources[*].flavors[*].quota.max`
[quantity](https://kubernetes.io/docs/reference/kubernetes-api/common-definitions/quantity/) field.
`max` must be greater than or equal to `min`.

If, for a given flavor, the `max` field is empty or null, a ClusterQueue can
borrow up to the sum of min quotas from all the ClusterQueues in the cohort.

## What's next?

- Create [local queues](/docs/concepts/local_queue.md)
- Create [resource flavors](/docs/concepts/resource_flavor.md) if you haven't already done so.
- Learn how to [administer cluster quotas](/docs/tasks/administer_cluster_quotas.md).
