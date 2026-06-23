# KEP-420: Allow partial admission of PodSets

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
- [Design Details](#design-details)
  - [Workload API](#workload-api)
  - [Scheduler / Flavorassignment](#scheduler--flavorassignment)
  - [Jobframework](#jobframework)
  - [batch/Job controller](#batchjob-controller)
  - [kubeflow/MPIJob controller](#kubeflowmpijob-controller)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

Add an optional way of allowing the partial admission of a workload if the full admission is not possible.

## Motivation

In practice, not all Workloads require the parallel execution of all the `count` of a `PodSet`, for such cases having a way to partially reserve the quota in order to prevent starvation.

For example if a batch/Job has parallelism is x and there is only quota available for y < x then the job could still be admitted if it can work with a lower parallelism.

### Goals

Provide an opt-in way for Workloads to accept the admission with a lower count of pods if the full count is not available.

### Non-Goals

Since this is an opt-in feature, the parent job should accept the partial admission parameters provided by Kueue.

Kueue will not take any measure to ensure that the parent job respects the assigned quota.

## Proposal

Change the way the flavor assigner work to support decrementing the pods count in order to find a better fit for the current workload.
In case a partial fit is chosen, the jobframework reconciler should provide the admitted pod counts to the parent job before unsuspending it, in a similar fashion as the node selectors are provided. In case the job gets suspended, the original pod counts should be restored in order to allow a potential future admission with its original pod counts.

### User Stories

Kueue issue [420](https://github.com/kubernetes-sigs/kueue/issues/420) provides details on the initial feature details and its applicability for `batch/Job`.

## Design Details

### Workload API

```go
type PodSet struct {
    // .......

    // count is the number of pods for the spec.
    // +kubebuilder:validation:Minimum=1
    Count int32 `json:"count"`

    // minimumCount is the minimum number of pods for the spec acceptable
    // in case of partial admission.
    //
    // If not provided, partial admission for the current PodSet is not
    // enabled.
    // +optional
    MinCount *int32 `json:"minCount,omitempty"`
}

```

### Scheduler / Flavorassignment

In case the workload proposed for the current scheduling cycle, does not fit, with or without preemption, in the current available quota and any of its PodSets allow partial admission, try to find to find a lower counts combination that fits the available quota with or without borrowing.

The search should be optimized (binary search) and preserve the proportion of pods lost across the variable count PodSets.

The accepted number of pods in each PodSet are recorded in `workload.Status.Admission.PodSetAssignments[*].ResourceUsage.Count`

In order to evaluate the potential success of the preemption, the preemption process should be split in:
- Target selection, which should select the workloads within cohort that will need to be evicted, if the list is empty the assignment will be treated as `NoFit`. This step will take place during nomination.
- Preemption issuing, will do the actual eviction of the targets previously selected.

**Partial admission with multiple variable PodSets count**

If multiple PodSets within a workload have variable counts the way the counts should be decrease is highly dependent on the job framework needs, currently the following is proposed:

Starting with the PodSets:

```go
[]podSet { {Count: 1} , {Count: 4, MinCount: 2}, {Count: 20, MinCount: 10}}
```

And only being able to admit 19 pods, it should end up with


```go
[]podSet { {Count: 1} , {Count: 3}, {Count: 15}}
```

With both pods that accept partial admission losing 50% of their range.

The presented solution is not taking into account which of the variable count PodSets is causing the "NoFit" status and can potentially decrease the count of PodSets that will otherwise fit. If this behaviour is not suitable for a specific job framework, the integration layer of the framework can take into account limiting the variable count PodSets to 1.

### Jobframework

```diff
type GenericJob interface {
    // ...

	
-    // RunWithNodeAffinity will inject the node affinity extracting from workload to job and unsuspend the job.
+    // RunWithPodSetsInfo will inject the node affinity and podsSet counts extracted from workload to the job and unsuspend the job.
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
  * jobs supporting partial admission should have a dedicated annotation. eg. `kueue.x-k8s.io/job-min-parallelism`, indicating the minimum `parallelism` acceptable by the job in case of partial admission.
  * jobs which need the `completions` count kept in sync with `parallelism` should indicate this in a second annotation `kueue.x-k8s.io/job-completions-equal-parallelism`
- rework `EquivalentToWorkload` to account for potential differences in `PodSets` spec `Parallelism`.

### kubeflow/MPIJob controller

In case of MPIJob `j.Spec.RunPolicy.SchedulingPolicy.MinAvailable` can be used to provide a `minimumCount` for the `Worker` PodSets while updating `j.Spec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeWorker].Replicas` before unsuspending the job and after suspending it.

Whether an MPIJob supports partial admission or not can be deduced based on `MinAvailable` without the need of a dedicated annotation.
Additional research is needed into the potential usage of multiple variable count PodSets.

### Test Plan

No regressions in the current test should be observed.

[X] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Unit Tests

The changes in the flavorassignment should be covered by unit tests

#### Integration tests

The `scheduler` and `controllers/job` should be extended to cover the new capabilities.

### Graduation Criteria


## Implementation History


## Drawbacks


## Alternatives

