---
title: "Run A Kubernetes Job"
linkTitle: "Kubernetes Jobs"
date: 2022-02-14
weight: 4
description: >
  Run a Job in a Kubernetes cluster with Kueue enabled.
---

This page shows you how to run a Job in a Kubernetes cluster with Kueue enabled.

The intended audience for this page are [batch users](/docs/tasks#batch-user).

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster is running.
- The kubectl command-line tool has communication with your cluster.
- [Kueue is installed](/docs/installation).
- The cluster has [quotas configured](/docs/tasks/manage/administer_cluster_quotas).

The following picture shows all the concepts you will interact with in this tutorial:

![Kueue Components](/images/queueing-components.svg)

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

Running a Job in Kueue is similar to [running a Job in a Kubernetes cluster](https://kubernetes.io/docs/tasks/job/)
without Kueue. However, you must consider the following differences:

- You should create the Job in a [suspended state](https://kubernetes.io/docs/concepts/workloads/controllers/job/#suspending-a-job),
  as Kueue will decide when it's the best time to start the Job.
- You have to set the Queue you want to submit the Job to. Use the
 `kueue.x-k8s.io/queue-name` label.
- You should include the resource requests for each Job Pod.

Here is a sample Job with three Pods that just sleep for a few seconds.

{{< include "examples/jobs/sample-job.yaml" "yaml" >}}

## 2. Run the Job

You can run the Job with the following command:

```shell
kubectl create -f sample-job.yaml
```

Internally, Kueue will create a corresponding [Workload](/docs/concepts/workload)
for this Job with a matching name.

```shell
kubectl -n default get workloads
```

The output will be similar to the following:

```shell
NAME               QUEUE         RESERVED IN   ADMITTED   AGE
sample-job-sl4bm   user-queue                             1s
```

## 3. (Optional) Monitor the status of the workload

You can see the Workload status with the following command:

```shell
kubectl -n default describe workload sample-job-sl4bm
```

If the ClusterQueue doesn't have enough quota to run the Workload, the output
will be similar to the following:

```shell
Name:         sample-job-sl4bm
Namespace:    default
Labels:       <none>
Annotations:  <none>
API Version:  kueue.x-k8s.io/v1beta1
Kind:         Workload
Metadata:
  ...
Spec:
  ...
Status:
  Conditions:
    Last Probe Time:       2022-03-28T19:43:03Z
    Last Transition Time:  2022-03-28T19:43:03Z
    Message:               workload didn't fit
    Reason:                Pending
    Status:                False
    Type:                  Admitted
Events:               <none>
```

When the ClusterQueue has enough quota to run the Workload, it will admit
the Workload. To see if the Workload was admitted, run the following command:

```shell
kubectl -n default get workloads
```

The output is similar to the following:

```shell
NAME               QUEUE         RESERVED IN   ADMITTED   AGE
sample-job-sl4bm   user-queue    cluster-queue True       1s
```

To view the event for the Workload admission, run the following command:

```shell
kubectl -n default describe workload sample-job-sl4bm
```

The output is similar to the following:

```shell
...
Events:
  Type    Reason    Age   From           Message
  ----    ------    ----  ----           -------
  Normal  Admitted  50s   kueue-manager  Admitted by ClusterQueue cluster-queue
```

To continue monitoring the Workload progress, you can run the following command:

```shell
kubectl -n default describe workload sample-job-sl4bm
```

Once the Workload has finished running, the output is similar to the following:

```shell
...
Status:
  Conditions:
    ...
    Last Probe Time:       2022-03-28T19:43:37Z                                                                                                                      
    Last Transition Time:  2022-03-28T19:43:37Z                                                                                                                      
    Message:               Job finished successfully                                                                                                                 
    Reason:                JobFinished                                                                                                                               
    Status:                True                                                                                                                                      
    Type:                  Finished
...
```

To review more details about the Job status, run the following command:

```shell
kubectl -n default describe job sample-job-sl4bm
```

The output is similar to the following:

```shell
Name:             sample-job-sl4bm
Namespace:        default
...
Start Time:       Mon, 28 Mar 2022 15:45:17 -0400
Completed At:     Mon, 28 Mar 2022 15:45:49 -0400
Duration:         32s
Pods Statuses:    0 Active / 3 Succeeded / 0 Failed
Pod Template:
  ...
Events:
  Type    Reason            Age   From                  Message
  ----    ------            ----  ----                  -------
  Normal  Suspended         22m   job-controller        Job suspended
  Normal  CreatedWorkload   22m   kueue-job-controller  Created Workload: default/sample-job-sl4bm
  Normal  SuccessfulCreate  19m   job-controller        Created pod: sample-job-sl4bm-7bqld
  Normal  Started           19m   kueue-job-controller  Admitted by clusterQueue cluster-queue
  Normal  SuccessfulCreate  19m   job-controller        Created pod: sample-job-sl4bm-7jw4z
  Normal  SuccessfulCreate  19m   job-controller        Created pod: sample-job-sl4bm-m7wgm
  Normal  Resumed           19m   job-controller        Job resumed
  Normal  Completed         18m   job-controller        Job completed
```

Since events have a timestamp with a resolution of seconds, the events might
be listed in a slightly different order from which they actually occurred.

## Partial admission

From version v0.4.0, Kueue provides the ability for a batch user to create Jobs that ideally will run with a parallelism `P0` but can accept a smaller parallelism, `Pn`, if the Job dose not fit within the available quota.

Kueue will only attempt to decrease the parallelism after both _borrowing_ and _preemption_ was taken into account in the admission process, and none of them are feasible.

To allow partial admission you can provide the minimum acceptable parallelism `Pmin` in `kueue.x-k8s.io/job-min-parallelism` annotation of the Job, `Pn` should be grater that 0 and less that `P0`. When a Job is partially admitted its parallelism will be set to `Pn`, `Pn` will be set to the maximum acceptable value between `Pmin` and `P0`. The Job's completions count will not be changed.

For example, a Job defined by the following manifest:

{{< include "examples/jobs/sample-job-partial-admission.yaml" "yaml" >}}

When queued in a ClusterQueue with only 9 CPUs available, it will be admitted with `parallelism=9`. Note that the number of completions doesn't change.

{{% alert title="Note" color="primary" %}}
PartialAdmission is an `Alpha` feature disabled by default, check the [Change the feature gates configuration](/docs/installation/#change-the-feature-gates-configuration) section of the [Installation](/docs/installation/) for details.
{{% /alert %}}