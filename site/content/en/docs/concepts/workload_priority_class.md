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
apiVersion: kueue.x-k8s.io/v1beta1
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
The `PriorityClassName` field can accept either `PriorityClass` or
`WorkloadPriorityClass` name as a value. To distinguish, when using `WorkloadPriorityClass`,
a `priorityClassSource` field has the `kueue.x-k8s.io/workloadpriorityclass` value.
When using `PriorityClass`, a `priorityClassSource` field has the `scheduling.k8s.io/priorityclass` value.

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: Workload
metadata:
  name: job-sample-job-7f173
spec:
  priorityClassSource: kueue.x-k8s.io/workloadpriorityclass
  priorityClassName: sample-priority
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

## Where workload's priority is used

The priority of workloads is used for:
- Sorting the workloads in the ClusterQueues.
- Determining whether a workload can preempt others.

## Workload's priority values are always mutable

The `Workload`'s `Priority` field is always mutable.
If a `Workload` has been pending for a while, you can consider updating its priority to execute it earlier,
based on your own policies.
Workload's `PriorityClassSource` and `PriorityClassName` fields are immutable.

## What's next?

- Learn how to [run jobs](/docs/tasks/run/jobs)
- Learn how to [run jobs with workload priority](/docs/tasks/manage/run_job_with_workload_priority)
- Read the [API reference](/docs/reference/kueue.v1beta1/#kueue-x-k8s-io-v1beta1-WorkloadPriorityClass) for `WorkloadPriorityClass`
