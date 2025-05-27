---
title: "Pending Workloads on-demand"
date: 2024-09-30
weight: 3
description: >
  Monitor pending Workloads with the on-demand visibility API
---

This page shows you how to monitor pending workloads with `VisibilityOnDemand` feature.

The intended audience for this page are [batch administrators](/docs/tasks#batch-administrator) for [Cluster Queue visibility section](#cluster-queue-visibility-via-kubectl), and [batch users](/docs/tasks#batch-user) for [Local Queue Visibility section](#local-queue-visibility-via-kubectl).

From version v0.6.0, Kueue provides the ability for a batch administrators to monitor
the pipeline of pending jobs, and help users to estimate when their jobs will
start.

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster is running.
- The kubectl command-line tool has communication with your cluster.
- [Kueue is installed](/docs/installation) in version v0.6.0 or later.

### Directly accessing the Visibility API

If you want to directly access the Visibility API with a http client like 
`curl` or `wget`, or a browser, there are multiple ways you can locate and 
authenticate against the Visibility API server:

#### (Recommended) Using kubectl proxy

Run kubectl in proxy mode. This method is recommended, since it uses 
the stored API server location and verifies the identity of the API server using 
a self-signed certificate. No man-in-the-middle (MITM) attack is possible using 
this method. 

For more details, see [kubectl documentation](https://kubernetes.io/docs/tasks/administer-cluster/access-cluster-api/#using-kubectl-proxy).

#### Without kubectl proxy

Alternatively, you can provide the location and credentials directly to the http client. 
This works with client code that is confused by proxies. To protect against man in 
the middle attacks, you'll need to import a root cert into your browser.

For more details, see [kubectl documentation](https://kubernetes.io/docs/tasks/administer-cluster/access-cluster-api/#without-kubectl-proxy).

You then need to create `ClusterRole` and `ClusterRoleBinding` for that same (default) k8s service account

{{< include "examples/visibility/cluster-role-and-binding.yaml" "yaml" >}}

using a command:

```shell
kubectl apply -f https://kueue.sigs.k8s.io/examples/admin/cluster-role-and-binding.yaml
```

## Monitor pending workloads on demand

{{< feature-state state="beta" for_version="v0.9" >}}
{{% alert title="Note" color="primary" %}}

`VisibilityOnDemand` is a Beta feature enabled by default.

You can disable it by setting the `VisibilityOnDemand` feature gate. Check the [Installation](/docs/installation/#change-the-feature-gates-configuration) guide for details on feature gate configuration.

If you disable the feature, you also need to remove the associated `APIService` from your cluster by doing `kubectl delete APIService v1beta1.visibility.kueue.x-k8s.io`.

{{% /alert %}}

To install a simple setup of ClusterQueue

{{< include "examples/admin/single-clusterqueue-setup.yaml" "yaml" >}}

run the following command:

```shell
kubectl apply -f https://kueue.sigs.k8s.io/examples/admin/single-clusterqueue-setup.yaml
```

Now, let's create 6 jobs

{{< include "examples/jobs/sample-job.yaml" "yaml" >}}

using a command: 

```shell
for i in {1..6}; do kubectl create -f https://kueue.sigs.k8s.io/examples/jobs/sample-job.yaml; done
```

3 of them saturate the ClusterQueue and the other 3 should be pending.

### Cluster Queue visibility via kubectl

To view pending workloads in ClusterQueue `cluster-queue` run the following command:

```shell
kubectl get --raw "/apis/visibility.kueue.x-k8s.io/v1beta1/clusterqueues/cluster-queue/pendingworkloads"
```

You should get results similar to:

```json
{
  "kind": "PendingWorkloadsSummary",
  "apiVersion": "visibility.kueue.x-k8s.io/v1beta1",
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
kubectl get --raw "/apis/visibility.kueue.x-k8s.io/v1beta1/clusterqueues/cluster-queue/pendingworkloads?limit=1&offset=1"
```

You should get results similar to 

``` json
{
  "kind": "PendingWorkloadsSummary",
  "apiVersion": "visibility.kueue.x-k8s.io/v1beta1",
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

### Cluster Queue visibility via curl

If you followed steps described in [Directly accessing the Visibility API](#directly-accessing-the-visibility-api) above,
you can use curl to view pending workloads in ClusterQueue using following commands:

{{< tabpane lang="shell" persist=disabled >}}
{{< tab header="Using kubectl proxy" >}} curl http://localhost:8080/apis/visibility.kueue.x-k8s.io/v1beta1/clusterqueues/cluster-queue/pendingworkloads {{< /tab >}}
{{< tab header="Without kubectl proxy" >}} curl -X GET $APISERVER/apis/visibility.kueue.x-k8s.io/v1beta1/clusterqueues/cluster-queue/pendingworkloads --header "Authorization: Bearer $TOKEN" --insecure {{< /tab >}}
{{< /tabpane >}}

You should get results similar to:

```json
{
  "kind": "PendingWorkloadsSummary",
  "apiVersion": "visibility.kueue.x-k8s.io/v1beta1",
  "metadata": {
    "creationTimestamp": null
  },
  "items": [
    {
      "metadata": {
        "name": "job-sample-job-z8sc5-223e8",
        "namespace": "default",
        "creationTimestamp": "2024-09-29T10:58:32Z",
        "ownerReferences": [
          {
            "apiVersion": "batch/v1",
            "kind": "Job",
            "name": "sample-job-z8sc5",
            "uid": "7086b1bb-39b7-42e5-9f6b-ee07d0100051"
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
        "name": "job-sample-job-2mfzb-28f54",
        "namespace": "default",
        "creationTimestamp": "2024-09-29T10:58:32Z",
        "ownerReferences": [
          {
            "apiVersion": "batch/v1",
            "kind": "Job",
            "name": "sample-job-2mfzb",
            "uid": "51ae8e48-8785-4bbb-9811-f9c1f041b368"
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
        "name": "job-sample-job-dpggt-3ecac",
        "namespace": "default",
        "creationTimestamp": "2024-09-29T10:58:32Z",
        "ownerReferences": [
          {
            "apiVersion": "batch/v1",
            "kind": "Job",
            "name": "sample-job-dpggt",
            "uid": "870655da-07d5-4910-be99-7650bf89b0d2"
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

### Local Queue visibility via kubectl

Similarly to ClusterQueue, to view pending workloads in LocalQueue `user-queue` run the following command:

```shell
kubectl get --raw /apis/visibility.kueue.x-k8s.io/v1beta1/namespaces/default/localqueues/user-queue/pendingworkloads
```

You should get results similar to:

``` json
{
  "kind": "PendingWorkloadsSummary",
  "apiVersion": "visibility.kueue.x-k8s.io/v1beta1",
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
kubectl get --raw "/apis/visibility.kueue.x-k8s.io/v1beta1/namespaces/default/localqueues/user-queue/pendingworkloads?limit=1&offset=1"

```
You should get results similar to 

``` json
{
  "kind": "PendingWorkloadsSummary",
  "apiVersion": "visibility.kueue.x-k8s.io/v1beta1",
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

### Local Queue visibility via curl

If you followed steps described in [Directly accessing the Visibility API](#directly-accessing-the-visibility-api) 
above, you can use curl to view pending workloads in LocalQueue using following commands:

{{< tabpane lang="shell" persist=disabled >}}
{{< tab header="Using kubectl proxy" >}} curl http://localhost:8080/apis/visibility.kueue.x-k8s.io/v1beta1/namespaces/default/localqueues/user-queue/pendingworkloads {{< /tab >}}
{{< tab header="Without kubectl proxy" >}} curl -X GET $APISERVER/apis/visibility.kueue.x-k8s.io/v1beta1/namespaces/default/localqueues/user-queue/pendingworkloads --header "Authorization: Bearer $TOKEN" --insecure {{< /tab >}}
{{< /tabpane >}}

You should get results similar to:

```json
{
  "kind": "PendingWorkloadsSummary",
  "apiVersion": "visibility.kueue.x-k8s.io/v1beta1",
  "metadata": {
    "creationTimestamp": null
  },
  "items": [
    {
      "metadata": {
        "name": "job-sample-job-z8sc5-223e8",
        "namespace": "default",
        "creationTimestamp": "2024-09-29T10:58:32Z",
        "ownerReferences": [
          {
            "apiVersion": "batch/v1",
            "kind": "Job",
            "name": "sample-job-z8sc5",
            "uid": "7086b1bb-39b7-42e5-9f6b-ee07d0100051"
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
        "name": "job-sample-job-2mfzb-28f54",
        "namespace": "default",
        "creationTimestamp": "2024-09-29T10:58:32Z",
        "ownerReferences": [
          {
            "apiVersion": "batch/v1",
            "kind": "Job",
            "name": "sample-job-2mfzb",
            "uid": "51ae8e48-8785-4bbb-9811-f9c1f041b368"
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
        "name": "job-sample-job-dpggt-3ecac",
        "namespace": "default",
        "creationTimestamp": "2024-09-29T10:58:32Z",
        "ownerReferences": [
          {
            "apiVersion": "batch/v1",
            "kind": "Job",
            "name": "sample-job-dpggt",
            "uid": "870655da-07d5-4910-be99-7650bf89b0d2"
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
