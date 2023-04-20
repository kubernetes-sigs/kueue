---
title: "Cluster Queue"
date: 2023-03-14
weight: 3
description: >
  A cluster-scoped resource that governs a pool of resources, defining usage limits and fair sharing rules.
---

A ClusterQueue is a cluster-scoped object that governs a pool of resources
such as CPU, memory, and hardware accelerators. A ClusterQueue defines:

- The quotas for the [resource _flavors_](/docs/concepts/resource_flavor) that the ClusterQueue manages,
  with usage limits and order of consumption.
- Fair sharing rules across the multiple ClusterQueues in the cluster.

Only [cluster administrators](/docs/tasks#batch-administrator) should create `ClusterQueue` objects.

A sample ClusterQueue looks like the following:

```yaml
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

This ClusterQueue admits [Workloads](/docs/concepts/workload) if and only if:

- The sum of the CPU requests is less than or equal to 9.
- The sum of the memory requests is less than or equal to 36Gi.

You can specify the quota as a [quantity](https://kubernetes.io/docs/reference/kubernetes-api/common-definitions/quantity/).

![Cohort](/images/cluster-queue.svg)

## Resources

In a ClusterQueue, you can define quotas for multiple [compute resources](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/#resource-types)
(CPU, memory, GPUs, etc.).

For each resource, you can define quotas for multiple _flavors_.
Flavors represent different variations of a resource (for example, different GPU
models). You can define a flavor using a [ResourceFlavor object](/docs/concepts/resource_flavor).

In a process called [admission](/docs/concepts#admission), Kueue assigns to the
[Workload pod sets](/docs/concepts/workload#pod-sets) a flavor for each resource the pod set
requests.
Kueue assigns the first flavor in the ClusterQueue's `.spec.resourceGroups[*].flavors`
list that has enough unused `nominalQuota` quota in the ClusterQueue or the
ClusterQueue's [cohort](#cohort).

### Resource Groups

It is possible that multiple resources in a ClusterQueue have the same flavors.
This is typical for `cpu` and `memory`, where the flavors are generally tied to
a machine family or VM availability policies. To tie two or more resources to 
the same set of flavors, you can list them in the same resource group.

An example of a ClusterQueue with multiple resource groups looks like the following:

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "cluster-queue"
spec:
  namespaceSelector: {} # match all.
  resourceGroups:
  - coveredResources: ["cpu", "memory"]
    flavors:
    - name: "spot"
      resources:
      - name: "cpu"
        nominalQuota: 9
      - name: "memory"
        nominalQuota: 36Gi
    - name: "on-demand"
      resources:
      - name: "cpu"
        nominalQuota: 18
      - name: "memory"
        nominalQuota: 72Gi
  - coveredResources: ["gpu"]
    flavors:
    - name: "vendor1"
      resources:
      - name: "gpu"
        nominalQuota: 10
    - name: "vendor2"
      resources:
      - name: "gpu"
        nominalQuota: 10
```

In the example above, `cpu` and `memory` belong to one resourceGroup, while `gpu`
belongs to another.

A resource flavor must belong to at most one resource group.

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
[admission](/docs/concepts#admission) attempt.

The following are the supported queueing strategies:

- `StrictFIFO`: Workloads are ordered first by [priority](/docs/concepts/workload#priority)
  and then by `.metadata.creationTimestamp`. Older workloads that can't be
  admitted will block newer workloads, even if the newer workloads fit in the
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
  relevant ResourceGroup inside ClusterQueue's 
  (`.spec.resourceGroups[*].flavors`). For each flavor, Kueue attempts
  to fit a Workload's pod set according to the quota defined in the
  ClusterQueue for the flavor and the unused quota in the cohort.
  If the Workload doesn't fit, Kueue evaluates the next flavor in the list.
- A Workload's pod set resource fits in a flavor defined for a ClusterQueue
  resource if the sum of requests for the resource:
  1. Is less than or equal to the unused `nominalQuota` for the flavor in the
     ClusterQueue; or
  2. Is less than or equal to the sum of unused `nominalQuota` for the flavor in
     the ClusterQueues in the cohort, and
  3. Is less than or equal to the unused `nominalQuota + borrowingLimit` for 
     the flavor in the ClusterQueue.
  In Kueue, when (2) and (3) are satisfied, but not (1), this is called
  _borrowing quota_.
- A ClusterQueue can only borrow quota for flavors that the ClusterQueue defines.
- For each pod set resource in a Workload, a ClusterQueue can only borrow quota
  for one flavor.

### Borrowing example

Assume you created the following two ClusterQueues:


```yaml
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
        borrowingLimit: 1
      - name: "memory"
        nominalQuota: 36Gi
```

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "team-b-cq"
spec:
  namespaceSelector: {} # match all.
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

ClusterQueue `team-a-cq` can admit Workloads depending on the following
scenarios:

- If ClusterQueue `team-b-cq` has no admitted Workloads, then ClusterQueue
  `team-a-cq` can admit Workloads with resources adding up to `12+9=21` CPUs and
  `48+36=84Gi` of memory.
- If ClusterQueue `team-b-cq` has pending Workloads and the ClusterQueue
  `team-a-cq` has all its `nominalQuota` quota used, Kueue will admit Workloads in
  ClusterQueue `team-b-cq` before admitting any new Workloads in `team-a-cq`.
  Therefore, Kueue ensures the `nominalQuota` quota for `team-b-cq` is met.

You can test borrowing limit by create the following two jobs and local queues:

```
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: LocalQueue
metadata:
  namespace: "default"
  name: "user-queue-1"
spec:
  clusterQueue: "team-a-cq"
---
apiVersion: batch/v1
kind: Job
metadata:
  name: sample-job-team-a
  namespace: default
  labels:
    kueue.x-k8s.io/queue-name: user-queue-1
spec:
  parallelism: 1
  completions: 1
  suspend: true
  template:
    spec:
      containers:
      - name: dummy-job
        image: gcr.io/k8s-staging-perf-tests/sleep:v0.0.3
        args: ["30s"]
        resources:
          requests:
            cpu: 10
            memory: "200Mi"
      restartPolicy: Never
```

```
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: LocalQueue
metadata:
  namespace: "default"
  name: "user-queue-2"
spec:
  clusterQueue: "team-b-cq"
---
apiVersion: batch/v1
kind: Job
metadata:
  name: sample-job-team-b
  namespace: default
  labels:
    kueue.x-k8s.io/queue-name: user-queue-2
spec:
  parallelism: 1
  completions: 1
  suspend: true
  template:
    spec:
      containers:
      - name: dummy-job
        image: gcr.io/k8s-staging-perf-tests/sleep:v0.0.3
        args: ["30s"]
        resources:
          requests:
            cpu: 12
            memory: "200Mi"
      restartPolicy: Never
```

You will see job `sample-job-team-a` is admitted because it borrowed 1 cpu from the cohort and `sample-job-team-b` can't be admitted because there is not enough cpu in the cohort.

### BorrowingLimit

To limit the amount of resources that a ClusterQueue can borrow from others,
you can set the `.spec.resourcesGroup[*].flavors[*].resource[*].borrowingLimit`
[quantity](https://kubernetes.io/docs/reference/kubernetes-api/common-definitions/quantity/) field.

If, for a given flavor/resource, the `borrowingLimit` field is empty or null, 
a ClusterQueue can borrow up to the sum of nominal quotas from all the 
ClusterQueues in the cohort.

## Preemption

When there is not enough quota left in a ClusterQueue or its cohort, an incoming
Workload can trigger preemption of previously admitted Workloads, based on
policies for the ClusterQueue.

A configuration for a ClusterQueue that enables preemption looks like the
following:

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "team-a-cq"
spec:
  preemption:
    reclaimWithinCohort: Any
    withinClusterQueue: LowerPriority
```

The fields above have the following semantics:

- `reclaimWithinCohort` determines whether a pending Workload can preempt
  Workloads from other ClusterQueues in the cohort that are using more than
  their nominal quota. The possible values are:
	  - `Never` (default): do not preempt Workloads in the cohort.
	  - `LowerPriority`: if the pending Workload fits within the nominal
	    quota of its ClusterQueue, only preempt Workloads in the cohort that have
	    lower priority than the pending Workload.
	  - `Any`: if the pending Workload fits within the nominal quota of its
	    ClusterQueue, preempt any Workload in the cohort, irrespective of
	    priority.

- `withinClusterQueue` determines whether a pending Workload that doesn't fit
  within the nominal quota for its ClusterQueue, can preempt active Workloads in
  the ClusterQueue. The possible values are:
  - `Never` (default): do not preempt Workloads in the ClusterQueue.
  - `LowerPriority`: only preempt Workloads in the ClusterQueue that have
    lower priority than the pending Workload.

Note that an incoming Workload can preempt Workloads both within the
ClusterQueue and the cohort. Kueue implements heuristics to preempt as few
Workloads as possible, preferring Workloads with these characteristics:
- Workloads belonging to ClusterQueues that are borrowing quota.
- Workloads with the lowest priority.
- Workloads that have been admitted more recently.

## What's next?

- Create [local queues](/docs/concepts/local_queue)
- Create [resource flavors](/docs/concepts/resource_flavor) if you haven't already done so.
- Learn how to [administer cluster quotas](/docs/tasks/administer_cluster_quotas).
