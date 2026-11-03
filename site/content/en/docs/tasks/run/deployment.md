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

1. The `deployment` integration is enabled by default.

2. For Kueue v0.15 and earlier, learn how to [install Kueue with a custom manager configuration](/docs/installation/#install-a-custom-configured-released-version),
   and ensure that you have the `deployment` integration enabled, for example:
   ```yaml
   apiVersion: config.kueue.x-k8s.io/v1beta2
   kind: Configuration
   integrations:
     frameworks:
      - "deployment"
   ```
   {{% alert title="Pod integration requirements" color="primary" %}}
   Since Kueue v0.15, you don't need to explicitly enable `"pod"` integration to use the `"deployment"` integration.

   For Kueue v0.14 and earlier, `"pod"` integration must be explicitly enabled.

   See [Run Plain Pods](/docs/tasks/run/plain_pods/#before-you-begin) for configuration details.
   {{% /alert %}}

3. Check [Administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas) for details on the initial Kueue setup.

## Running a Deployment admitted by Kueue

When running Deployment on Kueue, take into consideration the following aspects:

### a. Queue selection

The target [local queue](/docs/concepts/local_queue) should be specified in the `metadata.labels` section of the Deployment configuration.

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

You can also set a [WorkloadPriorityClass](/docs/concepts/workload_priority_class) with the `kueue.x-k8s.io/priority-class` label.
The label can be added to a Deployment that is already running, and it can be changed later, but it cannot be removed.
Adding the label starts a rolling update, and only the new Pods are admitted with that priority.
The Pods that are already running keep their priority.
Kueue does not apply the default WorkloadPriorityClass when you update a running Deployment that does not have the label.

While the new Pods wait for admission, the Deployment controller can terminate old Pods, up to the `maxUnavailable` limit of the rolling update, which is 25% by default.
To keep the full capacity during the rollout, set `maxUnavailable: 0`.
With `maxUnavailable: 0`, the Deployment controller keeps the old Pods until the new Pods are ready.
Kueue admits each new Pod separately, and the old Pods still hold their quota, so the new Pods need extra quota.
The ClusterQueue must be able to admit them from unused quota, from cohort borrowing, or from preemption when it is enabled.
Without that quota, the new Pods stay pending and the rollout stalls.

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
kubectl create -f https://kueue.sigs.k8s.io/examples/serving-workloads/sample-deployment.yaml
```
