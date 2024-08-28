---
title: "Troubleshooting delete ClusterQueue"
date: 2024-08-28
weight: 5
---

Deleting a ClusterQueue might require additional steps when it has active Workloads.

This guide provides a clear approach to safely deleting a ClusterQueue,
explaining the process and offering alternative methods to handle specific scenarios.

For this example, assume your ClusterQueue is named `my-cq`.

## Understanding `kubectl delete clusterqueue`

When you run the following command:

```sh
kubectl delete clusterqueue my-cq
```

you are initiating the deletion of the ClusterQueue object. 
However, if there are Workloads still utilizing the ClusterQueue, the command may hang or take some time to complete.


## Why does this happen?

Kueue creates a [Workload](/docs/concepts/workload/) object for each Job to track its admission status. 
If a Workload is admitted, it is associated with a specific ClusterQueue. 
Kueue uses a [finalizer](https://kubernetes.io/docs/concepts/overview/working-with-objects/finalizers/) 
called `kueue.x-k8s.io/resource-in-use` to prevent deletion of the ClusterQueue while resources are still in use. 
Consequently, a ClusterQueue with this finalizer cannot be deleted until all resources are released.


## How to identify the Workloads related to the ClusterQueue?

To find Workloads linked to a specific ClusterQueue, you can use the following steps:

### Using `kueuectl`

{{% alert title="Note" color="primary" %}}
If you don't have `kueuectl` installed, follow the [installation guide](/docs/reference/kubectl-kueue/installation/).
{{% /alert %}}

```shell
kueuectl list workload --clusterqueue my-cq -A
```

Example output:

```shell
NAMESPACE   NAME                     JOB TYPE   JOB NAME       LOCALQUEUE   CLUSTERQUEUE    STATUS     POSITION IN QUEUE   AGE
default     my-job-job-4gk8s-7b737   job        my-job-4gk8s   my-lq        my-cq           ADMITTED                       27m
default     my-job-job-826hp-caa72   job        my-job-826hp   my-lq        my-cq           ADMITTED                       1h
```

### Using `kubectl` and `grep`

Alternatively, you can use `kubectl` with `grep`:

```shell
kueuectl get workload -A | grep my-cq
```

Example output:

```shell
default     my-job-4gk8s-7b737   my-lq   my-cq   True                  30m
default     my-job-826hp-caa72   my-lq   my-cq   True                  30m
```

{{% alert title="Note" color="primary" %}}
The grep command only filters lines based on the specified pattern. 
It might return additional Workloads if the Workload name or LocalQueue name includes the same name as the ClusterQueue.
{{% /alert %}}

## How to stop the ClusterQueue?

{{% alert title="Note" color="primary" %}}
If no Workloads are found for the specified ClusterQueue, you can skip the stopping step and proceed directly to deleting the ClusterQueue.
{{% /alert %}}

### Using `kueuectl`

To stop all the jobs in a ClusterQueue and prevent new jobs from being admitted by it, run the following command:

```shell
kueuectl stop clusterqueue my-cq
```


### Using `kubectl edit`

Alternatively, you can stop the ClusterQueue by editing its configuration:

```shell
kubectl edit clusterqueue my-cq
```

In the editor, change the `stopPolicy` value to `HoldAndDrain`:

```yaml
spec:
   stopPolicy: HoldAndDrain
```

Save the changes. This will stop all workloads associated with the ClusterQueue.


## How to delete the ClusterQueue?

Once the Workloads are stopped, you can delete the ClusterQueue:

```shell
kueuectl delete clusterqueue my-cq
```
