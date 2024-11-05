---
title: "Run Deployment"
linkTitle: "Deployment"
date: 2024-07-25
weight: 6
description: >
  Run a Deployment as a Kueue-managed workload.
---

This page shows how to leverage Kueue's scheduling and resource management
capabilities when running Deployments.
Although Kueue does not yet support managing a Deployment as a single Workload, 
it's still possible to leverage Kueue's scheduling and resource management capabilities for the individual Pods of the Deployment.

We demonstrate how to support scheduling Deployments in Kueue based on the Plain Pod integration,
where every Pod from a Deployment is represented as a single independent Plain Pod.
This approach allows independent resource management for the Pods, and thus scale-out and scale-in of the Deployment.

This guide is for [serving users](/docs/tasks#serving-user) that have a basic understanding of Kueue.
For more information, see [Kueue's overview](/docs/overview).

## Before you begin

1. Learn how to [install Kueue with a custom manager configuration](/docs/installation/#install-a-custom-configured-released-version).

2. Follow steps in [Run Plain Pods](/docs/tasks/run/plain_pods/#before-you-begin)
to learn how to enable the `v1/pod` integration and how to configure it using the `podOptions` field.

3. Check [Administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas) for details on the initial Kueue setup.

## Running a Deployment admitted by Kueue

When running Deployment on Kueue, take into consideration the following aspects:

### a. Queue selection

The target [local queue](/docs/concepts/local_queue) should be specified in the `spec.template.metadata.labels` section of the Deployment configuration. 
Since Kueue's scheduling and resource management will be applied to the individual Pods of the Deployment,
the queue name should be specified at the Pod level.

```yaml
spec:
   template:
      metadata:
         labels:
            kueue.x-k8s.io/queue-name: user-queue
```

### b. Configure the resource needs

The resource needs of the workload can be configured in the `spec.template.spec.containers`.

```yaml
    - resources:
        requests:
          cpu: 3
```

### c. Scaling

You may perform scale up or scale down operations on Deployments.
On scale-in, the excess Pods are deleted, and the quota is freed.
On scale-out, new Pods are created, and remain suspended until their corresponding workloads get admitted.
If there is not enough quota in your cluster, the Deployment might run only a subset of Pods. 
So, if your workloads are business-critical, 
you can consider reserving the quota only for the serving workloads by the ClusterQueue `lendingLimit`. 
The `lendingLimit` allows you to rapidly scale out the critical serving workload.
For more `lendingLimit` details, please see the [ClusterQueue page](docs/concepts/cluster_queue#lendinglimit).

### d. Limitations

- The scope for Deployments is implied by the pod integration's namespace selector. There's no independent control for deployments.

## Example

Here is a sample Deployment:

{{< include "examples/serving-workloads/sample-deployment.yaml" "yaml" >}}

You can create the Deployment using the following command:
```sh
kubectl create -f sample-deployment.yaml
```
