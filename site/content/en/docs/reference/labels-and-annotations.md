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

The annotation key indicates that this workload is used for a group of Pods.

### kueue.x-k8s.io/job-completions-equal-parallelism

Type: Annotation

Example: `kueue.x-k8s.io/job-completions-equal-parallelism: "true"`

Used on: [batch/Job](/docs/tasks/run/jobs/).

The annotation key is used to keep `completions` and `parallelism` in sync

### kueue.x-k8s.io/job-min-parallelism

Type: Annotation

Example: `kueue.x-k8s.io/job-min-parallelism: "5"`

Used on: [batch/Job](/docs/tasks/run/jobs/).

The annotation key indicates the minimum `parallelism` acceptable for the job in the case of partial admission.

### kueue.x-k8s.io/job-owner-gvk

Type: Annotation

Example: `kueue.x-k8s.io/job-owner-gvk: "leaderworkerset.x-k8s.io/v1, Kind=LeaderWorkerSet"`

Used on: [Workload](/docs/concepts/workload/).

The annotation holds the GVK (GroupVersionKind) of the owner job. Used for MultiKueue adapter
lookup in workloads with multiple owner references (e.g., LeaderWorkerSet). This is an annotation
rather than a label because the GVK string format contains characters invalid in label values.

### kueue.x-k8s.io/job-owner-name

Type: Annotation

Example: `kueue.x-k8s.io/job-owner-name: "my-leaderworkerset"`

Used on: [Workload](/docs/concepts/workload/).

The annotation holds the name of the owner job. Used when the owner reference has been removed
by Kubernetes garbage collection but the job name is still needed for MultiKueue operations.

### kueue.x-k8s.io/job-uid

Type: Label

Example: `kueue.x-k8s.io/job-uid: "46ef6b23-a7d9-42b1-b0f8-071bbb29a94d"`

Used on: [Workload](/docs/concepts/workload/).

The label key in the workload resource holds the UID of the owner job.

### kueue.x-k8s.io/managed

Type: Label

Example: `kueue.x-k8s.io/managed: "true"`

Used on: [Plain Pods](/docs/tasks/run/plain_pods/) and [ProvisioningRequest](/docs/concepts/admission_check/provisioning_request).

The label key that indicates which pods and ProvisioningRequest are managed by Kueuue.

### kueue.x-k8s.io/max-exec-time-seconds

Type: Label

Example: `kueue.x-k8s.io/max-exec-time-seconds: "120"`

Used on: Kueue-managed Jobs.

The value of this label is passed in the Job's Workload `spec.maximumExecutionTimeSeconds` and used by the [Maximum execution time](/docs/concepts/workload/#maximum-execution-time) feature.

### kueue.x-k8s.io/multikueue-origin

Type: Label

Example: `kueue.x-k8s.io/multikueue-origin: "true"`

Used on: [MultiKueue](/docs/concepts/multikueue/).

The label key is used to track the creator of MultiKueue remote objects in Worker Cluster.

### kueue.x-k8s.io/multikueue-origin-uid

Type: Annotation

Example: `kueue.x-k8s.io/multikueue-origin-uid: "a1b2c3d4-e5f6-7890-abcd-ef1234567890"`

Used on: [MultiKueue](/docs/concepts/multikueue/).

The annotation stores the UID of the original object from the management cluster on remote objects
in worker clusters. Used by job types (e.g., LeaderWorkerSet) that generate workload names based on
their UID, ensuring workload names match between management and worker clusters.

### kueue.x-k8s.io/pod-group-fast-admission

Type: Annotation

Example: `kueue.x-k8s.io/pod-group-fast-admission: "true"`

Used on: [Plain Pods](/docs/tasks/run/plain_pods/).

The annotation key is used to allow admitting a PodGroup as soon as the first pod in the group is created.

### kueue.x-k8s.io/pod-group-name

Type: Label

Example: `kueue.x-k8s.io/pod-group-name: "my-pod-group-name"`

Used on: [Plain Pods](/docs/tasks/run/plain_pods/).

The label key indicates the name of the group of Pods that should be admitted together.

### kueue.x-k8s.io/pod-group-pod-index

Type: Label

Example: `kueue.x-k8s.io/pod-group-pod-index: "0"`

Used on: [Plain Pods](/docs/tasks/run/plain_pods/).

The label key indicated the Pod's index within the pod group it belongs to.

### kueue.x-k8s.io/pod-group-pod-index-label

Type: Annotation

Example: `kueue.x-k8s.io/pod-group-pod-index-label: "apps.kubernetes.io/pod-index"`

