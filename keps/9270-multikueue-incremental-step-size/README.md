 
# KEP-9270: MultiKueue Incremental Dispatcher Step Size
<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API Changes](#api-changes)
  - [Execution Loop](#execution-loop)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
<!-- /toc -->

## Summary

This proposal introduces a new configuration field called `stepSize` to the MultiKueue Incremental Dispatcher. This setting will control the number of worker clusters that the Manager Cluster will attempt to dispatch a workload to concurrently.

## Motivation

Currently, when MultiKueue attempts to schedule a workload across multiple worker clusters, the Incremental Dispatcher has a limitation. If it checks clusters one by one, the process is too slow and delays job execution. If it attempts to dispatch to all clusters simultaneously, it generates unnecessary API traffic, increases the load on the Manager Cluster, and can lead to race conditions where multiple clusters try to admit the same job at the same time.

Introducing a `stepSize` allows cluster administrators to configure a "batch size" for this process, finding the perfect balance between speed and system stability.

### Goals

* Add a `stepSize` parameter to the `MultiKueueConfig` API.
* Update the Workload Controller's dispatching loop to process worker clusters in batches defined by the `stepSize`.
* Ensure that if a workload is admitted in a batch, the remaining pending proxy workloads in that batch are cleanly removed.

### Non-Goals

* We will not change the way worker clusters are scored or prioritized.
* We will not alter the `AllAtOnce` dispatcher behavior, as this proposal strictly targets the Incremental Dispatcher.

## Proposal

We propose updating the `MultiKueueConfig` API to accept an optional integer for the step size, and updating the Workload Controller to respect this batch limit when iterating through the list of available worker clusters.


### Risks and Mitigations

**Risk:** An administrator might set the `stepSize` to an extremely high number (e.g., equal to the total number of clusters), which effectively recreates the heavy load issues of the `AllAtOnce` dispatcher and increases the chance of race conditions where multiple clusters admit the job at the exact same time.
**Mitigation:** Provide clear API documentation regarding best practices. The Workload Controller's conflict resolution logic (which deletes duplicate proxy workloads once one is admitted) will naturally handle race conditions, just as it already does for the `AllAtOnce` strategy. 

## Design Details

### API Changes
We will update the `MultiKueueConfigSpec` struct to include the new field. This change will be backwards compatible by defaulting the step size to `1` (which matches the current one-by-one behavior).

```go
type MultiKueueConfigSpec struct {
    // Other existing fields...

    // StepSize defines the number of worker clusters the Incremental 
    // Dispatcher will query at the same time. 
    // Minimum value is 1. If not set, it defaults to 1.
    // +optional
    // +kubebuilder:default=1
    // +kubebuilder:validation:Minimum=1
    StepSize *int32 `json:"stepSize,omitempty"`
}
```

### Execution Loop
The core logic change will take place inside the MultiKueue Workload Controller's reconciliation loop:
1. When a workload is ready for dispatch, the controller reads the `stepSize` from the `MultiKueueConfig`.
2. The controller takes the full list of available worker clusters and divides them into groups (batches) equal to the `stepSize`.
3. The controller loops through the first batch, creating proxy workloads on those specific worker clusters simultaneously.
4. If a cluster in the batch admits the job, the controller immediately deletes the proxy workloads from the other clusters in that same batch and stops the loop.
5. If no cluster in the batch admits the job, the controller moves on to the next batch of clusters.

### Test Plan

 I/we understand the owners of the involved components may require updates to existing tests to make this code solid enough prior to committing the changes necessary to implement this enhancement.

#### Prerequisite testing updates

No major prerequisite testing updates are required. Current mock worker cluster frameworks in EnvTest are sufficient.

#### Unit tests

- `pkg/controller/core/workload_controller_test.go`: Verify that the worker cluster list is correctly divided into chunks matching the `stepSize`.
- `pkg/controller/core/workload_controller_test.go`: Verify that the default value remains `1` if the user does not specify a `stepSize`.

#### Integration tests

- `TestIncrementalDispatcherStepSize`: Simulate a Manager Cluster with 5 Worker Clusters. Set `stepSize` to `2`. Submit a workload and verify via the API server that exactly 2 proxy workloads are created in the first step. Simulate a rejection on the first 2 clusters, and verify that the controller correctly creates the next 2 proxy workloads in the second step.

#### e2e tests

Standard E2E multi-cluster tests will be updated to include an iteration where `MultiKueueConfig` is deployed with a `stepSize > 1` to ensure workloads execute successfully end-to-end without duplication.

### Graduation Criteria

* **Alpha:**
    * API fields added.
    * Workload Controller updated to support batching.
    * Unit tests and EnvTest integration tests passing.
* **Beta:** * Gather feedback from the community.
    * E2E tests added and passing consistently.
* **Stable:**
    * Feature has been in Beta for at least one release cycle with no major bugs reported.

## Implementation History

- 2026-05-01: KEP proposed and initial draft created.

## Drawbacks

This adds slight code complexity to the existing Incremental Dispatcher loop and introduces one additional configuration parameter for administrators to manage and tune.