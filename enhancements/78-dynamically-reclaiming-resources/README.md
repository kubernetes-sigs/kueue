# Dynamically reclaim resources of Pods of a Workload

## Table of Contents

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
    - [Goals](#goals)
    - [Non-Goals](#non-goals)
- [Proposal](#proposal)
    - [Risks and Mitigations](#risks-and-mitigations)
- [API](#api)
- [Graduation Criteria](#graduation-criteria)
- [Testing Plan](#testing-plan)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [E2E tests](#e2e-tests)
- [Implementation History](#implementation-history)
<!-- /toc -->

## Summary

The resources of pods of a Workload are reclaimed by kueue after successful completion of the Pod. 
This is beneficial for workloads which cannot proceed due to the unavailability 
of the little resources for its corresponding pods and Workload does not get 
admitted.

Here after, *pods of a Workload* are synonymously used to refer *pods of a Job* and vice-versa because, 
Job manages the Workload and the pods associated with Job are associated with the Workload. 


## Motivation

Currently, the resources owned by a Job are reclaimed by kueue only when the whole Job finishes.
For jobs with multiple pods, the resources will be reclaimed only after the last Pod
of the Job finishes. This is not efficient as the pods of parallel Job may have laggards
consuming the unused resources of the jobs those are currently executing.

### Goals

- Utilize the unused resources of the Job which running in kueue.

### Non-Goals

- A running Job on queue should not be preempted.
- The resources that are being used by a Job should not be allocated to any other Job.

## Proposal

Reclaim the resources of the succeeded pods of a Job as soon as it completes its execution.

Add a new field to the status of the Workload to count successful completion of 
pods of the Workload. The Job controller monitors the count of pods which have
successfully completed its execution. The Job controller is responsible for updating
the status of the Workload object appropriately.

Update our documentation to reflect the new enhancement.

### Risks and Mitigations

There is a change in the functionality of reclaiming the cluster-queue resources.
The resources of a Job are reclaimed by kueue in an incremental manner(resources of each
Pod of a Job are reclaimed separately), as opposed to reclaiming the whole resources 
of the Job after its completion.

The update logic to reclaim resources after a Job completion must be handled correctly.

### API

A new field `CompletedPods` is added to the `WorkloadStatus` struct.

```go
// WorkloadStatus defines the observed state of Workload
type WorkloadStatus struct {
  // conditions hold the latest available observations of the Workload
  // current state.
  // +optional
  // +listType=map
  // +listMapKey=type
  Conditions []WorkloadCondition `json:"conditions,omitempty"`
  
  // The number of pods which reached phase Succeeded or Failed.
  // +optional
  CompletedPods int32 `json:"completedPods"`
}
```

#### CompletedPods

`CompletedPods` stores the count of pods which have successfully completed its execution.

High level execution flow:
1. The completed pods in a Job is the count of Succeeded pods => sum(succeeded).
2. Update the Workload status object if job.sum(Succeeded) > wl.Status.CompletedPods
3. Handle the update event of Workload and update Workload status in cache.
4. Update clusterQueue quota for resource flavor for requests of Pod of a Workload in cache.

## Graduation Criteria

* The features have been stable and reliable in the past several releases.
* Adequate documentation exists for the features.
* Test coverage of the features is acceptable.

## Testing Plan

Dynamically reclaiming resources enhancement has unit and integration tests. These tests
are run regularly as a part of kueue's prow CI/CD pipeline.

### Unit Tests
Here is a list of unit tests for various modules of the feature:
* [Cluster-Queue cache tests](https://github.com/kubernetes-sigs/kueue/blob/main/pkg/cache/cache_test.go)
* [Workload tests](https://github.com/kubernetes-sigs/kueue/blob/main/pkg/workload/workload_test.go)

### Integration tests
* Integration tests for Job controller are [found here](https://github.com/kubernetes-sigs/kueue/blob/main/test/integration/controller/job/job_controller_test.go)
* Integration tests for Workload controller are [found here](https://github.com/kubernetes-sigs/kueue/blob/main/test/integration/controller/core/workload_controller_test.go)

## Implementation History

Dynamically Reclaiming Resources are tracked as part of [enhancement#78](https://github.com/kubernetes-sigs/kueue/issues/78).

**TODO** - Add proposal link