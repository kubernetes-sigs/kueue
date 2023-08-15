# KEP-973: Workload priority

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Kueue WorkloadPriorityClass API](#kueue-workloadpriorityclass-api)
  - [How to use WorkloadPriorityClass on Job](#how-to-use-workloadpriorityclass-on-job)
  - [How to use WorkloadPriorityCLass on MPIJob](#how-to-use-workloadpriorityclass-on-mpijob)
  - [How workloads are created from Jobs](#how-workloads-are-created-from-jobs)
    - [1. A job specifies both <code>workload's priority</code> and <code>pod's priority</code>](#1-a-job-specifies-both--and-)
    - [2. A job specifies only <code>workload's priority</code>](#2-a-job-specifies-only-)
    - [3. A job specifies only <code>pod's priority</code>](#3-a-job-specifies-only-)
  - [How to expand Priority utility](#how-to-expand-priority-utility)
  - [Where workload's Priority is used](#where-workloads-priority-is-used)
  - [Role of workloadPriorityClass controller](#role-of-workloadpriorityclass-controller)
  - [What happens when a user changes the priority of <code>workloadPriorityClass</code>?](#what-happens-when-a-user-changes-the-priority-of-)
  - [Future works](#future-works)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

In this proposal, a `WorkloadPriorityClass` is created.
The `Workload` is able to utilize `WorkloadPriorityClass`.
`WorkloadPriorityClass` is independent from pod's priority.
The priority value is a part of the workload spec and is mutable.  
In this document, the term "workload Priority" is used to refer
to the priority utilized by Kueue controller for managing the queueing
and preemption of workloads.  
The term "pod Priority" is used to denote the priority utilized by the
kube-scheduler for preempting pods and jobs.

## Motivation

Currently, some proposals are submitted for the kueue scheduling order.
However, under the current implementation, the priority of the `Workload` is tied to the priority of the pod. Therefore, it's not possible to change only the priority of the `Workload`. We don't want to change the pod priority because pod's priority is tied to pod preemption. We need a mechanism where users can freely modify the priority of the `Workload` alone, not affecting pod priority.

### Goals

Implement `WorkloadPriorityClass`. `Workload` can utilize `WorkloadPriorityClass`.  
CRDs like Job, MPIJob etc specify the `WorkloadPriorityClass` through labels.  
Users can modify the priority of a `Workload` by changing `Workload`'s priority directly.

### Non-Goals

Using existing k8s `PriorityClass` for Workload's Priority is not recommended.  
`WorkloadPriorityClass` doesn't implement all the features of the k8s `PriorityClass`
because some fields on the k8s `PriorityClass` are not relevant to Kueue.

## Proposal

In this proposal, `WorkloadPriorityClass` is defined.
The `Workload` is able to utilize this `WorkloadPriorityClass`.
`WorkloadPriorityClass` is independent from pod's priority.
The priority value will be part of the workload spec and be mutable.
CRDs like Job, MPIJob etc specify the `WorkloadPriorityClass` through labels.

<!--
This is where we get down to the specifics of what the proposal actually is.
This should have enough detail that reviewers can understand exactly what
you're proposing, but should not include things like API designs or
implementation. What is the desired outcome and how do we measure success?.
The "Design Details" section below is for the real
nitty-gritty.
-->

### User Stories

Kueue issue [973](https://github.com/kubernetes-sigs/kueue/issues/973) provides details on the initial feature implementation.

#### Story 1

In an organization, admins want to set a lower priority for development workloads and a higher priority for production workloads.
In such cases, they create two `WorkloadPriorityClass` and apply each one to the respective workloads.

#### Story 2

An organization desires to modify the priority of workloads that remain inactive for a specific duration.
By developing a custom controller to manage Priority value of `Workload` spec, this expectation can be met.

### Risks and Mitigations

It's possible that the pod's priority conflicts with the workload's priority.
For example, a high-priority job with low-priority pods may never run to completion because it may always be preempted by kube-scheduler.
We should document the risks of pod preemption to uses.
We can also point users to create `PriorityClass` for their pods that are [non-preempting](https://kubernetes.io/docs/concepts/scheduling-eviction/pod-priority-preemption/#non-preempting-priority-class).
If a workload's priority is high and pod's priority is low and the kube-scheduler initiates preemption, the pod's priority is prioritized. To prevent this behavior, non-preempting setting is needed.


## Design Details

### Kueue WorkloadPriorityClass API

We introduce the `WorkloadPriorityClass` API.

```golang
type WorkloadPriorityClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Value int32   `json:"value,omitempty"`
	Description string `json:"description,omitempty"`
}
```

Also `priorityClassSource` field is added to `WorkloadSpec`.  
The `PriorityClass` field can accept both k8s `PriorityClass` and `workloadPriorityClass` as values.
To distinguish, when using `workloadPriorityClass`, a `priorityClassSource` field has the "kueue.x-k8s.io/WorkloadPriorityClass" value.
When using k8s `PriorityClass`, a `priorityClassSource` field has the "scheduling.k8s.io/PriorityClass" value.

```golang
type WorkloadSpec struct {
  ...
  PriorityClassSource string `json:"priorityClassSource,omitempty"`
  ...
}
```

### How to use WorkloadPriorityClass on Job

The `workloadPriorityClass` is specified through a label `kueue.x-k8s.io/priority-class`.

```yaml
# sample-priority-class.yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: WorkloadPriorityClass
metadata:
  name: sample-priority
value: 10000
description: "Sample priority"
---
# sample-job.yaml
apiVersion: batch/v1
kind: Job
metadata:
  generateName: sample-job-
  labels:
    kueue.x-k8s.io/queue-name: user-queue
    kueue.x-k8s.io/priority-class: sample-priority
spec:
  parallelism: 3
  completions: 3
  suspend: true
  template:
    spec:
      containers:
      - name: dummy-job
        image: gcr.io/k8s-staging-perf-tests/sleep:latest
      restartPolicy: Never
```

The following workload is generated by the yaml above.
The `PriorityClass` field can accept both k8s `PriorityClass` and `workloadPriorityClass` as values.
To distinguish, when using `workloadPriorityClass`, a `priorityClassSource` field has the "kueue.x-k8s.io/WorkloadPriorityClass" value.
When using k8s `PriorityClass`, a `priorityClassSource` field has the "scheduling.k8s.io/PriorityClass" value.

```yaml
# sample-workload.yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: Workload
metadata:
  name: job-sample-job-jf5fb-f5982
spec:
  priorityClassSource: kueue.x-k8s.io/WorkloadPriorityClass
  priorityClassName: sample-priority
  priority: 10000
  queueName: user-queue
  podSets:
  - count: 3
    name: dummy-job
    template:
      spec:
        containers:
        - image: gcr.io/k8s-staging-perf-tests/sleep:latest
          name: dummy-job
```

In this example, since the `WorkloadPriorityClassName` of `sample-job` is set to `sample-priority`, the `priority` of the `sample-job` will be set to 10,000.
During queuing and preemption of the workload, this priority value will be used in the calculations.

### How to use WorkloadPriorityCLass on MPIJob

The `workloadPriorityClass` is specified through a label `kueue.x-k8s.io/priority-class`.
This is same as other CRDs like `RayJob`.

```yaml
apiVersion: kubeflow.org/v2beta1
kind: MPIJob
metadata:
  name: pi
  labels:
    kueue.x-k8s.io/queue-name: user-queue
    kueue.x-k8s.io/priority-class: sample-priority
spec:
.....
```

### How workloads are created from Jobs

There are three scenarios for creating a workload from a job.
The same applies to CRDs other than `Job` (such as `RayJob`).

1. A job specifies both `workload's priority` and `pod's priority`
2. A job specifies only `workload's priority`
3. A job specifies only `pod's priority`

#### 1. A job specifies both `workload's priority` and `pod's priority`

When creating this yaml, the `workloadPriorityClass` sample-priority is used for the `workload's priority`.
On the other hand, the `priorityClass` high-priority is used for the `pod's priority`.
```yaml
apiVersion: batch/v1
kind: Job
metadata:
  generateName: sample-job-
  labels:
    kueue.x-k8s.io/queue-name: user-queue
    kueue.x-k8s.io/priority-class: sample-priority
spec:
  priorityClassName: high-priority
  parallelism: 3
  completions: 3
  suspend: true
  template:
    spec:
      containers:
      - name: dummy-job
        image: gcr.io/k8s-staging-perf-tests/sleep:latest
      restartPolicy: Never
```

#### 2. A job specifies only `workload's priority`

When creating this yaml, the `workloadPriorityClass` sample-priority is used for the `workload's priority`.

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  generateName: sample-job-
  labels:
    kueue.x-k8s.io/queue-name: user-queue
    kueue.x-k8s.io/priority-class: sample-priority
spec:
  parallelism: 3
  completions: 3
  suspend: true
  template:
    spec:
      containers:
      - name: dummy-job
        image: gcr.io/k8s-staging-perf-tests/sleep:latest
      restartPolicy: Never
```

#### 3. A job specifies only `pod's priority`

When creating this yaml, the `PriorityClass` high-priority is used for the `workload's priority`.
This is basically same as current implementation of workload.

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  generateName: sample-job-
  labels:
    kueue.x-k8s.io/queue-name: user-queue
spec:
  priorityClassName: high-priority
  parallelism: 3
  completions: 3
  suspend: true
  template:
    spec:
      containers:
      - name: dummy-job
        image: gcr.io/k8s-staging-perf-tests/sleep:latest
      restartPolicy: Never
```

### How to expand Priority utility

When referencing the `priorityClass` in the [Priority utility](https://github.com/kubernetes-sigs/kueue/blob/ba404ad282c35cf1d6b15d07643935fffbcc1835/pkg/util/priority/priority.go) function on workload, first we check the `PriorityClassSource` field.
If `PriorityClassSource` has the value `kueue.x-k8s.io/WorkloadPriorityClass`, `workloadPriorityClass` is searched.
If `PriorityClassSource` has the value `scheduling.k8s.io/PriorityClass`, `priorityClass` is searched.

### Where workload's Priority is used

The priority of workloads is utilized in queuing, preemption, and other scheduling processes in Kueue.
With the introduction of `workloadPriorityClass`, there is no change in the places where priority is used in Kueue.
It just enables the usage of `workloadPriorityClass` as the priority.

### Role of workloadPriorityClass controller

The workloadPriorityClass Controller only reconcile the `workloadPriorityClass`.

### What happens when a user changes the priority of `workloadPriorityClass`?

The priority of existing workloads isn't altered even if a priority of `workloadPriorityClass` has been updated. This is because users would like to modify priorities for individual workloads, as mentioned in [Story 2](#story-2).
For newly created workloads, their priorities is based on the latest priority value of `workloadPriorityClass`.
As a result, even if there is a change in the value of workloadPriorityClass, the reconciliation process for workload controller doesn't change the priority of existing workloads.

### Future works

In the future, we plan to enable each organization using Kueue to customize the priority values according to their specific requirements through CRDs defined by each organization.

### Test Plan

No regressions in the current test should be observed.

[X] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Unit Tests

This change should be covered by unit tests.

#### Integration tests

The following scenarios will be covered with integration tests where `WorkloadPriorityClass` is used:
- Controller tests related to job interfaces such as job_controller, mpi_job_controller, etc
- Integration tests for scheduler and webhook where the existing integration tests already cover `PriorityClass`
- Integration tests for workloadPriorityClass_controller

### Graduation Criteria


## Implementation History


## Drawbacks


## Alternatives