Used on: [Plain Pods](/docs/tasks/run/plain_pods/).

The annotation key indicates a label name used to retrieve the Pod's index within the group.

### kueue.x-k8s.io/pod-index-offset

Type: Annotation

Example: `kueue.x-k8s.io/pod-index-offset: "1"`

Used on: Kueue-managed Jobs (e.g., [MPIJob](/docs/tasks/run/kubeflow/mpijobs/)).

The annotation indicates a starting index offset for Pod replicas within a PodSet.
For example, when an MPIJob uses `runLauncherAsWorker` mode, Worker replica indexes
start from `1` instead of `0`. This annotation is set by the Kueue webhook and used
by the TAS topology ungater to correctly map Pod indexes to topology assignments.

Note: This annotation is not added when `kueue.x-k8s.io/podset-group-name` is specified,
as offset management is delegated to the PodSet Group mechanism in that case.

### kueue.x-k8s.io/pod-group-serving

Type: Annotation

Example: `kueue.x-k8s.io/pod-group-serving: "true"`

Used on: [Plain Pods](/docs/tasks/run/plain_pods/).

The annotation key is used to indicate whether the pod group is being used as serving workload.

### kueue.x-k8s.io/pod-group-total-count

Type: Annotation

Example: `kueue.x-k8s.io/pod-group-total-count: "2"`

Used on: [Plain Pods](/docs/tasks/run/plain_pods/).

The annotation key is used to indicate how many Pods to expect in the group.

### kueue.x-k8s.io/podset

Type: Label

Example: `kueue.x-k8s.io/podset: "main"`

Used on: Kueue-managed Jobs.

The label key is used on the Job's PodTemplate to indicate the name 
of the PodSet of the admitted Workload corresponding to the PodTemplate.
The label is set when starting the Job, and removed on stopping the Job.

### kueue.x-k8s.io/pod-suspending-parent

Type: Annotation

Example: `kueue.x-k8s.io/pod-suspending-parent: "deployment"`

Used on: [Plain Pods](/docs/tasks/run/plain_pods/).

The annotation key is used to indicate the integration name of the Pod owner.


### kueue.x-k8s.io/prebuilt-workload-name

Type: Label

Example: `kueue.x-k8s.io/prebuilt-workload-name: "my-prebuilt-workload-name"`

Used on: Kueue-managed Jobs.

The label key of the job holds the name of the pre-built workload to be used.
The intended use of prebuilt workload is to create the Job once the workload
is created. In other scenarios the behavior is undefined.

Note: When using `kueue.x-k8s.io/pod-group-name`, the prebuilt workload name 
and the pod group name should be the same.

### kueue.x-k8s.io/priority-class

Type: Label

Example: `kueue.x-k8s.io/priority-class: "my-priority-class-name"`

Used on: Kueue-managed Jobs.

The label key in the workload holds the `workloadPriorityClass` name.
This label is always mutable, as it may be useful for preemption.
For more details, see [Workload Priority Class](/docs/concepts/workload_priority_class/).

### kueue.x-k8s.io/queue-name

Type: Label

Example: `kueue.x-k8s.io/queue-name: "my-local-queue"`

Used on: Kueue-managed Jobs.

The label key in the workload holds the queue name.

Note: Read more about [Setup LocalQueueDefauling](/docs/tasks/manage/enforce_job_management/setup_default_local_queue/)
in order to automatically set the `kueue.x-k8s.io/queue-name` label.

### kueue.x-k8s.io/retriable-in-group

Type: Annotation

Example: `kueue.x-k8s.io/retriable-in-group: "false"`

Used on: [Plain Pods](/docs/tasks/run/plain_pods/).

The annotation key is used to finalize the group if at least one terminated Pod (either Failed or Succeeded)
has the `retriable-in-group: false` annotation.

### kueue.x-k8s.io/role-hash

Type: Annotation

Example: `kueue.x-k8s.io/role-hash: "b54683bb"`

Used on: [Plain Pods](/docs/tasks/run/plain_pods/).

The annotation key is used as the name for a Workload podSet.

### kueue.x-k8s.io/component-workload-index

Type: Annotation

Example: `kueue.x-k8s.io/component-workload-index: "0"`

Used on: [Workload](/docs/concepts/workload/).

The annotation stores the numeric index for component workloads created by multi-workload jobs
(e.g., LeaderWorkerSet creates one workload per replica). Used by MultiKueue to determine
primary workload ordering when dispatching component workloads to worker clusters atomically.
