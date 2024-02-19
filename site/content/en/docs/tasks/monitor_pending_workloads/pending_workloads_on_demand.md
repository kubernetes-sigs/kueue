---
title: "Pending Workloads on-demand"
date: 2023-12-05
weight: 3
description: >
  Pending Workloads on-demand
---

This page shows you how to monitor pending workloads with VisibilityOnDemand feature.

The intended audience for this page are [batch administrators](/docs/tasks#batch-administrator), and [batch users](/docs/tasks#batch-user) for [Local Queue Visibility section](#local-queue-visibility).

From version v0.6.0, Kueue provides the ability for a batch administrators to monitor
the pipeline of pending jobs, and help users to estimate when their jobs will
start.

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster is running.
- The kubectl command-line tool has communication with your cluster.
- [Kueue is installed](/docs/installation) in version v0.6.0 or later.

### Enabling feature VisibilityOnDemand

VisibilityOnDemand is an `Alpha` feature disabled by default, check the [Change the feature gates configuration](/docs/installation/#change-the-feature-gates-configuration) section of the [Installation](/docs/installation/) for details.

## Monitor pending workloads on demand

> _Available in Kueue v0.6.0 and later_

To install a simple setup of ClusterQueue

{{< include "examples/admin/single-clusterqueue-setup.yaml" "yaml" >}}

run the following command:

```shell
kubectl apply -f https://kueue.sigs.k8s.io/examples/admin/single-clusterqueue-setup.yaml
```

Now, let's create 10 jobs

{{< include "examples/jobs/sample-job.yaml" "yaml" >}}

using a command: 

```shell
for i in {1..6}; do kubectl create -f https://kueue.sigs.k8s.io/examples/jobs/sample-job.yaml; done
```

### Cluster Queue visibility

To view pending workloads in ClusterQueue `cluster-queue` run the following command:

```shell
kubectl get --raw "/apis/visibility.kueue.x-k8s.io/v1alpha1/clusterqueues/cluster-queue/pendingworkloads"
```

You should get results similar to:

```json
{
  "kind": "PendingWorkloadsSummary",
  "apiVersion": "visibility.kueue.x-k8s.io/v1alpha1",
  "metadata": {
    "creationTimestamp": null
  },
  "items": [
    {
      "metadata": {
        "name": "job-sample-job-jrjfr-8d56e",
        "namespace": "default",
        "creationTimestamp": "2023-12-05T15:42:03Z",
        "ownerReferences": [
          {
            "apiVersion": "batch/v1",
            "kind": "Job",
            "name": "sample-job-jrjfr",
            "uid": "5863cf0e-b0e7-43bf-a445-f41fa1abedfa"
          }
        ]
      },
      "priority": 0,
      "localQueueName": "user-queue",
      "positionInClusterQueue": 0,
      "positionInLocalQueue": 0
    },
    {
      "metadata": {
        "name": "job-sample-job-jg9dw-5f1a3",
        "namespace": "default",
        "creationTimestamp": "2023-12-05T15:42:03Z",
        "ownerReferences": [
          {
            "apiVersion": "batch/v1",
            "kind": "Job",
            "name": "sample-job-jg9dw",
            "uid": "fd5d1796-f61d-402f-a4c8-cbda646e2676"
          }
        ]
      },
      "priority": 0,
      "localQueueName": "user-queue",
      "positionInClusterQueue": 1,
      "positionInLocalQueue": 1
    },
    {
      "metadata": {
        "name": "job-sample-job-t9b8m-4e770",
        "namespace": "default",
        "creationTimestamp": "2023-12-05T15:42:03Z",
        "ownerReferences": [
          {
            "apiVersion": "batch/v1",
            "kind": "Job",
            "name": "sample-job-t9b8m",
            "uid": "64c26c73-6334-4d13-a1a8-38d99196baa5"
          }
        ]
      },
      "priority": 0,
      "localQueueName": "user-queue",
      "positionInClusterQueue": 2,
      "positionInLocalQueue": 2
    }
  ]
}
```

You can pass optional query parameters:
- limit `<integer>` - 1000 on default. It indicates max number of pending workloads that should be fetched.
- offset `<integer>` - 0 by default. It indicates position of the first pending workload that should be fetched, starting from 0.

To view only 1 pending workloads use, starting from position 1 in ClusterQueue run:

```shell
kubectl get --raw "/apis/visibility.kueue.x-k8s.io/v1alpha1/clusterqueues/cluster-queue/pendingworkloads?limit=1&offset=1"
```

You should get results similar to 

``` json
{
  "kind": "PendingWorkloadsSummary",
  "apiVersion": "visibility.kueue.x-k8s.io/v1alpha1",
  "metadata": {
    "creationTimestamp": null
  },
  "items": [
    {
      "metadata": {
        "name": "job-sample-job-jg9dw-5f1a3",
        "namespace": "default",
        "creationTimestamp": "2023-12-05T15:42:03Z",
        "ownerReferences": [
          {
            "apiVersion": "batch/v1",
            "kind": "Job",
            "name": "sample-job-jg9dw",
            "uid": "fd5d1796-f61d-402f-a4c8-cbda646e2676"
          }
        ]
      },
      "priority": 0,
      "localQueueName": "user-queue",
      "positionInClusterQueue": 1,
      "positionInLocalQueue": 1
    }
  ]
}
```

### Local Queue visibility

Similarly to ClusterQueue, to view pending workloads in LocalQueue `user-queue` run the following command:

```shell
kubectl get --raw /apis/visibility.kueue.x-k8s.io/v1alpha1/namespaces/default/localqueues/user-queue/pendingworkloads
```

You should get results similar to:

``` json
{
  "kind": "PendingWorkloadsSummary",
  "apiVersion": "visibility.kueue.x-k8s.io/v1alpha1",
  "metadata": {
    "creationTimestamp": null
  },
  "items": [
    {
      "metadata": {
        "name": "job-sample-job-jrjfr-8d56e",
        "namespace": "default",
        "creationTimestamp": "2023-12-05T15:42:03Z",
        "ownerReferences": [
          {
            "apiVersion": "batch/v1",
            "kind": "Job",
            "name": "sample-job-jrjfr",
            "uid": "5863cf0e-b0e7-43bf-a445-f41fa1abedfa"
          }
        ]
      },
      "priority": 0,
      "localQueueName": "user-queue",
      "positionInClusterQueue": 0,
      "positionInLocalQueue": 0
    },
    {
      "metadata": {
        "name": "job-sample-job-jg9dw-5f1a3",
        "namespace": "default",
        "creationTimestamp": "2023-12-05T15:42:03Z",
        "ownerReferences": [
          {
            "apiVersion": "batch/v1",
            "kind": "Job",
            "name": "sample-job-jg9dw",
            "uid": "fd5d1796-f61d-402f-a4c8-cbda646e2676"
          }
        ]
      },
      "priority": 0,
      "localQueueName": "user-queue",
      "positionInClusterQueue": 1,
      "positionInLocalQueue": 1
    },
    {
      "metadata": {
        "name": "job-sample-job-t9b8m-4e770",
        "namespace": "default",
        "creationTimestamp": "2023-12-05T15:42:03Z",
        "ownerReferences": [
          {
            "apiVersion": "batch/v1",
            "kind": "Job",
            "name": "sample-job-t9b8m",
            "uid": "64c26c73-6334-4d13-a1a8-38d99196baa5"
          }
        ]
      },
      "priority": 0,
      "localQueueName": "user-queue",
      "positionInClusterQueue": 2,
      "positionInLocalQueue": 2
    }
  ]
}
```

You can pass optional query parameters:
- limit `<integer>` - 1000 on default. It indicates max number of pending workloads that should be fetched.
- offset `<integer>` - 0 by default. It indicates position of the first pending workload that should be fetched, starting from 0.

To view only 1 pending workloads use, starting from position 1 in LocalQueue run:

```shell
kubectl get --raw "/apis/visibility.kueue.x-k8s.io/v1alpha1/localqueues/user-queue/pendingworkloads?limit=1&offset=1"

```
You should get results similar to 

``` json
{
  "kind": "PendingWorkloadsSummary",
  "apiVersion": "visibility.kueue.x-k8s.io/v1alpha1",
  "metadata": {
    "creationTimestamp": null
  },
  "items": [
    {
      "metadata": {
        "name": "job-sample-job-jg9dw-5f1a3",
        "namespace": "default",
        "creationTimestamp": "2023-12-05T15:42:03Z",
        "ownerReferences": [
          {
            "apiVersion": "batch/v1",
            "kind": "Job",
            "name": "sample-job-jg9dw",
            "uid": "fd5d1796-f61d-402f-a4c8-cbda646e2676"
          }
        ]
      },
      "priority": 0,
      "localQueueName": "user-queue",
      "positionInClusterQueue": 1,
      "positionInLocalQueue": 1
    }
  ]
}
```