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
    template:
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

### Resource requests

Kueue uses the `podSets` resources requests to calculate the quota used by a Workload and decide if and when to admit a Workload.

Kueue calculates the total resources usage for a Workload as the sum of the resource requests for each `podSet`. The resource usage of a `podSet` is equal to the resource requests of the pod spec multiplied by the `count`.

#### Requests values adjustment

Depending on the cluster setup, Kueue will adjust the resource usage of a Workload based on:

- The cluster defines default values in [Limit Ranges](https://kubernetes.io/docs/concepts/policy/limit-range/), the default values will be used if not provided in the `spec`.
- The created pods are subject of a [Runtime Class Overhead](https://kubernetes.io/docs/concepts/scheduling-eviction/pod-overhead/).
- The spec defines only resource limits, case in which the limit values will be treated as requests.

#### Requests values validation

In cases when the cluster defines Limit Ranges, the values resulting from the adjustment above will be validated against the ranges.
Kueue will mark the workload as `Inadmissible` if the range validation fails.

#### Reserved resource names

In addition to the usual resource naming restrictions, you cannot use the `pods` resource name in a Pod spec, as it is reserved for internal Kueue use. You can use the `pods` resource name in a [ClusterQueue](/docs/concepts/cluster_queue#resources) to set quotas on the maximum number of pods. 

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
