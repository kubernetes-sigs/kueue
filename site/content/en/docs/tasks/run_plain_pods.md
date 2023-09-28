---
title: "Run A Plain Pod"
date: 2023-09-27
weight: 6
description: >
  Run a Kueue scheduled Pod.
---

This page shows how to leverage Kueue's scheduling and resource management capabilities when running plain Pods.

This guide is for [batch users](/docs/tasks#batch-user) that have a basic understanding of Kueue. For more information, see [Kueue's overview](/docs/overview).

## Before you begin

1. By default, the integration for `v1/pod` is not enabled.
   Learn how to [install Kueue with a custom manager configuration](/docs/installation/#install-a-custom-configured-released-version)
   and enable the `pod` integration.

   A configuration for Kueue with enabled pod integration would look like follows:
   ```yaml
   apiVersion: config.kueue.x-k8s.io/v1beta1
   kind: Configuration
   integrations:
     frameworks:
      - "pod"
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

2. Pods that belong to other API resources managed by Kueue are excluded from being queued by `pod` integration. 
   For example, pods managed by `batch/v1/Job` won't be managed by `pod` integration if the `job` integration is enabled.

3. Kueue will inject a `kueue.x-k8s.io/managed=true` label to indicate which pods are managed by it.

4. Check [Administer cluster quotas](/docs/tasks/administer_cluster_quotas) for details on the initial Kueue setup.

## Pod definition

When running Pods on Kueue, take into consideration the following aspects:

### a. Queue selection

The target [local queue](/docs/concepts/local_queue) should be specified in the `metadata.labels` section of the Pod configuration.

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. Configure the resource needs

The resource needs of the workload can be configured in the `spec.containers`.

```yaml
    - resources:
        requests:
          cpu: 3
```

### c. Limitations

- A Kueue managed Pod cannot be created in `kube-system` or `kueue-system` namespaces.
- In case of [preemption](/docs/concepts/cluster_queue/#preemption), Pod will
  be terminated and deleted.

## Example Pod

Here is a sample Pod that just sleep for a few seconds:

{{< include "examples/pods-kueue/kueue-pod.yaml" "yaml" >}}

You can create the Pod using the following command:
```sh
# Create the pod
kubectl apply -f kueue-pod.yaml
```
