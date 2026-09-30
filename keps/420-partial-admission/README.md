# KEP-420: Allow partial admission of PodSets

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1 (partial admission)](#story-1-partial-admission)
- [Design Details](#design-details)
  - [Enablement](#enablement)
    - [Features](#features)
  - [Workload API](#workload-api)
  - [Validation](#validation)
  - [Scheduler / Flavorassignment](#scheduler--flavorassignment)
    - [Partial Admission for one PodSet](#partial-admission-for-one-podset)
    - [Partial Admission for multiple PodSets](#partial-admission-for-multiple-podsets)
  - [Jobframework](#jobframework)
  - [batch/Job controller](#batchjob-controller)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [E2E tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Future work](#future-work)
  - [kubeflow/MPIJob controller](#kubeflowmpijob-controller)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

Add an optional way of allowing the partial admission of a workload if the full admission is not possible.

## Motivation

In practice, not all Workloads require the parallel execution of all the `count` of a `PodSet`, for such cases having a way to partially reserve the quota in order to prevent starvation.

For example, if a batch/Job has parallelism x and there is only quota available for y < x, then the job could still be admitted if it can work with a lower parallelism.

### Goals

Provide an opt-in way for Workloads to accept the admission with a lower count of pods if the full count is not available.

### Non-Goals

Since this is an opt-in feature, the parent job should accept the partial admission parameters provided by Kueue.

Kueue will not take any measure to ensure that the parent job respects the assigned quota.

## Proposal

Change the way the flavor assigner works to support decrementing the pods count in order to find a better fit for the current workload.
In case a partial fit is chosen, the jobframework reconciler should provide the admitted pod counts to the parent job before unsuspending it, in a similar fashion as the node selectors are provided. In case the job gets suspended, the original pod counts should be restored in order to allow a potential future admission with its original pod counts.

### User Stories

Kueue issue [420](https://github.com/kubernetes-sigs/kueue/issues/420) provides details on the initial feature details and its applicability for `batch/Job`.

#### Story 1 (partial admission)

As a user submitting a regular batch Job (e.g., `batch/v1.Job`), I want the Job to start running even if there are not enough resources in the cluster to satisfy its full requested parallelism. If the Job can work with a lower parallelism (as long as it meets a specified minimum), Kueue should admit it with the available capacity, allowing the Job to proceed instead of waiting in the queue indefinitely.

## Design Details

### Enablement

PartialAdmission in Kueue is enabled through a combination of a Kubernetes feature gate and a `kueue.x-k8s.io/job-min-parallelism` annotation indicating the minimum `parallelism` acceptable by the job in case of partial admission.

#### Features
```go
	// Enables partial admission.
	PartialAdmission featuregate.Feature = "PartialAdmission"
```


### Workload API

```go
// +kubebuilder:validation:XValidation:rule="has(self.minCount) ? self.minCount <= self.count : true", message="minCount should be less or equal to count"
type PodSet struct {
  // .......

  // count is the number of pods requested by the Job.
  // +kubebuilder:validation:Minimum=0
  Count int32 `json:"count"`

  // minCount is the minimum number of pods acceptable for admission in case of partial admission.
  //
  // If not provided, partial admission for the current PodSet is not
  // enabled.
  // +optional
  MinCount *int32 `json:"minCount,omitempty"`
}

type PodSetAssignment struct {
  // ........

  // count is the number of pods taken into account at admission time.
  // This field will not change in case of quota reclaim.
  // Value could be missing for Workloads created before this field was added,
  // in that case spec.podSets[*].count value will be used.
  //
  // +optional
  // +kubebuilder:validation:Minimum=0
  Count *int32 `json:"count,omitempty"`
}
```

### Validation

- `.spec.podSets.minCount <= .spec.podSets.count`
- `.spec.podSets.minCount`
  - Until Kueue v0.19, this have to be `>=1`.
  - Since Kueue v0.20, this have to be `>=0` for KEP-12100.

### Scheduler / Flavorassignment

In case the workload proposed for the current scheduling cycle does not fit, with or without preemption, in the current available quota and any of its PodSets allow partial admission, Kueue will try to find a lower counts combination that fits the available quota with or without borrowing.

#### Partial Admission for one PodSet

The search for appropriate count value is done using binary search algorithm.

#### Partial Admission for multiple PodSets

Currently Partial Admission supports only one PodSet.
However the same approach as the Partial ScaleUp for ElasticJobs for multiple PodSets could be used if needed.

### Jobframework

```diff
type GenericJob interface {
    // ...

	
-    // RunWithNodeAffinity will inject the node affinity extracting from workload to job and unsuspend the job.
+    // RunWithPodSetsInfo will inject the node affinity and podSet counts extracted from workload to the job and unsuspend the job.
-    RunWithNodeAffinity(nodeSelectors []PodSetNodeSelector)
+    RunWithPodSetsInfo(nodeSelectors []PodSetNodeSelector, podSetCounts []int32)
-    // RestoreNodeAffinity will restore the original node affinity of job.
+    // RestorePodSetsInfo will restore the original node affinity of job.
-    RestoreNodeAffinity(nodeSelectors []PodSetNodeSelector)
+    RestorePodSetsInfo(nodeSelectors []PodSetNodeSelector, podSetCounts []int32)

    // ...
}

```

### batch/Job controller

Besides adapting `RunWithPodSetsInfo` and `RestorePodSetsInfo` it should also:

- rework `PodSets()` to populate `MinCount` if the job is marked to support partial admission.
  * jobs supporting partial admission should have a dedicated annotation, e.g., `kueue.x-k8s.io/job-min-parallelism`, indicating the minimum `parallelism` acceptable by the job in case of partial admission.
  * jobs which need the `completions` count kept in sync with `parallelism` should indicate this in a second annotation, `kueue.x-k8s.io/job-completions-equal-parallelism`
- rework `EquivalentToWorkload` to account for potential differences in `PodSets` spec `Parallelism`.

### Test Plan

No regressions in the current tests should be observed.

[X] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Unit Tests

- **Scheduler/Flavor Assignment**:
  - `pkg/scheduler/scheduler_test.go`:
    - `partial admission single variable pod set`: verifies flavor assignment with a single variable count PodSet.
    - `partial admission single variable pod set, preempt first`: verifies preemption behavior when a workload can be admitted using partial admission.
    - `partial admission single variable pod set, preempt with partial admission`: verifies that preemption triggers when partial admission alone is not enough.
    - `partial admission disabled, multiple variable pod sets`: verifies that no partial admission is performed if features/annotations are not active.
  - `pkg/scheduler/scheduler_tas_test.go`:
    - `TAS workload gets scheduled as trimmed by partial admission`: verifies that Topology Aware Scheduling is compatible with partial admission.
    - `reclaim within cohort; preempting with partial admission`: verifies that reclaiming quota within a cohort works alongside preemption and partial admission.

- **Controllers**:
  - `pkg/controller/jobs/job/job_controller_test.go`: verifies job controller's `RunWithPodSetsInfo` and `RestorePodSetsInfo` logic, updating/restoring parallelism correctly when job is unsuspended or suspended.

#### Integration tests

- **Controllers & Scheduler**:
  - `test/integration/singlecluster/controller/jobs/job/job_controller_test.go`:
    - `Should schedule jobs with partial admission`: verifies a complete integration flow where a Job with `kueue.x-k8s.io/job-min-parallelism` is suspended, partially admitted with reduced parallelism, and its original parallelism is restored when the workload is stopped.
- **Workload Webhook**:
  - `test/integration/singlecluster/webhook/core/workload_test.go`:
    - `invalid podSet minCount (negative)`: verifies negative minCount values are rejected.
    - `valid podSet minCount (zero)`: verifies minCount=0 is accepted when count is 0 or greater.
    - `invalid podSet minCount (too big)`: verifies minCount larger than count is rejected.
    - `too many variable count podSets`: verifies that workloads with multiple variable count PodSets are rejected.

#### E2E tests

- `test/e2e/singlecluster/baseline/job_test.go`:
  - `Should partially admit the Job if configured and not fully fits`: verifies that a real Job is successfully admitted with a reduced parallelism count matching the available cluster resources.

### Graduation Criteria

## Implementation History
- 07/07/2023 - Partial admission for batch/Job was added that supports only 1 podSet with minCount value.

## Future work

### kubeflow/MPIJob controller

In case of MPIJob `j.Spec.RunPolicy.SchedulingPolicy.MinAvailable` can be used to provide a `minimumCount` for the `Worker` PodSets while updating `j.Spec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeWorker].Replicas` before unsuspending the job and after suspending it.

Whether an MPIJob supports partial admission or not can be deduced based on `MinAvailable` without the need of a dedicated annotation.
Additional research is needed into the potential usage of multiple variable count PodSets.

## Drawbacks

## Alternatives

For partial admission of multiple PodSets, another approach that was considered is proportional shrinking. However it was discarded because of insufficiency for some use cases. For example, when shrinking two PodSets from initial counts of (2, 2) down to 3 pods, a proportional approach might result in (1, 1) due to rounding, rather than an optimal (2, 1) or (1, 2).