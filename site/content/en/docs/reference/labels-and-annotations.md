---
title: "Labels and Annotations"
linkTitle: "Labels and Annotations"
date: 2024-10-09
---

This page serves as a reference for all labels and annotations in Kueue.


### kueue.x-k8s.io/is-group-workload

Type: Annotation

Example: `kueue.x-k8s.io/is-group-workload: "true"`

Used on: [Workload](/docs/concepts/workload/).

The label key indicates that this workload is used for a group of Pods.


### kueue.x-k8s.io/job-completions-equal-parallelism

Type: Annotation

Example: `kueue.x-k8s.io/job-completions-equal-parallelism: "true"`

Used on: [batch/Job](/docs/tasks/run/jobs/).

The label key is used to keep `completions` and `parallelism` in sync


### kueue.x-k8s.io/job-min-parallelism

Type: Annotation

Example: `kueue.x-k8s.io/job-min-parallelism: "5"`

Used on: [batch/Job](/docs/tasks/run/jobs/).

The label key indicates the minimum `parallelism` acceptable for the job in the case of partial admission.


### kueue.x-k8s.io/job-uid

Type: Label

Example: `kueue.x-k8s.io/job-uid: "46ef6b23-a7d9-42b1-b0f8-071bbb29a94d"`

Used on: [Workload](/docs/concepts/workload/).

The label key in the workload resource holds the UID of the owner job.


### kueue.x-k8s.io/managed

Type: Label

Example: `kueue.x-k8s.io/managed: "true"`

Used on: Resources managed by Kueue.

The label key that indicates an object is managed by Kueue.


### kueue.x-k8s.io/multikueue-origin

Type: Label

Example: `kueue.x-k8s.io/multikueue-origin: "true"`

Used on: [MultiKueue](/docs/concepts/multikueue/).

The label key is used to track the creator of MultiKueue remote objects.


### kueue.x-k8s.io/pod-group-name

Type: Label

Example: `kueue.x-k8s.io/pod-group-name: "my-pod-group-name"`

Used on: [Plain Pods](/docs/tasks/run/plain_pods/).

The label key indicates the name of the group of Pods that should be admitted together.


### kueue.x-k8s.io/pod-group-total-count

Type: Annotation

Example: `kueue.x-k8s.io/pod-group-total-count: "2"`

Used on: [Plain Pods](/docs/tasks/run/plain_pods/).

The label key is used to indicate how many Pods to expect in the group.


### kueue.x-k8s.io/prebuilt-workload-name

Type: Label

Example: `kueue.x-k8s.io/prebuilt -workload-name: "my-prebuild-workload-name"`

Used on: Jobs.

The label key of the job holds the name of the pre-built workload to be used.


### kueue.x-k8s.io/priority-class

Type: Label

Example: `kueue.x-k8s.io/priority-class: "my-priority-class-name"`

Used on: Jobs.

The label key in the workload holds the `workloadPriorityClass` name.
This label is always mutable, as it may be useful for preemption.
For more details, see [Workload Priority Class](/docs/concepts/workload_priority_class/).


### kueue.x-k8s.io/queue-name

Type: Label

Example: `kueue.x-k8s.io/queue-name: "my-local-queue"`

Used on: Jobs.

The label key in the workload holds the queue name.


### kueue.x-k8s.io/queue-name (deprecated)

Type: Annotation

Example: `kueue.x-k8s.io/queue-name: "my-local-queue"`

Used on: Jobs.

The annotation key in the workload holds the queue name.

{{% alert title="Warning" color="warning" %}}
Starting from `v1beta1` this annotation is deprecated.
Please use [kueue.x-k8s.io/queue-name label](#kueuex-k8sioqueue-name) instead.
{{% /alert %}}


### kueue.x-k8s.io/retriable-in-group

Type: Annotation

Example: `kueue.x-k8s.io/retriable-in-group: "false"`

Used on: [Plain Pods](/docs/tasks/run/plain_pods/).

The label key is used to finalize the group if at least one terminated Pod (either Failed or Succeeded)
has the `retriable-in-group: false` annotation.


### kueue.x-k8s.io/role-hash

Type: Annotation

Example: `kueue.x-k8s.io/role-hash: "b54683bb"`

Used on: [Plain Pods](/docs/tasks/run/plain_pods/).

The label key is used as the name for a Workload podSet.
