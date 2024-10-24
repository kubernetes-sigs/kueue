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

We demonstrate how to support scheduling StatefulSets in Kueue based on the Plain Pod integration with the help of PodGroup,
where StatefulSet is represented as a single independent workload.

This guide is for [serving users](/docs/tasks#serving-user) that have a basic understanding of Kueue.
For more information, see [Kueue's overview](/docs/overview).

## Before you begin

1. Learn how to [install Kueue with a custom manager configuration](/docs/installation/#install-a-custom-configured-released-version).

2. Follow steps in [Run Plain Pods](/docs/tasks/run/plain_pods/#before-you-begin)
   to learn how to enable the `v1/pod` integration and how to configure it using the `podOptions` field.
   Also, ensure that you have the `v1/statefulset` integration enabled.

   A configuration for Kueue with enabled statefulset integration would look like follows:
   ```yaml
   apiVersion: config.kueue.x-k8s.io/v1beta1
   kind: Configuration
   integrations:
     frameworks:
      - "pod" # required by statefulset
      - "statefulset"
     podOptions:
       # You can change namespaceSelector to define in which 
       # namespaces kueue will manage the pods.
       namespaceSelector:
         matchExpressions:
         - key: kubernetes.io/metadata.name
           operator: NotIn
           values: [ kube-system, kueue-system ]
       # Kueue uses podSelector to manage pods with particular 
       # labels. The default podSelector will match all the pods. 
       podSelector:
         matchExpressions:
         - key: kueue-job
           operator: In
           values: [ "true", "True", "yes" ]
   ```

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
Ensure that your StatefulSet configuration accounts for the fixed number of Pods required for your workload.


## Example
Here is a sample StatefulSet:

{{< include "examples/serving-workloads/sample-statefulset.yaml" "yaml" >}}

You can create the StatefulSet using the following command:

```sh
kubectl create -f sample-statefulset.yaml
```
