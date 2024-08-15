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
  - [How to use WorkloadPriorityClass on MPIJob](#how-to-use-workloadpriorityclass-on-mpijob)
  - [How workloads are created from Jobs](#how-workloads-are-created-from-jobs)
    - [1. A job specifies both <code>workload's priority</code> and <code>pod's priority</code>](#1-a-job-specifies-both-workloads-priority-and-pods-priority)
    - [2. A job specifies only <code>workload's priority</code>](#2-a-job-specifies-only-workloads-priority)
    - [3. A job specifies only <code>pod's priority</code>](#3-a-job-specifies-only-pods-priority)
    - [4. A jobFramework specifies both <code>workload's priority</code> and <code>priorityClass</code>](#4-a-jobframework-specifies-both-workloads-priority-and-priorityclass)
    - [5. A jobFramework specifies only <code>workload's priority</code>](#5-a-jobframework-specifies-only-workloads-priority)
    - [6. A jobFramework specifies only <code>priorityClass</code>](#6-a-jobframework-specifies-only-priorityclass)
  - [Where workload's Priority is used](#where-workloads-priority-is-used)
  - [Workload's priority values are always mutable](#workloads-priority-values-are-always-mutable)
  - [What happens when a user changes the priority of <code>workloadPriorityClass</code>?](#what-happens-when-a-user-changes-the-priority-of-workloadpriorityclass)
  - [Validation webhook](#validation-webhook)
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
The priority value is a part of the workload spec. The priority field of workload is mutable.  
In this document, the term `workload Priority` is used to refer
to the priority utilized by Kueue controller for managing the queueing
and preemption of workloads.  
The term `pod Priority` is used to denote the priority utilized by the
kube-scheduler for preempting pods.

## Motivation

Currently, some proposals are submitted for the kueue scheduling order.
However, under the current implementation, the priority of the `Workload` is tied to the priority of the pod. Therefore, it's not possible to change only the priority of the `Workload`. We don't want to change the pod priority because pod's priority is tied to pod preemption. We need a mechanism where users can freely modify the priority of the `Workload` alone, not affecting pod priority.

### Goals

Implement `WorkloadPriorityClass`. `Workload` can utilize `WorkloadPriorityClass`.  
JobFrameworks like Job, MPIJob etc specify the `WorkloadPriorityClass` through labels.  
Users can modify the priority of a `Workload` by changing `Workload`'s priority directly.

### Non-Goals

Using existing k8s Pod's `PriorityClass` for Workload's priority is not recommended.  
`WorkloadPriorityClass` doesn't implement all the features of the k8s Pod's `PriorityClass`
because some fields on the k8s `PriorityClass` are not relevant to Kueue.  
When creating a new `WorkloadPriorityClass`, there is no need to create other CRDs owned by `WorkloadPriorityClass`. Therefore, the reconcile functionality is unnecessary. The `WorkloadPriorityClass` controller will not be implemented for now.

## Proposal

In this proposal, `WorkloadPriorityClass` is defined.  
The `Workload` is able to utilize this `WorkloadPriorityClass`.  
`WorkloadPriorityClass` is independent from pod's priority.  
`Priority`, `PriorityClassName` and `PriorityClassSource` fields will be part of the workload spec.
`Priority` field of `workload` is always mutable because it might be useful for the preemption.  
Workload's `PriorityClassSource` and `PriorityClassName` fields are immutable for simplicity. 
JobFrameworks like Job, MPIJob etc specify the `WorkloadPriorityClass` through labels.

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
We should document the risks of pod preemption to use.  
We can also point users to create `PriorityClass` for their pods that are [non-preempting](https://kubernetes.io/docs/concepts/scheduling-eviction/pod-priority-preemption/#non-preempting-priority-class).  
If a workload's priority is high and pod's priority is low and the kube-scheduler initiates preemption, the pod's priority is prioritized. To prevent this behavior, non-preempting setting is needed.


## Design Details

### Kueue WorkloadPriorityClass API

We introduce the `WorkloadPriorityClass` API.

```golang
type WorkloadPriorityClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Value int32   `json:"value"`
	Description string `json:"description,omitempty"`
}
```

Also `PriorityClassSource` field is added to `WorkloadSpec`.  
The `PriorityClass` field can accept both Pod's `PriorityClass` and `WorkloadPriorityClass` names as values.
To distinguish, when using `WorkloadPriorityClass`, a `PriorityClassSource` field has the `kueue.x-k8s.io/workloadpriorityclass` value.
When using k8s Pod's `PriorityClass`, a `priorityClassSource` field has the `scheduling.k8s.io/priorityclass` value.

```golang
type WorkloadSpec struct {
  ...
  PriorityClassSource string `json:"priorityClassSource,omitempty"`
  ...
}
```

### How to use WorkloadPriorityClass on Job

The `workloadPriorityClass` is specified through a label `kueue.x-k8s.io/priority-class`.
This label is always mutable because it might be useful for the preemption.

```yaml
# sample-priority-class.yaml
apiVersion: kueue.x-k8s.io/v1beta1
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
  name: sample-job
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
The `PriorityClassName` field can accept either `PriorityClass` or `workloadPriorityClass` name as a value.
To distinguish, when using `WorkloadPriorityClass`, a `priorityClassSource` field has the `kueue.x-k8s.io/workloadpriorityclass` value.
When using `PriorityClass`, a `priorityClassSource` field has the `scheduling.k8s.io/priorityclass` value.

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: Workload
metadata:
  name: job-sample-job-7f173
spec:
  priorityClassSource: kueue.x-k8s.io/workloadpriorityclass
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

### How to use WorkloadPriorityClass on MPIJob

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

1. A job specifies both `workload's priority` and `pod's priority`
2. A job specifies only `workload's priority`
3. A job specifies only `pod's priority`

In the case of jobFrameworks, the following scenarios are considered. For jobFrameworks, the `priorityClass` is intended to reflect the `pod's priority`. Therefore, `workloadPriorityClass` is used for the `workload's priority` if jobFramework has both `workloadPriorityClass` and `priorityClass`.

4. A jobFramework specifies both `workload's priority` and `priorityClass`
5. A jobFramework specifies only `workload's priority`
6. A jobFramework specifies only `priorityClass`

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

#### 4. A jobFramework specifies both `workload's priority` and `priorityClass`

When creating this yaml, the `workloadPriorityClass` sample-priority is used for the `workload's priority`.

```yaml
apiVersion: kubeflow.org/v2beta1
kind: MPIJob
metadata:
  name: pi
  labels:
    kueue.x-k8s.io/queue-name: user-queue
    kueue.x-k8s.io/priority-class: sample-priority
spec:
  slotsPerWorker: 1
  runPolicy:
    cleanPodPolicy: Running
    ttlSecondsAfterFinished: 60
    schedulingPolicy:
      priorityClass: high-priority
  sshAuthMountPath: /home/mpiuser/.ssh
  mpiReplicaSpecs:
    Launcher:
      replicas: 1
      template:
        spec:
          containers:
          - image: mpioperator/mpi-pi:openmpi
            name: mpi-launcher
            securityContext:
              runAsUser: 1000
            command:
            - mpirun
            args:
            - -n
            - "2"
            - /home/mpiuser/pi
            resources:
              limits:
                cpu: 1
                memory: 1Gi
```

#### 5. A jobFramework specifies only `workload's priority`

When creating this yaml, the `workloadPriorityClass` sample-priority is used for the `workload's priority`.

```yaml
apiVersion: kubeflow.org/v2beta1
kind: MPIJob
metadata:
  name: pi
  labels:
    kueue.x-k8s.io/queue-name: user-queue
    kueue.x-k8s.io/priority-class: sample-priority
spec:
  slotsPerWorker: 1
  runPolicy:
    cleanPodPolicy: Running
    ttlSecondsAfterFinished: 60
  sshAuthMountPath: /home/mpiuser/.ssh
  mpiReplicaSpecs:
    Launcher:
      replicas: 1
      template:
        spec:
          containers:
          - image: mpioperator/mpi-pi:openmpi
            name: mpi-launcher
            securityContext:
              runAsUser: 1000
            command:
            - mpirun
            args:
            - -n
            - "2"
            - /home/mpiuser/pi
            resources:
              limits:
                cpu: 1
                memory: 1Gi
```

#### 6. A jobFramework specifies only `priorityClass`

When creating this yaml, the `PriorityClass` high-priority is used for the `workload's priority`.
This is basically same as current implementation of workload.

```yaml
apiVersion: kubeflow.org/v2beta1
kind: MPIJob
metadata:
  name: pi
  labels:
    kueue.x-k8s.io/queue-name: user-queue
spec:
  slotsPerWorker: 1
  runPolicy:
    cleanPodPolicy: Running
    ttlSecondsAfterFinished: 60
    schedulingPolicy:
      priorityClass: high-priority
  sshAuthMountPath: /home/mpiuser/.ssh
  mpiReplicaSpecs:
    Launcher:
      replicas: 1
      template:
        spec:
          containers:
          - image: mpioperator/mpi-pi:openmpi
            name: mpi-launcher
            securityContext:
              runAsUser: 1000
            command:
            - mpirun
            args:
            - -n
            - "2"
            - /home/mpiuser/pi
            resources:
              limits:
                cpu: 1
                memory: 1Gi
```

### Where workload's Priority is used

The priority of workloads is utilized in queuing, preemption, and other scheduling processes in Kueue.
With the introduction of `workloadPriorityClass`, there is no change in the places where priority is used in Kueue.
It just enables the usage of `workloadPriorityClass` as the priority.

### Workload's priority values are always mutable

Workload's `Priority` field is always mutable because it might be useful for the preemption.  
Workload's `PriorityClassSource` and `PriorityClassName` fields are immutable for simplicity.  
By the way, there is an [open KEP](https://github.com/kubernetes/enhancements/pull/4129) to make `PriorityClass` mutable in k8s. This `workload`'s design aligns with the direction of k8s `PriorityClass`.

### What happens when a user changes the priority of `workloadPriorityClass`?

The priority of existing workloads isn't altered even if a priority of `workloadPriorityClass` has been updated. This is because users would like to modify priorities for individual workloads, as mentioned in [Story 2](#story-2).
For newly created workloads, their priorities is based on the latest priority value of `workloadPriorityClass`.
As a result, even if there is a change in the value of workloadPriorityClass, the reconciliation process for workload controller doesn't change the priority of existing workloads.

### Validation webhook

By introducing workload webhook, it makes the `workloadPriorityClass` field and `workloadPrioritySource` in the workload CRD immutable.  
Also, by introducing job's webhook, it makes the `workloadPriorityClass` label of jobs immutable.

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
- Controller and webhook tests related to `Workload`
- Integration tests for job controller where the existing integration tests already cover `PriorityClass`
- e2e tests where the existing tests already cover `PriorityClass`

### Graduation Criteria


## Implementation History


## Drawbacks


## Alternatives