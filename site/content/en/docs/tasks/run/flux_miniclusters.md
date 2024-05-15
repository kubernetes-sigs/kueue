---
title: "Run A Flux MiniCluster"
linkTitle: "Flux MiniClusters"
date: 2022-02-14
weight: 6
description: >
  Run a Kueue scheduled Flux MiniCluster.
---

This page shows how to leverage Kueue's scheduling and resource management capabilities when running [Flux Operator's](https://flux-framework.org/flux-operator/) MiniClusters.

This guide is for [batch users](/docs/tasks#batch-user) that have a basic understanding of Kueue. For more information, see [Kueue's overview](/docs/overview).

## Before you begin

Check [administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas) for details on the initial cluster setup.

Check [the Flux Operator installation guide](https://flux-framework.org/flux-operator/getting_started/user-guide.html#install).

## MiniCluster definition

Because a Flux MiniCluster runs as a [`batch/Job`](https://kubernetes.io/docs/concepts/workloads/controllers/job/), Kueue does not require extra components to manage a Flux MiniCluster.
However, take into consideration the following aspects:

### a. Queue selection

The target [local queue](/docs/concepts/local_queue) should be specified in the `spec.jobLabels` section of the MiniCluster configuration.

```yaml
  jobLabels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. Configure the resource needs

The resource needs of the workload can be configured in the `spec.container[*].resources` sections of the MiniCluster configuration.

```yaml
spec:
  containers:
    - image: <image>
      resources:
        requests:
          cpu: 4
          memory: "200Mi"
```

## Sample MiniCluster

```yaml
apiVersion: flux-framework.org/v1alpha1
kind: MiniCluster
metadata:
  generateName: flux-sample-kueue-
spec:
  size: 1
  containers:
    - image: ghcr.io/flux-framework/flux-restful-api:latest
      command: sleep 10 
      resources:
        requests:
          cpu: 4
          memory: "200Mi"
  jobLabels:
    kueue.x-k8s.io/queue-name: user-queue
```

For equivalent instructions for doing this in Python, see [Run Python Jobs](/docs/tasks/run/python_jobs/#flux-operator-job).
