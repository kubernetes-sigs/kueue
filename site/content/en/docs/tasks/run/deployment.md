---
title: "Run Deployment"
linkTitle: "Deployment"
date: 2024-07-25
weight: 6
description: >
  Run a Deployment as a Kueue-managed job.
---

This page shows how to leverage Kueue's scheduling and resource management
capabilities when running Deployments.

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

2. Kueue will run webhooks for all created pods if the pod integration is enabled. The webhook namespaceSelector could be 
   used to filter the pods to reconcile. The default webhook namespaceSelector is:
   ```yaml
   matchExpressions:
   - key: kubernetes.io/metadata.name
     operator: NotIn
     values: [ kube-system, kueue-system ]
   ```
   
   When you [install Kueue via Helm](/docs/installation/#install-via-helm), the webhook namespace selector 
   will match the `integrations.podOptions.namespaceSelector` in the `values.yaml`.

   Make sure that namespaceSelector never matches the kueue namespace, otherwise the 
   Kueue deployment won't be able to create Pods.

3. Pods that belong to other API resources managed by Kueue are excluded from being queued by `pod` integration. 
   For example, pods managed by `batch/v1.Job` won't be managed by `pod` integration.

4. Check [Administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas) for details on the initial Kueue setup.

## Running a Deployment admitted by Kueue

When running Deployment on Kueue, take into consideration the following aspects:

### a. Queue selection

The target [local queue](/docs/concepts/local_queue) should be specified in the `spec.template.metadata.labels` section of the Deployment configuration.

```yaml
spec:
   template:
      metadata:
         labels:
            kueue.x-k8s.io/queue-name: user-queue
```

### b. Configure the resource needs

The resource needs of the workload can be configured in the `spec.template.spec.containers`.

```yaml
    - resources:
        requests:
          cpu: 3
```

### c. The "managed" label

Kueue will inject the `kueue.x-k8s.io/managed=true` label to indicate which pods are managed by it.

### d. Limitations

- A Kueue managed Deployment cannot be created in `kube-system` or `kueue-system` namespaces.
- In case of [preemption](/docs/concepts/cluster_queue/#preemption), the Deployment will
  be terminated and deleted.

## Example Deployment

Here is a sample Deployment:

{{< include "examples/deployment-kueue/kueue-deployment.yaml" "yaml" >}}

You can create the Deployment using the following command:
```sh
# Create the pod
kubectl create -f kueue-deployment.yaml
```
