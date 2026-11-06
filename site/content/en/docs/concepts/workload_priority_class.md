---
title: "Workload Priority Class"
date: 2023-10-02
weight: 6
description: >
  A priority class whose value is utilized by Kueue controller and is independent from Pod's priority.
---

A `WorkloadPriorityClass` allows you to control the [`Workload`'s](/docs/concepts/workload) priority without affecting the pod's priority.
This feature is useful for these cases:
- want to prioritize workloads that remain inactive for a specific duration
- want to set a lower priority for development workloads and higher priority for production workloads

A sample WorkloadPriorityClass looks like the following:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: WorkloadPriorityClass
metadata:
  name: sample-priority
value: 10000
description: "Sample priority"
```

`WorkloadPriorityClass` objects are cluster scoped, so they can be used by a job in any namespace.

## How to use WorkloadPriorityClass on Jobs

You can specify the `WorkloadPriorityClass` by setting the label `kueue.x-k8s.io/priority-class`.

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: sample-job
  labels:
    kueue.x-k8s.io/queue-name: user-queue
    kueue.x-k8s.io/priority-class: sample-priority
spec:
...
```

Kueue generates the following `Workload` for the Job above.
The `priorityClassRef` field references either a `PriorityClass` or a `WorkloadPriorityClass`.
To distinguish, when using `WorkloadPriorityClass`, `priorityClassRef.group` is
`kueue.x-k8s.io` and `priorityClassRef.kind` is `WorkloadPriorityClass`.
When using `PriorityClass`, `priorityClassRef.group` is `scheduling.k8s.io` and
`priorityClassRef.kind` is `PriorityClass`.

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: Workload
metadata:
  name: job-sample-job-7f173
spec:
  priorityClassRef:
    group: kueue.x-k8s.io
    kind: WorkloadPriorityClass
    name: sample-priority
  priority: 10000
  queueName: user-queue
...
```

For other job frameworks, you can set `WorkloadPriorityClass` using the same label.
The Following is an example of `MPIJob`.

```yaml
apiVersion: kubeflow.org/v2beta1
kind: MPIJob
metadata:
  name: pi
  labels:
    kueue.x-k8s.io/queue-name: user-queue
    kueue.x-k8s.io/priority-class: sample-priority
spec:
...
```

## Referencing a WorkloadPriorityClass

The `WorkloadPriorityClass` referenced by the `kueue.x-k8s.io/priority-class` label must exist in the cluster.

If the label explicitly references a nonexistent `WorkloadPriorityClass`, Kueue cannot resolve the workload priority and does not fall back to the Pod `PriorityClass`, the global default `PriorityClass`, or the default priority value. Kueue therefore cannot create the corresponding `Workload`, and cannot point an existing one at the named class, until the reference is corrected or the referenced `WorkloadPriorityClass` is created.

Whenever Kueue resolves the label, either to create a `Workload` or to update one whose priority already comes from a `WorkloadPriorityClass`, it reports a missing class as a `Warning` event with the reason `WorkloadPriorityClassNotFound` on the object carrying the label, so `kubectl describe` on that object names the class. Creation or the update succeeds on a later reconciliation once the class exists.

This differs from omitting the `kueue.x-k8s.io/priority-class` label. When the label is omitted, Kueue determines the workload priority from the applicable Pod `PriorityClass`, the global default `PriorityClass`, or the default priority value. When the alpha `WorkloadPriorityClassDefaulting` feature is enabled and a `WorkloadPriorityClass` named `default` exists, an omitted label is first defaulted to that class; see [Setup default WorkloadPriorityClass](/docs/tasks/manage/enforce_job_management/setup_default_workload_priority_class).

## The relationship between pod's priority and workload's priority

When creating a `Workload` for a given job, Kueue considers the following scenarios:
1. A job specifies both `WorkloadPriorityClass` and `PriorityClass`
- `WorkloadPriorityClass` is used for the workload's priority.
- `PriorityClass` is used for the pod's priority.
2. A job specifies only `WorkloadPriorityClass`
- `WorkloadPriorityClass` is used for the workload's priority.
- `WorkloadPriorityClass` is not used for pod's priority.
3. A job specifies only `PriorityClass`
- `PriorityClass` is used for the workload's priority and pod's priority.

In certain job frameworks, there are CRDs that:
- Define multiple pod specs, where each can have their own pod priority, or
- Define the overall pod priority in a dedicated field.
By default kueue will take the PriorityClassName of the first PodSet having one set,
however the integration of the CRD with Kueue can implement
[`JobWithPriorityClass interface`](https://github.com/kubernetes-sigs/kueue/blob/e162f8508b503d20feb9b31fd0b27d91e58f2c2f/pkg/controller/jobframework/interface.go#L81-L84)
to change this behavior. You can read the code for each job integration
to learn how the priority class is obtained.

## Referencing a PriorityClass

Wherever the workload's priority comes from a `PriorityClass`, that `PriorityClass` must
exist in the cluster. Kueue cannot resolve a priority from a name that does not exist, so
it does not create the `Workload`, and the job it would have queued stays suspended.

Kueue reports this as a `Warning` event with the reason `PriorityClassNotFound` on the
object that named the class, so `kubectl describe` on that object names it. The `Workload`
is created on a later reconciliation once the `PriorityClass` exists.

Kubernetes refuses a Pod that names a nonexistent `PriorityClass` and reports that on the
Pod's owner, but a job Kueue has suspended creates no Pod, so that error never appears. A
CRD that defines the overall pod priority in a dedicated field does not reach a Pod spec
at all: `MPIJob`, for example, resolves `.spec.runPolicy.schedulingPolicy.priorityClass`
ahead of its launcher and worker templates, so the name Kueue looks up there need not
appear on any Pod.

## Where workload's priority is used

The priority of workloads is used for:
- Sorting the workloads in the ClusterQueues.
- Determining whether a workload can preempt others.
- Ordering workloads that need to borrow quota within the same [cohort](/docs/concepts/cluster_queue/#flavors-and-borrowing-semantics).
  By default, higher-priority workloads are scheduled first; this can be disabled
  by setting the `PrioritySortingWithinCohort` feature gate to `false`, in which case
  Kueue falls back to ordering by `.metadata.creationTimestamp`.

## Mutability of priority fields

The `kueue.x-k8s.io/priority-class` label on the Job can be changed while the Job is suspended.
When updated, Kueue reconciles the Workload's priority fields accordingly.

On the Workload, `Priority` is always mutable.
`priorityClassRef` (and its `group`/`kind`) is mutable while the Workload is pending,
and becomes immutable once the `QuotaReserved` condition is `True`.
`priorityClassRef.name` follows the same rule, except when `priorityClassRef.kind` is
`WorkloadPriorityClass`. In that case, `.priorityClassRef.name` can still be updated
after the `QuotaReserved` condition is `True`.

## What's next?

- Learn how to [run jobs](/docs/tasks/run/jobs)
- Learn how to [run jobs with workload priority](/docs/tasks/manage/run_job_with_workload_priority)
- Learn how to [setup a default WorkloadPriorityClass](/docs/tasks/manage/enforce_job_management/setup_default_workload_priority_class)
- Read the [API reference](/docs/reference/kueue.v1beta2/#kueue-x-k8s-io-v1beta2-WorkloadPriorityClass) for `WorkloadPriorityClass`
