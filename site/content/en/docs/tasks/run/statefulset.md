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

We demonstrate how to support scheduling StatefulSets in Kueue based on the
[Plain Pod Group](https://kueue.sigs.k8s.io/docs/tasks/run/plain_pods/) integration,
where StatefulSet is represented as a single independent workload.

This guide is for [serving users](/docs/tasks#serving-user) that have a basic understanding of Kueue.
For more information, see [Kueue's overview](/docs/overview).

## Before you begin

1. Learn how to [install Kueue with a custom manager configuration](/docs/installation/#install-a-custom-configured-released-version).

2. Ensure that you have the v1/statefulset integration enabled, for example:
   ```yaml
   apiVersion: config.kueue.x-k8s.io/v1beta1
   kind: Configuration
   integrations:
     frameworks:
      - "pod" # required by statefulset
      - "statefulset"
   ```
   Also, follow steps in [Run Plain Pods](/docs/tasks/run/plain_pods/#before-you-begin)
   to learn how to enable the `v1/pod` integration and how to configure it using the `podOptions` field.

3. Check [Administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas) for details on the initial Kueue setup.

## Running a StatefulSet admitted by Kueue

When running a StatefulSet on Kueue, take into consideration the following aspects:

### a. Queue selection

The target [local queue](/docs/concepts/local_queue) should be specified in the `metadata.labels` section of the StatefulSet configuration.

```yaml
metadata:
   labels:
      kueue.x-k8s.io/queue-name: user-queue
```

### b. Configure the resource needs
The resource needs of the workload can be configured in the `spec.template.spec.containers`.

```yaml
spec:
  template:
     spec:
      containers:
       - resources:
           requests:
             cpu: 3
```

### c. Scaling

Currently, scaling operations on StatefulSets are not supported.
This means you cannot perform scale up or scale down operations directly through Kueue.

## Example
Here is a sample StatefulSet:

{{< include "examples/serving-workloads/sample-statefulset.yaml" "yaml" >}}

You can create the StatefulSet using the following command:

```sh
kubectl create -f sample-statefulset.yaml
```
