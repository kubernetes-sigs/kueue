# KEP-754: Time-dependent job priority

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
  - [How to use WorkloadPriorityClass on Job](#how-to-use-workloadpriorityclass-on-job)
  - [Priority Calculation](#priority-calculation)
  - [Other points](#other-points)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

This proposal allows job priorities to be dynamically adjusted based on time.  
This functionality aims to prioritize the execution of jobs that have been waiting 
for an extended period, even if they have a lower initial priority.  
By introducing time-dependent priority adjustments, the system can ensure that jobs 
that have been deferred receive the attention they require.  
This KEP outlines the necessary changes and considerations to incorporate this 
time-based priority mechanism.

## Motivation

Lower-priority jobs sometimes get postponed for long time than expected.  
To address situations where jobs remain unexecuted even after a certain period of time,
we implementate this KEP.

### Goals

Provide a mechanism where the priority of a job increase after a certain period of
time has elapsed.

### Non-Goals

The timed out job is going to the head of the ClusterQueue instead of changing 
the priority.

## Proposal

Replace the priority of Jobs with a calculation that takes into account the time since the Job was created.  
This replacement will modify the calculation methods for Preemption and Queueing strategy.  
The priority of Job that don't have `WorkloadPriorityClass` will remain unchanged.

<!--
This is where we get down to the specifics of what the proposal actually is.
This should have enough detail that reviewers can understand exactly what
you're proposing, but should not include things like API designs or
implementation. What is the desired outcome and how do we measure success?.
The "Design Details" section below is for the real
nitty-gritty.
-->

### User Stories

#### Story 1

The job doesn't have a high priority but is intended for production, so it needs to start execution within three hours.


### Risks and Mitigations

If the job is just popped out for scheduling, the running job would be preempted easily by other Jobs.  
That's why this mechanism should be based on priority of job.

## Design Details

### Kueue WorkloadPriorityClass API

We introduce the Kueue `WorkloadPriorityClass` API.

```golang
// WorkloadPriorityClass defines basePriority and time dependent calculation logic
type WorkloadPriorityClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkloadPrioritySpec   `json:"spec,omitempty"`  
}

type WorkloadPrioritySpec struct {
  // initial priority of workload
	Priority int32 `json:"priority,omitempty"`

  // +optional
  DynamicPriorityPolicy DynamicPriorityPolicy `json:"dynamicPriorityPolicy,omitempty"`

}

type DynamicPriorityPolicy struct {

  // step is a value that is added to the basePriority every time the duration defined by delayForStep elapses. 
  Step int32 `json:"step,omitempty"`

  // max defines the maximum number of Priority. We don't want it to go to infinity.
  Max int32 `json:"max,omitempty"`

  // delayForStep indicates the time to wait before changing the priority after a timeout
  DeleyForStep time.Duration `json:"delayForStep,omitempty"`

}

```

### How to use WorkloadPriorityClass on Job
The `WorkloadPriorityClass` for a Job is defined using labels instead of `PriorityClass`.  
We do not use the `spec.PriorityClass` field for `Job` because `Kueue` wouldn't manage `spec.PriorityClass`.

```yaml
# sample-job.yaml
apiVersion: batch/v1
kind: Job
metadata:
  generateName: sample-job-
  labels:
    kueue.x-k8s.io/queue-name: user-queue
    kueue.x-k8s.io/workload-priority-class: sample-priority
```

### Priority Calculation

Priority is calculated based on the following equation:
```
Priority = min(priority + int(ceil(elapsedTime / delayForStep)) * step, max)
```

Let's calculate the priority for the following example:
```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: WorkloadPriorityClass
metadata:
  name: sample-priority
Spec:
  priority: 100
  dynamicPriorityPolicy:
    step: 400
    max: 1000
    delayForStep: 1h
```

Calculation:

```
Priority when the job is created: 100
Priority after 1 hour: 100+400=500
Priority after 2 hour: 100+400*2=900
Priority after 3 hour: min(100+400*3, 1000)=1000
...
Priority after n hour: min(100+400*n, 1000)=1000
```

### Other points
If a Job has both the `spec.PriorityClass` and WorkloadPriorityClass defined, `spec.PriorityClass` is ignored.  
Since the `creationTimeStamp` of the Job is always available, the calculation can be performed every time
without updating the values of Job and Workload Priority.


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

