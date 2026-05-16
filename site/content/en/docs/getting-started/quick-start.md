---
title: "Quick Start"
linkTitle: "Quick Start"
weight: 2
description: >
  Run your first Job with Kueue in minutes.
---

This page walks you through running a Kubernetes batch Job managed by Kueue.
You will create the Kueue infrastructure, submit a Job, and observe its progress using `kubectl`.

## Before you begin

Make sure the following conditions are met:

- A running Kubernetes cluster. For quick start, we recommend [KIND](https://kind.sigs.k8s.io/docs/user/quick-start/).
- The kubectl command-line tool has communication with your cluster.
- [Kueue is installed](/docs/getting-started/installation).

## 1. Create the queue infrastructure

Kueue uses three resources to manage job queuing:

- **ResourceFlavor**: describes the hardware profile available to jobs.
- **ClusterQueue**: defines the quota (CPU, memory, etc.) for a pool of resources.
- **LocalQueue**: a namespace-scoped queue that users submit jobs to; it maps to a ClusterQueue.

Apply the following manifest to create all three at once:

{{< include "examples/getting-started/quick-start-setup.yaml" "yaml" >}}

```shell
kubectl apply -f https://kueue.sigs.k8s.io/examples/getting-started/quick-start-setup.yaml
```

Verify the ClusterQueue is active:

```shell
kubectl get clusterqueue cluster-queue -o wide
```

The output is similar to the following:

```
NAME            COHORT   STRATEGY         PENDING WORKLOADS   ADMITTED WORKLOADS
cluster-queue            BestEffortFIFO   0                   0
```

Verify the LocalQueue is ready in the `default` namespace:

```shell
kubectl get localqueue user-queue -n default
```

The output is similar to the following:

```
NAME         CLUSTERQUEUE    PENDING WORKLOADS   ADMITTED WORKLOADS
user-queue   cluster-queue   0                   0
```

## 2. Submit a Job

The Job below runs 3 parallel pods, each sleeping for 30 seconds.
The `kueue.x-k8s.io/queue-name` label tells Kueue which LocalQueue to use.

{{< include "examples/getting-started/quick-start-job.yaml" "yaml" >}}

Apply the Job:

```shell
kubectl apply -f https://kueue.sigs.k8s.io/examples/getting-started/quick-start-job.yaml
```

## 3. Observe the Job

Kueue creates a [Workload](/docs/concepts/workload) object for every managed Job.
Check the Workload status to see whether the Job has been admitted:

```shell
kubectl get workloads -n default
```

The output is similar to the following:

```
NAME          QUEUE        RESERVED IN     ADMITTED   FINISHED   AGE
sample-job    user-queue   cluster-queue   True                  10s
```

Once admitted, the Job's pods start running. Check their status:

```shell
kubectl get pods -n default
```

The output is similar to the following:

```
NAME                    READY   STATUS    RESTARTS   AGE
sample-job-<id>         1/1     Running   0          5s
sample-job-<id>         1/1     Running   0          5s
sample-job-<id>         1/1     Running   0          5s
```

Check the Job status:

```shell
kubectl get jobs -n default
```

The output is similar to the following:

```
NAME          COMPLETIONS   DURATION   AGE
sample-job    0/3           15s        20s
```

After the pods finish, the Job completes:

```shell
kubectl get jobs -n default
```

```
NAME          COMPLETIONS   DURATION   AGE
sample-job    3/3           35s        50s
```

The corresponding Workload is also marked as finished:

```shell
kubectl get workloads -n default
```

```
NAME                   QUEUE        RESERVED IN     ADMITTED   FINISHED   AGE
job-sample-job-<id>   user-queue   cluster-queue   True       True       46s
```

## 4. Clean up

Remove the Job, queues, and resource flavor:

```shell
kubectl delete job sample-job -n default
kubectl delete localqueue user-queue -n default
kubectl delete clusterqueue cluster-queue
kubectl delete resourceflavor default-flavor
```

## What's next

- Learn about [Kueue concepts](/docs/concepts) such as ClusterQueues, Workloads, and preemption.
- Configure [resource quotas and fair sharing](/docs/tasks/manage/administer_cluster_quotas) for multiple teams.
- Run other workload types: [Kubeflow jobs](/docs/tasks/run/kubeflow/), [RayJobs](/docs/tasks/run/rayjobs/), [plain Pods](/docs/tasks/run/plain_pods/).
