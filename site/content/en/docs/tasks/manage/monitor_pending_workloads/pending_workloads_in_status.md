---
title: "Pending workloads in Status"
date: 2024-09-23
weight: 3
description: >
  Obtain the pending workloads in ClusterQueue and LocalQueue statuses.
---

This page shows you how to monitor pending workloads.

The intended audience for this page are [batch administrators](/docs/tasks#batch-administrator).

From version v0.5.0, Kueue provides the ability for a batch administrators to monitor
the pipeline of pending jobs, and help users to estimate when their jobs will
start.

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster is running.
- The kubectl command-line tool has communication with your cluster.
- [Kueue is installed](/docs/installation) in version v0.5.0 or later.

## Enabling feature QueueVisibility

QueueVisibility is an `Alpha` feature disabled by default, check the [Change the feature gates configuration](/docs/installation/#change-the-feature-gates-configuration) section of the [Installation](/docs/installation/) for details.

## Monitor pending workloads

{{< feature-state state="deprecated" for_version="v0.9" >}}

{{% alert title="Warning" color="warning" %}}
This feature is deprecated and will be deleted on v1beta2. 
Please use [Pending Workloads on-demand](/docs/tasks/manage/monitor_pending_workloads/pending_workloads_on_demand/) instead.
{{% /alert %}}

To install a simple setup of cluster queue, run the following command:

```shell
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/kueue/main/site/static/examples/admin/single-clusterqueue-setup.yaml
```

For example, let's create 10 jobs in a loop

```shell
for i in {1..10}; do kubectl create -f https://raw.githubusercontent.com/kubernetes-sigs/kueue/main/site/static/examples/jobs/sample-job.yaml; done
```

To view the pending workload status, run the following command:

```shell
kubectl describe clusterqueues.kueue.x-k8s.io
```

The output is similar to the following:

```shell
Status:
  ...
  Pending Workloads:  10
  Pending Workloads Status:
    Cluster Queue Pending Workload:
      Name:            job-sample-job-gswhv-afff9
      Namespace:       default
      Name:            job-sample-job-v8dwc-ab5b7
      Namespace:       default
      Name:            job-sample-job-lrxbj-a9a91
      Namespace:       default
      Name:            job-sample-job-dj6nb-e6ef8
      Namespace:       default
      Name:            job-sample-job-6hdgw-bb26e
      Namespace:       default
      Name:            job-sample-job-2d268-693a7
      Namespace:       default
      Name:            job-sample-job-bfkd7-5e739
      Namespace:       default
      Name:            job-sample-job-8sbgz-506c6
      Namespace:       default
      Name:            job-sample-job-k2bmq-44616
      Namespace:       default
      Name:            job-sample-job-c724j-50fd2
      Namespace:       default
    Last Change Time:  2023-09-28T09:22:12Z
```

To configure queue visibility, please, follow the instruction how to [install Kueue with a custom manager configuration](/docs/installation/#install-a-custom-configured-released-version).

The `queueVisibility.clusterQueues.maxCount` parameter indicates the maximal number of pending workloads exposed in the ClusterQueue status. 
By default, Kueue will set this parameter to 10. 
When the value is set to 0, then ClusterQueues visibility updates are disabled.

```yaml
    queueVisibility:
      clusterQueues: 
        maxCount: 0
```

The `queueVisibility.updateIntervalSeconds` parameter allows to control the period of snapshot updates after Kueue startup. 
Defaults to 5s. 
It also can be changed in Kueue configuration:

```yaml
    queueVisibility:
      updateIntervalSeconds: 5s
```
