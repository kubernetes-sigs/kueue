# KEP-78:  Dynamically reclaim resources of Pods of a Workload

## Table of Contents

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Pod Successful Completion Cases](#pod-successful-completion-cases)
    - [Case N &gt;= M](#case-n--m)
    - [Case M &gt; N](#case-m--n)
  - [Pod Failure Cases](#pod-failure-cases)
    - [RestartPolicy of Pods as Never](#restartpolicy-of-pods-as-never)
    - [RestartPolicy of Pods as OnFailure](#restartpolicy-of-pods-as-onfailure)
- [API](#api)
- [Implementation](#implementation)
- [Testing Plan](#testing-plan)
  - [Unit Tests](#unit-tests)
  - [Integration tests](#integration-tests)
- [Implementation History](#implementation-history)
<!-- /toc -->

## Summary

This proposal allows Kueue to reclaim the resources of a successfully completed Pod of a Workload even before the whole Workload completes its execution. Freeing the unused resources of a Workload earlier may allow more pending Workloads to be admitted. This will allow Kueue to utilize the existing resources of the cluster much more efficiently.

In the remaining of this document, *Pods of a Workload* means the same as *Pods of a Job* and vice-versa.


## Motivation

Currently, the resources owned by a Job are reclaimed by Kueue only when the whole Job finishes. For Jobs with multiple Pods, the resources will be reclaimed only after the last Pod of the Job finishes. This is not efficient as the Pods of parallel Job may have laggards consuming the unused resources of the Jobs those are currently executing.

### Goals

- Utilize the unused resources of the successfully completed Pods of a running Job.

### Non-Goals

- Preempting a Workload or Pods of a Workload to free resources for a pending Workload.
- Partially admitting a Workload.

## Proposal

Reclaiming the resources of the succeeded Pods of a running Job as soon as the Pod completes its execution.

We propose to add a new field `.status.ReclaimedPodSets` to the Workload API. `.status.ReclaimedPodSets` is a list that holds the count of successfully completed Pods belonging to a PodSet whose resources have been reclaimed by Kueue.

Now we will look at the Pod successful completion and failure cases and how Kueue will reclaim the resources in each of the cases.

### Pod Successful Completion Cases

Reclaiming the resources of a successful Pod of a Job depends on two parameters - remaining completions of a Job(M) and Job's parallelism(N). Depending on the values of **M** and **N**, we will look at the different cases how the resources will be reclaimed by Kueue.

Please note here that **M** here refers to the remaining successful completions of a Job and not the Job's `.spec.completions` field. **M** is subject to change during the lifetime of the Job. One way to derive the value of **M** would be to calculate the difference of `.spec.completions` and `.status.succeeded` values of a Job.

#### Case N >= M
If a Job's parallelism is greater or equal to its remaining completions, then for every successful completion of Pod of a Job, Kueue will reclaim the resources associated with the successful Pod.

#### Case M > N
When the remaining completions of a Job are greater than the Job's parallelism, then for every successfully completed Pod of a Job, the resources associated with the Pod won't be reclaimed by Kueue. This is because, the resource requirement of the Job still remains the same. The Job has to create a new Pod as a replacement against the successfully completed Pod.

The Job will be able to reclaim the resources of Pods when it satisfies the case `N >= M`. A Job which proceeds further in its execution with case `M > N` will get converted to a problem of case `N >= M` because, the value **M** will decrease with successful Pod completions. Hence, the process to reclaim resources of a Pod of a Job will be same as mentioned for the case `N >= M`

### Pod Failure Cases

#### RestartPolicy of Pods as Never

Whenever a Pod of a Job fails, the Job will recreate a Pod as replacement against the failed Pod. The Job will continue this process of creating new Pods until the Job reaches its `backoffLimit`(by default the backoffLimit is `6`). The newly created Pod against the failed Pod will reuse the resources of the failed Pod. Once the Job reaches its `backoffLimit` and does not have the required successful `.spec.completions` count, the Job is termed as a `Failed` Job. When the Job is marked as failed, no more Pods would be created by the Job. So, the remaining owned resources of the Job will be reclaimed by queue. 

#### RestartPolicy of Pods as OnFailure

When the Pods of the Job have `.spec.template.spec.restartPolicy = "OnFailure"`, the failed Pod stays on the node and the failed container within the Pod is restarted. The Pod might run indefinitely or get marked as `Failed` after the `.spec.activeDeadlineSeconds` is completed by Job if specified. In this case also, Kueue won't reclaim the resources of the Job until the Job gets completed as either `Failed` or `Succeeded`.

Hence, as seen from the above discussed Pod failure cases, we conclude that the Pods of a Job which fail during its execution, its resources are not immediately reclaimed by Kueue. Only when the Job gets marks as `Failed`, the resources of failed Pods will be reclaimed by Kueue.

## API

A new field `ReclaimedPodSets` is added to the `.status` of Workload API.

```go
// WorkloadStatus defines the observed state of Workload
type WorkloadStatus struct {
    // conditions hold the latest available observations of the Workload
    // current state.
    // +optional
    // +listType=map
    // +listMapKey=type
    Conditions []WorkloadCondition `json:"conditions,omitempty"`

    // list of count of Pods of a PodSet with resources reclaimed
    // +optional
    ReclaimedPodSets []ReclaimedPodSetPods `json:"reclaimedPodSets"`
}

// ReclaimedPodSetPod defines the PodSet name and count of successfully completed Pods 
// belonging to the PodSet whose resources can be reclaimed.
type ReclaimedPodSet struct{
    Name string
    Count int
}
```

## Implementation

Kueue's job reconciler will compare the Job's `.status.Succeeded` field and the Workload's `.status.reclaimedPodSets[i].count` field value. If the former value is greater than the later then, Kueue's Job reconciler will update the Workload object's `.status`. Workload reconciler will catch the update event of Kueue Job reconciler and release the resources of the newly succeeded Pods to the ClusterQueue depending upon the case the Job satisfies discussed in the section of [successful Pod completion](#pod-successful-completion-cases).

## Testing Plan

Dynamically reclaiming resources enhancement has unit and integration tests. These tests
are run regularly as a part of Kueue's prow CI/CD pipeline.

### Unit Tests

All the Kueue's core components must be covered by unit tests.  Here is a list of unit tests required for the modules of the feature:

* [Cluster-Queue cache tests](https://github.com/kubernetes-sigs/kueue/blob/main/pkg/cache/cache_test.go)
* [Workload tests](https://github.com/kubernetes-sigs/kueue/blob/main/pkg/workload/workload_test.go)

### Integration tests
* Kueue Job Controller
  - Checking the resources owned by a Job are released to the cache and clusterQueue when a Pod of the Job succeed.
  - Integration tests for Job controller are [found here](https://github.com/kubernetes-sigs/kueue/blob/main/test/integration/controller/job/job_controller_test.go).

* Workload Controller
  - Should update the `.spec.reclaimedPodSets` of a Workload when a Pod of a Job succeeds.
  - Integration tests for Workload Controller are [found here](https://github.com/kubernetes-sigs/kueue/blob/main/test/integration/controller/core/workload_controller_test.go).

* Scheduler
  - Checking if a Workload gets admitted when an active parallel Job releases resources of completed Pods.
  - Integration tests for Scheduler are [found here](https://github.com/kubernetes-sigs/kueue/blob/main/test/integration/scheduler/scheduler_test.go).

## Implementation History

Dynamically Reclaiming Resources are tracked as part of [enhancement#78](https://github.com/kubernetes-sigs/kueue/issues/78).