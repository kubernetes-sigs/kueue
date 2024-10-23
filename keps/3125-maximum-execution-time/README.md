# KEP-3125: Workload Maximum Execution Time

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
  - [Notes/Constraints/Caveats (Optional)](#notesconstraintscaveats-optional)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API](#api)
    - [Workload Spec](#workload-spec)
    - [Workload Status](#workload-status)
  - [Constants](#constants)
  - [Controller](#controller)
    - [Workload](#workload)
    - [Jobs / Jobframework](#jobs--jobframework)
  - [Webhooks](#webhooks)
    - [Workload](#workload-1)
    - [Jobs](#jobs)
  - [Test Plan](#test-plan)
      - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
      - [controller/core/workload](#controllercoreworkload)
      - [controller/jobs/job](#controllerjobsjob)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

Add the ability to set a maximum execution time of a Job.

## Motivation

Extend Kueue so that kjobctl can incorporate functionalities equivalent to `-t` in [slurm](https://slurm.schedmd.com/).

Different Jobs CRDs have a field with similar semantics, but there is no standard. For example `batch/Job` has `spec.activeDeadlneSeconds`, while JobSet does not have such an API for now.

### Goals

Introduce a Job agnostic API of setting and enforcing the maximum execution time of a Job.


### Non-Goals

- Change the behavior of the Kueue's scheduler based on the maximum execution time.
- Mitigate potential conflicting configurations between `kueue.x-k8s.io/max-exec-time-seconds` and other job specific `activeDeadline` functionalities.

## Proposal

A new Kueue specific label is added `kueue.x-k8s.io/max-exec-time-seconds` containing
the maximum number of seconds the Job is expected to execute.

The duration is passed to the Job's Workload.

If a workload stays admitted for longer that it is expected it will be Deactivated. 

### User Stories (Optional)

#### Story 1

As a Batch User, I want to have the ability to set a limit on how long my job can run.

### Notes/Constraints/Caveats (Optional)


### Risks and Mitigations

## Design Details

### API

#### Workload Spec

Add a new optional field to hold the 

```go
// WorkloadSpec defines the desired state of Workload
// +kubebuilder:validation:XValidation:rule="has(self.priorityClassName) ? has(self.priority) : true", message="priority should not be nil when priorityClassName is set"
type WorkloadSpec struct {
	// ...

	// MaximumExecutionTimeSeconds if provided, determines the maximum time, in seconds, the workload can be admitted  
	// befrore it's automatically deactivated.
    // +optional
	MaximumExecutionTimeSeconds *int32 `json:"maximumExecutionTimeSeconds,omitempty"`
}
```

#### Workload Status

Add a optional field to hold the 
```go
// WorkloadStatus defines the observed state of Workload
type WorkloadStatus struct {
	// ...

	// AccumulatedPastExecutionTimeSeconds holds the total duration the workload spent in Admitted state
	// in the previous `Admit` - `Evict` cycles.
    // +optional
	AccumulatedPastExexcutionTimeSecond *int32 `json:"accumulatedPastExexcutionTimeSeconds,omitempty"`
}

```

### Constants

Define a new constant holding the new label name `kueue.x-k8s.io/max-exec-time-seconds`.

### Controller

#### Workload
During reconcile:

- When a workload transitions out of `Admitted` state, add the time spent in its `status.AccumulatedPastExecutionTimeSeconds`
```go
cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadAdmitted)
wl.Status.AccumulatedPastExecutionTimeSeconds += wl.Spec.MaximumExecutionTimeSeconds -
    time.Now().Sub(cond.LastTransitionTime.Time).Seconds()
```
- When the workload is admitted and a maximum execution time is specified compute the remaining execution execution time as:
```go
if wl.Spec.MaximumExecutionTimeSeconds != nil {
	if cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadAdmitted); cond != nil && cond.Status == metav1.ConditionTrue {
		remainingTime := wl.Spec.MaximumExecutionTimeSeconds -
            wl.Status.AccumulatedPastExecutionTimeSeconds -
            time.Now().Sub(cond.LastTransitionTime.Time).Seconds()
	}
}
```
  - If `remainingTime` > 0 , a reconcile request should be queued with a `remainingTime` delay.
  - If `remainingTime` <= 0 , the workload's deactivation is triggered, the `wl.status.accumulatedPastExecutionTimeSeconds` set to 0, and a relevant event recorded.

#### Jobs / Jobframework

Upon workload creation, the value from `kueue.x-k8s.io/max-exec-time-seconds` is converted to `int32` and set into the new workload's `spec`.

### Webhooks

#### Workload

If specified, validate that the value of `wl.spec.maximumExecutionTimeSeconds` is greater then 0.

Validate that `wl.spec.maximumExecutionTimeSeconds` is immutable, while the workload is admitted.

#### Jobs

Validate that the value provided in the `kueue.x-k8s.io/max-exec-time-seconds` label is the base 10 representation of an integer greater then 0.

The value of the `kueue.x-k8s.io/max-exec-time-seconds` label is immutable, while the job is unsuspended.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

##### Prerequisite testing updates

No regressions in the existing tests.

#### Unit Tests

As needed to provide coverage fore the new code.

-  pkg/controller/core: 2024-09-25 - 9.9%
-  pkg/controller/jobframework: 2024-09-25 - 1.9%
-  pkg/controller/jobs/deployment: 2024-09-25 - 0.4%
-  pkg/controller/jobs/job: 2024-09-25 - 8.5%
-  pkg/controller/jobs/jobset: 2024-09-25 - 4.2%
-  pkg/controller/jobs/kubeflow/jobs/mxjob: 2024-09-25 - 3.6%
-  pkg/controller/jobs/kubeflow/jobs/paddlejob: 2024-09-25 - 3.5%
-  pkg/controller/jobs/kubeflow/jobs/pytorchjob: 2024-09-25 - 0.7%
-  pkg/controller/jobs/kubeflow/jobs/tfjob: 2024-09-25 - 0.7%
-  pkg/controller/jobs/kubeflow/jobs/xgboostjob: 2024-09-25 - 3.5%
-  pkg/controller/jobs/kubeflow/kubeflowjob: 2024-09-25 - 0.0%
-  pkg/controller/jobs/mpijob: 2024-09-25 - 2.2%
-  pkg/controller/jobs/pod: 2024-09-25 - 8.9%
-  pkg/controller/jobs/raycluster: 2024-09-25 - 3.8%
-  pkg/controller/jobs/rayjob: 2024-09-25 - 1.2%
-  pkg/webhooks: 2024-09-25 - 2.1%

#### Integration tests

##### controller/core/workload

Add "An admitted workload is deactivated when its maximum execution time expires"

##### controller/jobs/job

Add "A job is suspended when its maximum execution time expires"

### Graduation Criteria


## Implementation History


## Drawbacks


## Alternatives

