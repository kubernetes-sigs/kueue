 
# KEP-9270: Introduce configuration for the MultiKueue Incremental Dispatcher
<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API Changes](#api-changes)
  - [Implementation overview](#implementation-overview)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Possible Follow-ups](#possible-follow-ups)
- [Drawbacks](#drawbacks)
<!-- /toc -->

## Summary

This proposal introduces a new configuration structure for the MultiKueue Incremental Dispatcher, starting with a stepSize field. This setting will control the number of worker clusters that the Manager Cluster will attempt to dispatch a workload to concurrently.

## Motivation

Currently, the MultiKueue Incremental Dispatcher has a hardcoded batch size `batchSize := 3` when attempting to schedule a workload across multiple worker clusters. 

Sometimes administrators need to configure the parameter in order to:
1. avoid unnecessary job execution delays when there are many clusters (by increasing the stepSize)
2. use more granular prioritization of the clusters (by setting stepSize=1)
3. tune the api-server traffic and load on the manager cluster

### Goals

* Add an `IncrementalDispatcherConfig` struct to the `MultiKueueConfig` API containing a `stepSize` parameter
* Update the Workload Controller's dispatching loop to process worker clusters in batches defined by the `stepSize`.

### Non-Goals

* We will not change the way worker clusters are scored or prioritized.
* We will not alter the `AllAtOnce` dispatcher behavior, as this proposal strictly targets the Incremental Dispatcher.

## Proposal

We propose updating the `MultiKueueConfig` API to introduce an `IncrementalDispatcherConfig` struct containing an optional integer for the step size, and updating the Workload Controller to respect this batch limit when iterating through the list of available worker clusters.

### Risks and Mitigations

**Risk:** An administrator might set the `stepSize` to an extremely high number (e.g., equal to the total number of clusters), which effectively recreates the heavy load issues of the `AllAtOnce` dispatcher and increases the chance of race conditions where multiple clusters admit the job at the exact same time.

**Mitigation:** Provide clear API documentation regarding best practices. The Workload Controller's conflict resolution logic will naturally handle race conditions, just as it already does for the `AllAtOnce` strategy.

## Design Details

### API Changes

We will update the global `Configuration` API (specifically the `MultiKueue` struct) to include a new configuration struct, rather than placing it in `MultiKueueConfigSpec`. This keeps the incremental dispatcher configuration co-located with the `DispatcherName` selection.

The field will be named `IncrementalDispatcherConfig` and will contain a `StepSize` field. To ensure backwards compatibility and preserve existing system behavior, the default value for `StepSize` will be `3`.

Only the new field is shown below; existing `MultiKueue` fields are unchanged.

```go
type MultiKueue struct {
      // IncrementalDispatcherConfig contains the configuration for the incremental dispatcher.
      // This field is only valid when DispatcherName is set to the incremental dispatcher.
      // Note: This field is ignored when the MultiKueueIncrementalDispatcher feature gate is disabled.
      // +optional
      IncrementalDispatcherConfig *IncrementalDispatcherConfig `json:"incrementalDispatcherConfig,omitempty"`
}

type IncrementalDispatcherConfig struct {
      // StepSize defines the number of worker clusters the Incremental
      // Dispatcher will query simultaneously.
      // Minimum value is 1. If not set, it defaults to 3.
      // +optional
      StepSize *int32 `json:"stepSize,omitempty"`
}
```

Because the `Configuration` API is a component-config (ConfigMap-based) API rather than a CRD, kubebuilder markers are not used for schema generation, validation, or defaulting. Accordingly, the `StepSize` minimum (`1`) and default (`3`) are enforced through the manual validation and defaulting code under `apis/config/v1beta2` (e.g. `defaults.go`).

### Implementation overview

The core logic change is minimal because the batching mechanism is already implemented in the Incremental Dispatcher, assuming the hard-coded value of stepSize=3.  
No architectural changes are required to the dispatching loop itself.

### Test Plan

 I/we understand the owners of the involved components may require updates to existing tests to make this code solid enough prior to committing the changes necessary to implement this enhancement.

#### Prerequisite testing updates

No major prerequisite testing updates are required. Current mock worker cluster frameworks in EnvTest are sufficient.

#### Unit tests

- `pkg/controller/workloaddispatcher/incrementaldispatcher_test.go`: `TestIncrementalDispatcherNominateWorkers` verifies that the worker cluster list is correctly divided into chunks matching the `stepSize` and that the default value remains `3` if the user does not specify a `stepSize`.

#### Integration tests

- `TestIncrementalDispatcherReconciler_Reconcile`: Simulate a Manager Cluster with 5 Worker Clusters. Set `stepSize` to `2`. Submit a workload and verify via the API server that exactly 2 proxy workloads are created in the first step. Simulate a rejection on the first 2 clusters, and verify that the controller correctly creates the next 2 proxy workloads in the second step.

#### e2e tests

Standard E2E multi-cluster tests will be updated to include an iteration where `MultiKueueConfig` is deployed with a `stepSize > 1` to ensure workloads execute successfully end-to-end without duplication.

### Graduation Criteria

* **Alpha:**
    * `MultiKueueIncrementalDispatcherConfig` is alpha (disabled by default)
    * API fields added.
    * Workload Controller updated to support batching.
    * Unit tests and EnvTest integration tests passing.
* **Beta:** 
    * `MultiKueueIncrementalDispatcherConfig` is beta (enabled by default)
    * Gather feedback from the community.
    * E2E tests added and passing consistently.
* **Stable:**
    * `MultiKueueIncrementalDispatcherConfig` is locked (will be removed in N+2)
    * Feature has been in Beta for at least one release cycle with no major bugs reported.

## Implementation History

- 2026-05-01: KEP proposed and initial draft created.

## Possible Follow-ups

By introducing the `IncrementalDispatcherConfig` structure, we pave the way for extending the configuration of the incremental dispatcher in the future without breaking API compatibility.

Possible future configurations that could be added to this struct include:

* `timeout`: To configure how long the dispatcher waits before moving to the next batch of clusters.

* `stepSizeAsPercent`: To define the batch size as a percentage of the total available worker clusters rather than a static integer.
## Drawbacks

This adds slight code complexity to the existing Incremental Dispatcher loop and introduces one additional configuration parameter for administrators to manage and tune.
