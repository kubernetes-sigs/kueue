---
title: "Workload"
date: 2022-02-14
weight: 5
description: >
  An application that will run to completion. It is the unit of admission in Kueue. Sometimes referred to as job.
---

A _workload_ is an application that will run to completion. It can be composed
by one or multiple Pods that, loosely or tightly coupled, that, as a whole,
complete a task. A workload is the unit of [admission](/docs/concepts#admission) in Kueue.

The prototypical workload can be represented with a
[Kubernetes `batch/v1.Job`](https://kubernetes.io/docs/concepts/workloads/controllers/job/).
For this reason, we sometimes use the word _job_ to refer to any workload, and
Job when we refer specifically to the Kubernetes API.

However, Kueue does not directly manipulate Job objects. Instead, Kueue manages
Workload objects that represent the resource requirements of an arbitrary
workload. Kueue automatically creates a Workload for each Job object and syncs
the decisions and statuses.

The manifest for a Workload looks like the following:

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: Workload
metadata:
  name: sample-job
  namespace: team-a
spec:
  queueName: team-a-queue
  podSets:
  - count: 3
    name: main
    spec:
      containers:
      - image: gcr.io/k8s-staging-perf-tests/sleep:latest
        imagePullPolicy: Always
        name: container
        resources:
          requests:
            cpu: "1"
            memory: 200Mi
      restartPolicy: Never
```

## Queue name

To indicate in which [LocalQueue](/docs/concepts/local_queue) you want your Workload to be
enqueued, set the name of the LocalQueue in the `.spec.queueName` field.

## Pod sets

A Workload might be composed of multiple Pods with different pod specs.

Each item of the `.spec.podSets` list represents a set of homogeneous Pods and has
the following fields:

- `spec` describes the pods using a [`v1/core.PodSpec`](https://kubernetes.io/docs/reference/kubernetes-api/workload-resources/pod-v1/#PodSpec).
- `count` is the number of pods that use the same `spec`.
- `name` is a human-readable identifier for the pod set. You can use the role of
  the Pods in the Workload, like `driver`, `worker`, `parameter-server`, etc.

## Priority

Workloads have a priority that influences the [order in which they are admitted by a ClusterQueue](/docs/concepts/cluster_queue#queueing-strategy).
You can see the priority of the Workload in the field `.spec.priority`.

For a `batch/v1.Job`, Kueue sets the priority of the Workload based on the
[pod priority](https://kubernetes.io/docs/concepts/scheduling-eviction/pod-priority-preemption/)
of the Job's pod template.

## Custom Workloads

As described previously, Kueue has built-in support for workloads created with
the Job API. But any custom workload API can integrate with Kueue by
creating a corresponding Workload object for it.

## What's next

- Learn how to [run jobs](/docs/tasks/run_jobs)
