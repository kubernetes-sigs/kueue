---
title: "Run StatefulSet"
linkTitle: "StatefulSet"
date: 2024-07-25
weight: 6
description: >
  Run a StatefulSet as a Kueue-managed workload.
---

This page shows how to leverage Kueue's scheduling and resource management
capabilities when running StatefulSets.
Although Kueue does not yet support managing a StatefulSet as a single Workload,
it's still possible to leverage Kueue's scheduling and resource management capabilities for the individual Pods of the StatefulSet.

We demonstrate how to support scheduling StatefulSets in Kueue based on the Plain Pod integration,
where every Pod from a StatefulSet is represented as a single independent Plain Pod.

This guide is for [serving users](/docs/tasks#serving-user) that have a basic understanding of Kueue.
For more information, see [Kueue's overview](/docs/overview).

## Before you begin

1. Learn how to [install Kueue with a custom manager configuration](/docs/installation/#install-a-custom-configured-released-version).

2. Follow steps in [Run Plain Pods](/docs/tasks/run/plain_pods/#before-you-begin)
   to learn how to enable the `v1/pod` integration and how to configure it using the `podOptions` field.

3. Check [Administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas) for details on the initial Kueue setup.

## Running a StatefulSet admitted by Kueue

When running a StatefulSet on Kueue, take into consideration the following aspects:

### a. Queue selection

The target [local queue](/docs/concepts/local_queue) should be specified in the `labels` section of the StatefulSet configuration.
Since Kueue's scheduling and resource management will be applied to the individual Pods of the StatefulSet,
the queue name should be specified at the Pod level.

```yaml
labels:
   kueue.x-k8s.io/queue-name: user-queue
```

### b. Configure the resource needs
The resource needs of the workload can be configured in the spec.template.spec.containers.

```yaml
    - resources:
        requests:
          cpu: 3
```

## Example
Here is a sample StatefulSet:

{{< include "examples/serving-workloads/sample-statefulset.yaml" "yaml" >}}

You can create the StatefulSet using the following command:

```sh
kubectl create -f sample-statefulset.yaml
```
