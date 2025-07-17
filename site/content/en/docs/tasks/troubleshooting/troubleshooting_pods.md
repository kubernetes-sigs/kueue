---
title: "Troubleshooting Pods"
date: 2024-03-21
weight: 4
description: >
  Troubleshooting the status of a Pod or group of Pods
---

This doc is about troubleshooting [plain Pods](/docs/tasks/run/plain_pods/) when directly managed by Kueue,
in other words, Pods that are not managed by kubernetes Jobs or supported CRDs.

{{% alert title="Note" color="primary" %}}
This doc focuses on the behavior of Kueue when managing Pods that is different from other job integrations.
You can read [Troubleshooting Jobs](troubleshooting_jobs) for more general troubleshooting steps.
{{% /alert %}}

## Is my Pod managed directly by Kueue?

Kueue adds the label `kueue.x-k8s.io/managed` with value `true` to Pods that it manages.
If the label is not present on a Pod, it means that Kueue is not going to admit or account for the
resource usage of this Pod directly.

A Pod might not have the `kueue.x-k8s.io/managed` due to one of the following reasons:

1. The [Pod integration is disabled](/docs/tasks/run/plain_pods/#before-you-begin).
2. The Pod belongs to a namespace that don't satisfy the requirements of
   the [`managedJobsNamespaceSelector`](/docs/reference/kueue-config.v1beta1/#Configuration).
3. The Pod is owned by a Job or equivalent CRD that is managed by Kueue.
4. The Pod doesn't have a `kueue.x-k8s.io/queue-name` label and [`manageJobsWithoutQueueName`](/docs/reference/kueue-config.v1beta1/#Configuration)
   is set to `false`.

{{% alert title="Note" color="primary" %}}
Prior to Kueue v0.10, the Configuration field `integrations.podOptions.namespaceSelector`
was used instead. The use of `podOptions` was
deprecated in Kueue v0.11. Users should migrate to using `managedJobsNamespaceSelector`.
{{% /alert %}}

## Identifying the Workload for your Pod

When using [Pod groups](/docs/tasks/run/plain_pods/#running-a-group-of-pods-to-be-admitted-together),
the name of the Workload matches the value of the label `kueue.x-k8s.io/pod-group-name`.

When using [single Pods](/docs/tasks/run/plain_pods/#running-a-single-pod-admitted-by-kueue), you can identify its corresponding
Workload by following the guide for [Identifying the Workload of a Job](troubleshooting_jobs/#identifying-the-workload-for-your-job).

## Why doesn't a Workload exist for my Pod group?

Before creating a Workload object, Kueue expects all the Pods for the group to be created.
The Pods should all have the same value for the label `kueue.x-k8s.io/pod-group-name` and
the number of Pods should be equal to the value of the annotation `kueue.x-k8s.io/pod-group-total-count`.

You can run the following command to identify whether Kueue has or has not created a Workload
for the Pod:

```bash
kubectl describe pod my-pod -n my-namespace
```

If Kueue didn't create the Workload object, you will see an output similar to the following:

```
...
Events:
  Type     Reason              Age   From                  Message
  ----     ------              ----  ----                  -------
  Warning  ErrWorkloadCompose  14s   pod-kueue-controller  'my-pod-group' group has fewer runnable pods than expected
```

{{% alert title="Note" color="primary" %}}
The above event might show up for the first Pod that Kueue observes, and it will remain
even if Kueue successfully creates the Workload for the Pod group later.
{{% /alert %}}

Once Kueue observes all the Pods for the group, you will see an output similar to the following:

```
...
Events:
  Type     Reason              Age   From                  Message
  ----     ------              ----  ----                  -------
  Normal   CreatedWorkload     14s   pod-kueue-controller  Created Workload: my-namespace/my-pod-group
```

## Why did my Pod disappear?

When you enable [preemption](/docs/concepts/cluster_queue/#preemption), Kueue might preempt Pods
to accommodate higher priority jobs or reclaim quota. Preemption is implemented via `DELETE` calls,
the standard way of terminating a Pod in Kubernetes.

When using single Pods, Kubernetes will delete Workload object along with the Pod, as there is
nothing else holding ownership to it.

Kueue doesn't typically fully delete Pods in a Pod group upon preemption. See the next question
to understand the deletion mechanics for Pods in a Pod group.

## Why aren't Pods in a Pod group deleted when Failed or Succeeded?

When using Pod groups, Kueue keeps a [finalizer](https://kubernetes.io/docs/concepts/overview/working-with-objects/finalizers/)
`kueue.x-k8s.io/managed` to prevent Pods from being deleted and to be able to track the progress of the group.
You should not modify finalizers manually.

Kueue will remove the finalizer from Pods when:
- The group satisfies the [termination](/docs/tasks/run/plain_pods/#termination) criteria, for example,
  when all Pods terminate successfully.
- For Failed Pods, when Kueue observes a replacement Pod.
- You delete the Workload object.

Once a Pod doesn't have any finalizers, Kubernetes will delete the Pods based on:
- Whether a user or a controller has issued a Pod deletion.
- The [Pod garbage collector](https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/#pod-garbage-collection).
