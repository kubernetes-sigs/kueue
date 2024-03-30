---
title: "Run A CronJob"
linkTitle: "Kubernetes CronJobs"
date: 2023-12-12
weight: 5
description: >
  Run a CronJob in a Kubernetes cluster with Kueue enabled.
---

This page shows you how to run a CronJob in a Kubernetes cluster with Kueue enabled.

The intended audience for this page are [batch users](/docs/tasks#batch-user).

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster is running.
- The kubectl command-line tool has communication with your cluster.
- [Kueue is installed](/docs/installation).
- The cluster has [quotas configured](/docs/tasks/administer_cluster_quotas).

## 0. Identify the queues available in your namespace

Run the following command to list the `LocalQueues` available in your namespace.

```shell
kubectl -n default get localqueues
# Or use the 'queues' alias.
kubectl -n default get queues
```

The output is similar to the following:

```bash
NAME         CLUSTERQUEUE    PENDING WORKLOADS
user-queue   cluster-queue   3
```

The [ClusterQueue](/docs/concepts/cluster_queue) defines the quotas for the
Queue.

## 1. Define the Job

Running a CronJob in Kueue is similar to [running a CronJob in a Kubernetes cluster](https://kubernetes.io/docs/tasks/job/automated-tasks-with-cron-jobs/)
without Kueue. However, you must consider the following differences:

- You should set the JobTemplate in CronJob in a [suspended state](https://kubernetes.io/docs/concepts/workloads/controllers/job/#suspending-a-job),
  as Kueue will decide when it's the best time to start the Job.
- You have to set the Queue you want to submit the Job to. Use the
 `kueue.x-k8s.io/queue-name` label in `jobTemplate.metadata`
- You should include the resource requests for each Job Pod.
- You should set the [`spec.concurrencyPolicy`](https://kubernetes.io/docs/concepts/workloads/controllers/cron-jobs/#concurrency-policy) to control the concurrency policy. The default is `Allow`. You can also set it to `Forbid` to prevent concurrent runs.
- You should set the [`spec.startingDeadlineSeconds`](https://kubernetes.io/docs/concepts/workloads/controllers/cron-jobs/#starting-deadline) to control the deadline for starting the Job. The default is no deadline.

Here is a sample CronJob with three Pods that just sleep for 10 seconds. The CronJob runs every minute.

{{< include "examples/jobs/sample-cronjob.yaml" "yaml" >}}

## 2. Run the CronJob

You can run the CronJob with the following command:

```shell
kubectl create -f sample-cronjob.yaml
```

Internally, Kueue will create a corresponding [Workload](/docs/concepts/workload)
for each run of the Job with a matching name.

```shell
kubectl -n default get workloads
```

The output will be similar to the following:

```shell
NAME                                QUEUE        ADMITTED BY     AGE
job-sample-cronjob-28373362-0133d   user-queue   cluster-queue   69m
job-sample-cronjob-28373363-e2aa0   user-queue   cluster-queue   68m
job-sample-cronjob-28373364-b42ac   user-queue   cluster-queue   67m
```

You can also [Monitoring Status of the Workload](/docs/tasks/run_jobs#3-optional-monitor-the-status-of-the-workload).