---
title: "Run job with WorkloadPriority"
date: 2023-10-02
weight: 4
description: >
  Run job with WorkloadPriority, which is independent from Pod's priority
---

Usually, in Kueue, workload's priority is calculated using for pod's priority for queuing and preemption.  
By using a [`WorkloadPriorityClass`](/docs/concepts/workload_priority_class),
you can independently manage the priority of workloads for queuing and preemption, separate from pod's priority.  

This page contains instructions on how to run a job with workload priority.

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster is running.
- The kubectl command-line tool has communication with your cluster.
- [Kueue is installed](/docs/installation).

## 0. Create WorkloadPriorityClass

The WorkloadPriorityClass should be created first.

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: WorkloadPriorityClass
metadata:
  name: sample-priority
value: 10000
description: "Sample priority"
```

## 1. Create Job with `kueue.x-k8s.io/priority-class` label

You can specify the `WorkloadPriorityClass` by setting the label `kueue.x-k8s.io/priority-class`.
This is same for other CRDs like `RayJob`.  

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: sample-job
  labels:
    kueue.x-k8s.io/queue-name: user-queue
    kueue.x-k8s.io/priority-class: sample-priority
spec:
  parallelism: 3
  completions: 3
  suspend: true
  template:
    spec:
      containers:
      - name: dummy-job
        image: gcr.io/k8s-staging-perf-tests/sleep:latest
      restartPolicy: Never
```

Kueue generates the following `Workload` for the Job above.
The priority of workloads is utilized in queuing, preemption, and other scheduling processes in Kueue.
This priority doesn't affect pod's priority.  
Workload's `Priority` field is always mutable because it might be useful for the preemption.
Workload's `PriorityClassSource` and `PriorityClassName` fields are immutable.

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
  podSets:
  - count: 3
    name: dummy-job
    template:
      spec:
        containers:
        - image: gcr.io/k8s-staging-perf-tests/sleep:latest
          name: dummy-job
```
