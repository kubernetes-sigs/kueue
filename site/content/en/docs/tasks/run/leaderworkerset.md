---
title: "Run LeaderWorkerSet"
linkTitle: "LeaderWorkerSet"
date: 2025-02-17
weight: 6
description: >
   Run a LeaderWorkerSet as a Kueue-managed workload.
---

This page shows how to leverage Kueue's scheduling and resource management
capabilities when running [LeaderWorkerSet](https://github.com/kubernetes-sigs/lws).

We demonstrate how to support scheduling LeaderWorkerSets where a group of Pods constitutes
a unit of admission represented by a Workload. This allows to scale-up and down LeaderWorkerSets
group by group.

This integration is based on the [Plain Pod Group](https://kueue.sigs.k8s.io/docs/tasks/run/plain_pods/) integration.

This guide is for [serving users](/docs/tasks#serving-user) that have a basic understanding of Kueue.
For more information, see [Kueue's overview](/docs/overview).

## Before you begin

1. Learn how to [install Kueue with a custom manager configuration](/docs/installation/#install-a-custom-configured-released-version).

2. Ensure that you have the `leaderworkerset.x-k8s.io/leaderworkerset` integration enabled, for example:
   ```yaml
   apiVersion: config.kueue.x-k8s.io/v1beta1
   kind: Configuration
   integrations:
     frameworks:
      - "pod"
      - "leaderworkerset.x-k8s.io/leaderworkerset"
   ```
   Also, follow steps in [Run Plain Pods](/docs/tasks/run/plain_pods/#before-you-begin)
   to learn how to enable and configure the `pod` integration.

3. Check [Administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas) for details on the initial Kueue setup.

## Running a LeaderWorkerSet admitted by Kueue

When running a LeaderWorkerSet on Kueue, take into consideration the following aspects:

### a. Queue selection

The target [local queue](/docs/concepts/local_queue) should be specified in the `metadata.labels` section of the LeaderWorkerSet configuration.

```yaml
metadata:
   labels:
      kueue.x-k8s.io/queue-name: user-queue
```

### b. Configure the resource needs
The resource needs of the workload can be configured in the `spec.template.spec.containers`.

```yaml
spec:
   leaderWorkerTemplate:
      leaderTemplate:
         spec:
            containers:
               - resources:
                    requests:
                       cpu: "100m"
      workerTemplate:
         spec:
            containers:
               - resources:
                    requests:
                       cpu: "100m"
```

### c. Scaling

You can perform scale up or scale down operations on a LeaderWorkerSet `.spec.replicas`.

The unit of scaling is a LWS group. By changing the number of `replicas` in the LWS you can create
or delete entire groups of Pods. As a result of scale up the newly created group of Pods is
suspended by a scheduling gate, until the corresponding Workload is admitted.

## Example
Here is a sample LeaderWorkerSet:

{{< include "examples/serving-workloads/sample-leaderworkerset.yaml" "yaml" >}}

You can create the LeaderWorkerSet using the following command:

```sh
kubectl create -f sample-leaderworkerset.yaml
```
