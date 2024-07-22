---
title: "Run Plain Pods"
linkTitle: "Plain Pods"
date: 2023-09-27
weight: 6
description: >
  Run a single Pod, or a group of Pods as a Kueue-managed job.
---

This page shows how to leverage Kueue's scheduling and resource management
capabilities when running plain Pods. Kueue supports management of both
[individual Pods](#running-a-single-pod-admitted-by-kueue), or
[Pod groups](#running-a-group-of-pods-to-be-admitted-together).

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

## Running a single Pod admitted by Kueue

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

### c. The "managed" label

Kueue will inject the `kueue.x-k8s.io/managed=true` label to indicate which pods are managed by it.

### d. Limitations

- A Kueue managed Pod cannot be created in `kube-system` or `kueue-system` namespaces.
- In case of [preemption](/docs/concepts/cluster_queue/#preemption), the Pod will
  be terminated and deleted.

## Example Pod

Here is a sample Pod that just sleeps for a few seconds:

{{< include "examples/pods-kueue/kueue-pod.yaml" "yaml" >}}

You can create the Pod using the following command:
```sh
# Create the pod
kubectl create -f kueue-pod.yaml
```

## Running a group of Pods to be admitted together

In order to run a set of Pods as a single unit, called Pod group, add the
"pod-group-name" label, and the "pod-group-total-count" annotation to all
members of the group, consistently:

```yaml
metadata:
  labels:
    kueue.x-k8s.io/pod-group-name: "group-name"
  annotations:
    kueue.x-k8s.io/pod-group-total-count: "2"
```

### Feature limitations

Kueue provides only the minimal required functionality of running Pod groups,
just for the need of environments where the Pods are managed by external
controllers directly, without a Job-level CRD.

As a consequence of this design decision, Kueue does not re-implement core
functionalities that are available in the Kubernetes Job API, such as advanced retry
policies. In particular, Kueue does not re-create failed Pods.

This design choice impacts the scenario of
[preemption](/docs/concepts/cluster_queue/#preemption).
When a Kueue needs to preempt a workload that represents a Pod group, kueue sends
delete requests for all of the Pods in the group. It is the responsibility of the
user or controller that created the original Pods to create replacement Pods.

{{% alert title="Note" color="primary" %}}
We recommend using the kubernetes Job API or similar CRDs such as
JobSet, MPIJob, RayJob (see more [here](/docs/tasks/#batch-user)).
{{% /alert %}}

### Termination

Kueue considers a Pod group as successful, and marks the associated Workload as
finished, when the number of succeeded Pods equals the Pod group size.

If a Pod group is not successful, there are two ways you may want to use to
terminate execution of a Pod group to free the reserved resources:
1. Issue a Delete request for the Workload object. Kueue will terminate all
   remaining Pods.
2. Set the `kueue.x-k8s.io/retriable-in-group: false` annotation on at least
   one Pod in the group (can be a replacement Pod). Kueue will mark the workload
   as finished once all Pods are terminated.

### Example Pod group

Here is a sample Pod group that just sleeps for a few seconds:

{{< include "examples/pods-kueue/kueue-pod-group.yaml" "yaml" >}}

You can create the Pod group using the following command:
```sh
kubectl create -f kueue-pod-group.yaml
```

The name of the associated Workload created by Kueue equals the name of the Pod
group. In this example it is `sample-group`, you can inspect the workload using:
```sh
kubectl describe workload/sample-group
```
