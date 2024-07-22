---
title: "Cluster Queue"
date: 2023-03-14
weight: 3
description: >
  A cluster-scoped resource that governs a pool of resources, defining usage limits and fair sharing rules.
---

A ClusterQueue is a cluster-scoped object that governs a pool of resources
such as pods, CPU, memory, and hardware accelerators. A ClusterQueue defines:

- The quotas for the [resource _flavors_](/docs/concepts/resource_flavor) that the ClusterQueue manages,
  with usage limits and order of consumption.
- Fair sharing rules across the multiple ClusterQueues in the cluster.

Only [batch administrators](/docs/tasks#batch-administrator) should create `ClusterQueue` objects.

A sample ClusterQueue looks like the following:

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "cluster-queue"
spec:
  namespaceSelector: {} # match all.
  resourceGroups:
  - coveredResources: ["cpu", "memory", "pods"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "cpu"
        nominalQuota: 9
      - name: "memory"
        nominalQuota: 36Gi
      - name: "pods"
        nominalQuota: 5
```

This ClusterQueue admits [Workloads](/docs/concepts/workload) if and only if:

- The sum of the CPU requests is less than or equal to 9.
- The sum of the memory requests is less than or equal to 36Gi.
- The total number of pods is less than or equal to 5.

You can specify the quota as a [quantity](https://kubernetes.io/docs/reference/kubernetes-api/common-definitions/quantity/).

![Cohort](/images/cluster-queue.svg)

## Resources

In a ClusterQueue, you can define quotas for multiple [compute resources](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/#resource-types)
(CPU, memory, GPUs, pods, etc.).

For each resource, you can define quotas for multiple _flavors_.
Flavors represent different variations of a resource (for example, different GPU
models). You can define a flavor using a [ResourceFlavor object](/docs/concepts/resource_flavor).

When definining quotas for a ClusterQueue, you can set the following values:
- `nominalQuota` is the quantity of this resource that is available for a ClusterQueue at a specific time.
- `borrowingLimit` is the maximum amount of quota that this ClusterQueue is allowed to borrow from the unused
  nominal quota of other ClusterQueues in the same [cohort](#cohort).
- `lendingLimit` is the maximum amount of quota that this ClusterQueue allows other
  ClusterQueues in the cohort to borrow when this ClusterQueue is not using its nominal quota.

In a process called [admission](/docs/concepts#admission), Kueue assigns to the
[Workload pod sets](/docs/concepts/workload#pod-sets) a flavor for each resource the pod set
requests.
Kueue assigns the first flavor in the ClusterQueue's `.spec.resourceGroups[*].flavors`
list that has enough unused `nominalQuota` quota in the ClusterQueue or the
ClusterQueue's [cohort](#cohort).

Since `pods` resource name is [reserved](/docs/concepts/workload/#reserved-resource-names) and it's value
is computed by Kueue in the during [admission](/docs/concepts#admission), not provided by the [batch user](/docs/tasks/#batch-user),
it could be used by the [batch administrators](/docs/tasks#batch-administrator) to limit the number of zero or very
small resource requesting workloads admitted at the same time.

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
  - coveredResources: ["cpu", "memory", "pods"]
    flavors:
    - name: "spot"
      resources:
      - name: "cpu"
        nominalQuota: 9
      - name: "memory"
        nominalQuota: 36Gi
      - name: "pods"
        nominalQuota: 50
    - name: "on-demand"
      resources:
      - name: "cpu"
        nominalQuota: 18
      - name: "memory"
        nominalQuota: 72Gi
      - name: "pods"
        nominalQuota: 100
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

There are multiple ways to allow specific namespaces access to a Cluster Queue. A sample `namespaceSelector` using `matchLabels` to match the workload to a namespace `team-a` looks like the following:

```yaml
namespaceSelector:
  matchLabels:
    kubernetes.io/metadata.name: team-a
```

Here `kubernetes.io/metadata.name: team-a` refers to an immutable label `kubernetes.io/metadata.name` that the Kubernetes control plane sets on all namespaces. The value of the label is the namespace name, in this case `team-a`.

However, `matchLabels` can take any key that matches a label present in the Namespace objects. For example, let's assume `team-a` and `team-b` are in a cohort `team-a-b` and the user defined label `research-cohort: team-a-b` is present on both namespaces like so:

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: team-a
  labels:
    research-cohort: team-a-b
```

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: team-b
  labels:
    research-cohort: team-a-b
```

A namespaceSelector configuration allowing both namespaces to submit Jobs to this ClusterQueue would look something like this:

```yaml
namespaceSelector:
  matchLabels:
    research-cohort: team-a-b
```

Another way to configure `namespaceSelector` is using `matchExpressions`. See [Kubernetes documentation](https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/#resources-that-support-set-based-requirements) for more details.

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

{{% alert title="Note" color="primary" %}}
Within a Cohort, Kueue prioritizes scheduling workloads that will fit under `nominalQuota`.
By default, if multiple workloads require `borrowing`, Kueue will try to schedule workloads with higher [priority](/docs/concepts/workload#priority) first.
If the feature gate `PrioritySortingWithinCohort=false` is set, Kueue will try to schedule workloads with the earliest `.metadata.creationTimestamp`.
{{% /alert %}}

You can influence some semantics of flavor selection and borrowing
by setting a [`flavorFungibility`](/docs/concepts/cluster_queue#flavorfungibility) in ClusterQueue.

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

### BorrowingLimit

To limit the amount of resources that a ClusterQueue can borrow from others,
you can set the `.spec.resourcesGroup[*].flavors[*].resource[*].borrowingLimit`
[quantity](https://kubernetes.io/docs/reference/kubernetes-api/common-definitions/quantity/) field.

As an example, assume you created the following two ClusterQueues:

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
```

In this case, because we set borrowingLimit in ClusterQueue `team-a-cq`, if
ClusterQueue `team-b-cq` has no admitted Workloads, then ClusterQueue `team-a-cq`
can admit Workloads with resources adding up to `9+1=10` CPUs.

If, for a given flavor/resource, the `borrowingLimit` field is empty or null,
a ClusterQueue can borrow up to the sum of nominal quotas from all the
ClusterQueues in the cohort. So for the yamls listed above, `team-b-cq` can
use up to `12+9` CPUs.

### LendingLimit

To limit the amount of resources that a ClusterQueue can lend in the cohort,
you can set the `.spec.resourcesGroup[*].flavors[*].resource[*].lendingLimit`
[quantity](https://kubernetes.io/docs/reference/kubernetes-api/common-definitions/quantity/) field.

{{< feature-state state="alpha" for_version="v0.6" >}}
{{% alert title="Warning" color="warning" %}}

`LendingLimit` is an Alpha feature disabled by default.

You can enable it by setting the `LendingLimit` feature gate. Check the [Installation](/docs/installation/#change-the-feature-gates-configuration) guide for details on feature gate configuration.
{{% /alert %}}

As an example, assume you created the following two ClusterQueues:

```yaml
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
    - name: "default-flavor"
      resources:
      - name: "cpu"
        nominalQuota: 9
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
  - coveredResources: ["cpu"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "cpu"
        nominalQuota: 12
        lendingLimit: 1
```

Here, you set lendingLimit=1 in ClusterQueue `team-b-cq`. It means that
if all admitted workloads in the ClusterQueue `team-b-cq` have their total
quota usage below the `nominalQuota` (less or equal `12-1=11` CPUs),
then ClusterQueue `team-a-cq` can admit Workloads with resources
adding up to `9+1=10` CPUs.

If the `lendingLimit` field is not specified, a ClusterQueue can lend out
all of its resources. In this case, `team-b-cq` can use up to `9+12` CPUs.

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
    borrowWithinCohort:
      policy: LowerPriority
      maxPriorityThreshold: 100
    withinClusterQueue: LowerPriority
```

The fields above do the following:

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

- `borrowWithinCohort` determines whether a pending Workload can preempt
  Workloads from other ClusterQueues if the workload requires borrowing. This
  field requires to specify `policy` sub-field with possible values:
  - `Never` (default): do not preempt Workloads in the cohort if borrowing is required.
  - `LowerPriority`: if the pending Workload requires borrowing, only preempt
    Workloads in the cohort that have lower priority than the pending Workload.
  This preemption policy is only supported when `reclaimWithinCohort` is enabled (different than `Never`).
  Additionally, only workloads up to the priority indicated by
  `maxPriorityThreshold` can be preempted in that scenario.

- `withinClusterQueue` determines whether a pending Workload that doesn't fit
  within the nominal quota for its ClusterQueue, can preempt active Workloads in
  the ClusterQueue. The possible values are:
  - `Never` (default): do not preempt Workloads in the ClusterQueue.
  - `LowerPriority`: only preempt Workloads in the ClusterQueue that have
    lower priority than the pending Workload.
  - `LowerOrNewerEqualPriority`: only preempt Workloads in the ClusterQueue that either have a lower priority than the pending workload or equal priority and are newer than the pending workload.

Note that an incoming Workload can preempt Workloads both within the
ClusterQueue and the cohort.

Read [Preemption](/docs/concepts/preemption) to learn more about
the heuristics that Kueue implements to preempt as few Workloads as possible.

## FlavorFungibility

When there is not enough nominal quota of resources in a ResourceFlavor, the incoming Workload can borrow
quota or preempt running Workloads in the ClusterQueue or Cohort.

Kueue evaluates the flavors in a ClusterQueue in order. You can influence whether to prioritize
preemptions or borrowing in a flavor before trying to accommodate the Workload in the next flavor, by
setting the `flavorFungibility` field.

A configuration for a ClusterQueue that configures this behavior looks like the following:

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "team-a-cq"
spec:
  flavorFungibility:
    whenCanBorrow: TryNextFlavor
    whenCanPreempt: Preempt
```

The fields above do the following:

- `whenCanBorrow` determines whether a workload should stop finding a better assignment if it can get enough resource by borrowing in current ResourceFlavor. The possible values are:
  - `Borrow` (default): ClusterQueue stops finding a better assignment.
  - `TryNextFlavor`: ClusterQueue tries the next ResourceFlavor to see if the workload can get a better assignment.
- `whenCanPreempt` determines whether a workload should try preemption in current ResourceFlavor before try the next one. The possible values are:
  - `Preempt`: ClusterQueue stops trying preemption in current ResourceFlavor and starts from the next one if preempting failed.
  - `TryNextFlavor` (default): ClusterQueue tries the next ResourceFlavor to see if the workload can fit in the ResourceFlavor.

By default, the incoming workload stops trying the next flavor if the workload can get enough borrowed resources.
And Kueue triggers preemption only after Kueue determines that the remaining ResourceFlavors can't fit the workload.

Note that, whenever possible and when the configured policy allows it, Kueue avoids preemptions if it can fit a Workload by borrowing.

## StopPolicy

StopPolicy allows a cluster administrator to temporary stop the admission of workloads within a ClusterQueue by setting its value in the [spec](/docs/reference/kueue.v1beta1/#kueue-x-k8s-io-v1beta1-ClusterQueueSpec) like:

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "team-a-cq"
spec:
  stopPolicy: Hold
```

The example above will stop the admission of new workloads in the ClusterQueue while allowing the already admitted workloads to finish.
The `HoldAndDrain` will have a similar effect but, in addition, it will trigger the eviction of the admitted workloads.

If set to `None` or `spec.stopPolicy` is removed the ClusterQueue will to normal admission behavior.

## AdmissionChecks

AdmissionChecks are a mechanism that allows Kueue to consider additional criteria before admitting a Workload.

For an example ClusterQueue configuration using admission checks, see [Admission Checks](/docs/concepts/admission_check#usage).

## What's next?

- Create [local queues](/docs/concepts/local_queue)
- Create [resource flavors](/docs/concepts/resource_flavor) if you haven't already done so.
- Learn how to [administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas).
- Read the [API reference](/docs/reference/kueue.v1beta1/#kueue-x-k8s-io-v1beta1-ClusterQueue) for `ClusterQueue`
