# KEP-973: Workload priority

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Kueue PriorityClass API](#kueue-priorityclass-api)
  - [How to use PriorityClass on Workload](#how-to-use-priorityclass-on-workload)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

In this proposal, a `PriorityClass` for `Workload` will be created.
The `Workload` will be able to utilize this `PriorityClass`.
`Workload`'s `PriorityClass` is independent from pod's priority.
The priority value will be a part of the workload spec and be mutable.

## Motivation

We want to make the priority of the workload customizable according to user's needs.
By defining `Workload`'s `PriorityClass`, we aim to enable various cutomizations only for Kueue's `Workload`.

### Goals

Implement `PriorityClass` for `Workload`.
`Workload` can utilize `Workload`'s `PriorityClass`.

### Non-Goals

Using existing k8s `PriorityClass` for `Workload` is not recommended.

## Proposal

In this proposal, a `PriorityClass` for `Workload` will be created.
The `Workload` will be able to utilize this `PriorityClass`.
`Workload`'s `PriorityClass` is independent from pod's priority.
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

Kueue issue [973](https://github.com/kubernetes-sigs/kueue/issues/973) provides details on the initial feature details.

### Risks and Mitigations

It's possible that pod's priority conflicts with workload's priority.
We sohuld document the risks of pod preemption to uses.
We can also point users to create PriorityClasses for their pods that are non-preempting.

## Design Details

### Kueue PriorityClass API

We introduce the Kueue `PriorityClass` API.

```golang
type PriorityClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Value int32   `json:"value,omitempty"`
  Description string `json:"description,omitempty"`
}

```

### How to use PriorityClass on Workload
The `PriorityClass` for a Workload is defined using `PriorityClassName` field.

```yaml
# sample-priority-class.yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: PriorityClass
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