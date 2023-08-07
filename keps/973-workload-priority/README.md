# KEP-973: Workload priority

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1](#story-1)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Kueue WorkloadPriorityClass API](#kueue-workloadpriorityclass-api)
  - [How to use WorkloadPriorityClass on Workload](#how-to-use-workloadpriorityclass-on-workload)
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

In this proposal, a `WorkloadPriorityClass` will be created.
The `Workload` will be able to utilize this `WorkloadPriorityClass`.
`WorkloadPriorityClass` is independent from pod's priority.
The priority value will be a part of the workload spec and be mutable.

## Motivation

We want to make the priority of the workload customizable according to user's needs.
By defining `WorkloadPriorityClass`, we aim to enable various cutomizations only for Kueue's `Workload`.

### Goals

Implement `WorkloadPriorityClass`.
`Workload` can utilize `WorkloadPriorityClass`.

### Non-Goals

Using existing k8s `PriorityClass` for `Workload` is not recommended.
`WorkloadPriorityClass` doesn't implement all the features of the k8s `PriorityClass` because some fields on the k8s `PriorityClass` are not relevant to Kueue.

## Proposal

In this proposal, a `WorkloadPriorityClass` will be created.
The `Workload` will be able to utilize this `WorkloadPriorityClass`.
`WorkloadPriorityClass` is independent from pod's priority.
The priority value will be part of the workload spec and be mutable.

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

```yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: WorkloadPriorityClass
metadata:
  name: dev-workload-class
value: 100
description: "Dev's priority class"
---
apiVersion: kueue.x-k8s.io/v1alpha1
kind: WorkloadPriorityClass
metadata:
  name: prod-workload-class
value: 10000
description: "Prod's priority class"
---
# dev-workload-a.yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: Workload
metadata:
  name: dev-job-a
spec:
  priorityClassName: dev-workload-class
  queueName: team-a-queue
  podSets:
  - count: 3
    name: main
    template:
      spec:
        containers:
        - image: gcr.io/k8s-staging-perf-tests/sleep:latest
---        
# dev-workload-b.yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: Workload
metadata:
  name: dev-job-b
spec:
  priorityClassName: dev-workload-class
  queueName: team-b-queue
  podSets:
  - count: 3
    name: main
    template:
      spec:
        containers:
        - image: gcr.io/k8s-staging-perf-tests/sleep:latest
# prod-workload-a.yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: Workload
metadata:
  name: prod-job-b
spec:
  priorityClassName: prod-workload-class
  queueName: team-b-queue
  podSets:
  - count: 3
    name: main
    template:
      spec:
        containers:
        - image: gcr.io/k8s-staging-perf-tests/sleep:latest        
```



### Risks and Mitigations

It's possible that the pod's priority conflicts with the workload's priority.
For example, a high-priority job with low-priority pods may never run to completion because it may always be preempted.
We should document the risks of pod preemption to uses.
We can also point users to create `PriorityClass` for their pods that are [non-preempting](https://kubernetes.io/docs/concepts/scheduling-eviction/pod-priority-preemption/#non-preempting-priority-class).

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

### How to use WorkloadPriorityClass on Workload

The `WorkloadPriorityClass` is defined using `PriorityClassName` field.

```yaml
# sample-priority-class.yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: WorkloadPriorityClass
metadata:
  name: sample-priority
value: 10000
description: "Sample priority"
---
# sample-workload.yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: Workload
metadata:
  name: sample-job
spec:
  priorityClassName: sample-priority
  queueName: team-a-queue
  podSets:
  - count: 3
    name: main
    template:
      spec:
        containers:
        - image: gcr.io/k8s-staging-perf-tests/sleep:latest
          imagePullPolicy: Always
          name: container
```

In this example, since the `WorkloadPriorityClassName` of `sample-job` is set to `sample-priority`, the `priority` of the `sample-job` will be set to 10,000.
During queuing and preemption of the workload, this priority value will be used in the calculations.

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

This change should be covered by integration tests.

### Graduation Criteria


## Implementation History


## Drawbacks


## Alternatives