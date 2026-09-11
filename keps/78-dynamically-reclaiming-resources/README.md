# KEP-78:  Dynamically reclaim resources of Pods of a Workload

## Table of Contents

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Pod Completion Accounting](#pod-completion-accounting)
    - [Reference design for (<code>batch/Job</code>)](#reference-design-for-batchjob)
      - [To consider](#to-consider)
  - [API](#api)
  - [Topology Aware Scheduling (TAS) Usage Release](#topology-aware-scheduling-tas-usage-release)
    - [Multi-PodSet Workloads](#multi-podset-workloads)
    - [Quota Release vs. TAS Usage Release](#quota-release-vs-tas-usage-release)
    - [Feature Gate Dependency](#feature-gate-dependency)
- [Implementation](#implementation)
  - [Workload](#workload)
    - [API](#api-1)
    - [<code>pkg/workload</code>](#pkgworkload)
  - [Jobframework](#jobframework)
  - [Batch/Job](#batchjob)
- [Testing Plan](#testing-plan)
  - [NonRegression](#nonregression)
  - [Unit Tests](#unit-tests)
  - [Integration tests](#integration-tests)
- [Implementation History](#implementation-history)
<!-- /toc -->

## Summary

This proposal allows Kueue to reclaim the resources of a successfully completed Pod of a Workload even before the whole Workload completes its execution. Freeing the unused resources of a Workload earlier may allow more pending Workloads to be admitted. This will allow Kueue to utilize the existing resources of the cluster much more efficiently.

In the remaining of this document:
1. *Job* refers to any kind job supported by Kueue including `batch/job`, `MPIJob`, `RayJob`, etc.
2. *Pods of a Workload* means the same as *Pods of a Job* and vice-versa.


## Motivation

Currently, the quota assigned to a Job is reclaimed by Kueue only when the whole Job finishes.
For Jobs with multiple Pods, the resources will be reclaimed only after the last Pod of the Job finishes.
This is not efficient as the Jobs might have different needs during execution, for instance the needs of a `batch/job` having the `parallelism` equal to `completions` will decrease with every Pod finishing it's execution.

### Goals

- Utilize the unused resources of the successfully completed Pods of a running Job.

### Non-Goals

- Preempting a Workload or Pods of a Workload to free resources for a pending Workload.
- Partially admitting a Workload.
- Monitor the Job's pod execution.

## Proposal

Reclaiming the resources of the succeeded Pods of a running Job as soon as the Pod completes its execution.

We propose to add a new field `.status.ReclaimablePods` to the Workload API. `.status.ReclaimablePods` is a list that holds the count of Pods belonging to a PodSet whose resources are no longer needed and could be reclaimed by Kueue.

### Pod Completion Accounting

Since the ability of actively monitoring pods execution of a job is not in the scope of the core Kueue implementation, the number of pods for which the resources are no longer needed for each PodSet should be reported by each framework specific `GenericJob` implementation.

For this purpose the `GenericJob` interface should be changed to add an additional method able to report this

```go
type GenericJob interface {
    // ...

    // Get reclaimable pods.
    ReclaimablePods() []ReclaimablePod
}
```

#### Reference design for (`batch/Job`)

Having a job defined with **P** `parallelism` and **C** `completions`, and **n** number of completed pod executions,
the expected reclaimablePods should be:

```go
[]ReclaimablePod{
    {
        Name: "main",
        Count: P - min(P,(C-n)),
    }
}
```

##### To consider
According to [kubernetes/enhancements](https://github.com/kubernetes/enhancements) the algorithm presented above might need to be reworked in order to account for:
- [KEP-3939](https://github.com/kubernetes/enhancements/pull/3940) which adds a new field `terminating` to account for terminating pods, depending on `spec.RecreatePodsWhen`, when reclaimablePods are computed the new fields needs to be taken into account.
- [KEP-3850](https://github.com/kubernetes/enhancements/pull/3967) which adds the ability for an index to fail. If an index fails, the resources previously reserved for it are no longer needed.

### API

A new field `ReclaimablePods` is added to the `.status` of Workload API.

```go
// WorkloadStatus defines the observed state of Workload.
type WorkloadStatus struct {

    // ...


    // reclaimablePods keeps track of the number pods within a podset for which
    // the resource reservation is no longer needed.
    // +optional
    ReclaimablePods []ReclaimablePod `json:"reclaimablePods,omitempty"`
}

type ReclaimablePod struct {
    // name is the PodSet name.
    Name string `json:"name"`

    // count is the number of pods for which the requested resources are no longer needed.
    Count int32 `json:"count"`
}
```

### Topology Aware Scheduling (TAS) Usage Release

While compute quota (such as CPU and memory) is dynamically reclaimed pod-by-pod as individual pods complete execution, Topology Aware Scheduling (TAS) assignments operate at the PodSet level.

When all Pods within an admitted PodSet become reclaimable (i.e., `ReclaimablePod.Count` equals the PodSet's `count`), Kueue releases that PodSet's TAS topology usage (`TASUsage`). This makes the assigned topology domain (such as a node, rack, or block) available in the scheduler cache for other pending workloads to be admitted.

#### Multi-PodSet Workloads

This behavior is particularly important for multi-PodSet workloads, such as `JobSet` or driver-worker workloads, where one PodSet can finish its execution while other PodSets continue to run. Once a completed PodSet becomes fully reclaimable, its assigned topology domain is released immediately rather than being held until the entire workload finishes.

#### Quota Release vs. TAS Usage Release

The distinction between quota release and TAS usage release is preserved:
- **Quota release**: Can occur incrementally for individual completed pods within a PodSet, reducing the overall workload resource requests.
- **TAS usage release**: Only occurs when an entire PodSet is fully reclaimable (`ps.Count == 0`). Topology assignments cannot be partially sliced or released for partial pod completions within a single PodSet.

#### Feature Gate Dependency

This behavior is gated by the `ReclaimablePods` feature gate. When `ReclaimablePods` is disabled:
- TAS usage behavior remains unchanged, and topology assignments are retained until the workload completes or is deactivated.

## Implementation

### Workload
#### API

- Add the new field in the workload's status.
- Validate the data in `status.ReclaimablePods`:
  1. The names must be found in the `PodSets`.
  2. The cont should never exceed the `PodSets` count.
  3. The cont should not decrease if the workload is admitted.


#### `pkg/workload`

Rework the way `Info.TotalRequests` in computed in order to take the `ReclaimablePods` into account.

Additionally, update `Info.TASUsage` to omit PodSets that are fully reclaimable when the `ReclaimablePods` feature gate is enabled, releasing their topology domain usage in the scheduler cache.

### Jobframework

Adapt the `GenericJob` interface, and ensure that the `ReclaimablePods` information provided is synced with it's associated workload status.

### Batch/Job

Adapt it's `GenericJob` implementation to the new interface.


## Testing Plan

### NonRegression
The new implementation should not impact any of the existing unit, integration or e2e tests. A workload that has no `ReclaimablePods` populated should behave the same as it dose prior to this implementation.

### Unit Tests

All the Kueue's core components must be covered by unit tests.

### Integration tests
* Scheduler
  - Checking if a Workload gets admitted when an admitted Workload releases a part of it's assigned resources.
  - Checking if a pending Workload requiring topology gets admitted when one PodSet of an admitted multi-PodSet workload becomes fully reclaimable and releases its assigned topology domain.

* Kueue Job Controller (Optional)
  - Checking the resources owned by a Job are released to the cache and clusterQueue when a Pod of the Job succeed.

## Implementation History

- Dynamically Reclaiming Resources are tracked as part of [enhancement#78](https://github.com/kubernetes-sigs/kueue/issues/78).
- Extend KEP-78 to document Topology Aware Scheduling (TAS) topology usage release when a PodSet is fully reclaimable under the `ReclaimablePods` feature gate.
