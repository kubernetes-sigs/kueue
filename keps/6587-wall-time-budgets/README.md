# KEP-28: Wall Time Budgets

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Research Team GPU Time Management](#story-1-research-team-gpu-time-management)
    - [Story 2: Multi-tenant Cost Control](#story-2-multi-tenant-cost-control)
    - [Story 3: Budget Exhaustion Handling](#story-3-budget-exhaustion-handling)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Admin-facing API](#admin-facing-api)
  - [Internal Implementation](#internal-implementation)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration Tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

This KEP introduces Wall Time Budgets, a new feature that allows administrators to limit the total wall clock time that workloads can consume in a LocalQueue. This enables cost control and fair resource allocation by setting time-based quotas for expensive resources like GPUs at the namespace/team level.

The feature adds a new `WallTimePolicy` field to the LocalQueue spec that allows administrators to define wall time limits. When workloads consume wall time against these budgets, the usage is tracked at the workload level and enforced at the LocalQueue level. When budgets are exhausted, the LocalQueue can be configured to hold new workloads or both hold and drain existing workloads.

## Motivation

In multi-tenant Kubernetes environments, especially those using expensive resources like GPUs, organizations need mechanisms to control costs and ensure fair resource allocation at the team or namespace level. Traditional resource quotas only limit concurrent usage but don't account for the total time resources are consumed, which is often the primary cost factor for cloud resources.

Wall time budgets provide a time-based quota system at the LocalQueue level that complements existing resource quotas by tracking and limiting the cumulative wall clock time that workloads spend in the admitted state.

### Goals

- Enable administrators to set wall time limits for LocalQueues
- Track wall time consumption at the workload level with support for eviction/re-admission cycles
- Provide configurable actions when wall time budgets are exhausted (Hold or HoldAndDrain)
- Integrate seamlessly with existing Kueue scheduling and admission logic
- Support visibility into wall time usage through LocalQueue status

### Non-Goals

- Implementing fine-grained billing or accounting features
- Supporting wall time limits at the ClusterQueue level (only LocalQueue level for now)
- Implementing wall time budget sharing across LocalQueues or ClusterQueues
- Providing automatic budget renewal or reset mechanisms
- Per-resource-flavor budgets within a LocalQueue (this is a future enhancement)

## Proposal

### User Stories

#### Story 1: Research Team GPU Time Management

As a cluster administrator for a research organization, I want to allocate specific compute hours to different research teams so that I can ensure fair access to expensive GPU resources and prevent any single team from monopolizing the cluster.

I configure LocalQueues (one per team namespace) with wall time budgets like:
- Team A's LocalQueue gets 100 hours per month
- Team B's LocalQueue gets 50 hours per month

When teams submit workloads to their respective LocalQueues, their wall time usage is tracked at the workload level, and new workloads are held when the LocalQueue's budget is exhausted.

#### Story 2: Multi-tenant Cost Control

As a platform engineer managing a shared Kubernetes cluster, I want to implement cost controls based on actual resource usage time rather than just concurrent limits, so that I can provide predictable billing and prevent cost overruns.

I set up LocalQueues with wall time budgets per namespace that correspond to our billing model (e.g., 1000 compute hours per month for the ML team namespace) and configure monitoring to track when budgets approach exhaustion.

#### Story 3: Budget Exhaustion Handling

As a cluster administrator, I want flexibility in how the system behaves when wall time budgets are exhausted, so that I can choose between holding new workloads or also draining existing ones based on my organization's policies.

I can configure different LocalQueues with:
- `Hold`: New workloads are queued but existing workloads continue running
- `HoldAndDrain`: New workloads are queued and existing workloads are evicted

### Notes/Constraints/Caveats

- Wall time tracking begins when a workload is admitted and ends when it completes
- Workloads maintain their own wall time accounting in `WorkloadStatus`, supporting eviction/re-admission scenarios through two fields:
  - `wallTimeSeconds`: Tracks the current admission period
  - `accumulatedPastExexcutionTimeSeconds`: Accumulates time from previous admission cycles
- Wall time budgets are enforced at the LocalQueue level
- Budget exhaustion affects the entire LocalQueue, not individual workloads
- Wall time usage is tracked in seconds at the workload level and displayed in hours at the LocalQueue level
- The initial implementation focuses on LocalQueue-level budgets; per-resource-flavor budgets are a future enhancement

### Risks and Mitigations

- **Risk**: Wall time tracking overhead could impact scheduler performance
  - **Mitigation**: Use efficient data structures and update wall time usage asynchronously where possible

- **Risk**: Clock skew between nodes could affect accuracy
  - **Mitigation**: Document that wall time is based on workload admission/completion events recorded by the controller

- **Risk**: Budget exhaustion could lead to resource starvation
  - **Mitigation**: Provide clear monitoring and alerting capabilities through metrics and status fields

## Design Details

### Admin-facing API

The feature adds a new `WallTimePolicy` field to the LocalQueue spec:

```go
type LocalQueueSpec struct {
    // ... existing fields ...

    // wallTimePolicy defines the wall time limits for this LocalQueue.
    // When specified, workloads in this LocalQueue will have their wall time
    // tracked against the defined budget.
    WallTimePolicy *LocalQueueWallTimeLimits `json:"wallTimePolicy,omitempty"`
}

type LocalQueueWallTimeLimits struct {
    // wallTimeAllocatedHours is the number of hours that this wall time quota applies to.
    WallTimeAllocatedHours int32 `json:"wallTimeAllocatedHours"`

    // actionWhenWallTimeExhausted defines the action to take when the budget is exhausted.
    // The possible values are:
    // - Hold: New workloads are held, existing workloads continue running
    // - HoldAndDrain: New workloads are held, existing workloads are evicted
    ActionWhenWallTimeExhausted StopPolicy `json:"actionWhenWallTimeExhausted,omitempty"`
}
```

The LocalQueue status includes the wall time policy information (mirrored from spec):

```go
type LocalQueueStatus struct {
    // ... existing fields ...

    // wallTimePolicy defines the wallTimePolicy for the LocalQueue.
    WallTimePolicy *LocalQueueWallTimeLimits `json:"wallTimePolicy,omitempty"`
}
```

#### Workload API Changes

To support wall time tracking, the WorkloadStatus is extended with fields that track wall time consumption at the workload level:

```go
type WorkloadStatus struct {
    // ... existing fields ...

    // wallTimeSeconds holds the total time, in seconds, the workload spent
    // in Admitted state during the current admission period.
    //
    // This represents the current wall time for an admitted workload. When combined with
    // accumulatedPastExexcutionTimeSeconds, it provides the complete wall time consumption
    // across all admission cycles.
    WallTimeSeconds *int32 `json:"wallTimeSeconds,omitempty"`
}
```

These fields work together to provide complete wall time tracking:
- `wallTimeSeconds`: Tracks the current admission period's wall time (from admission to now/eviction)

This design supports workloads that may be evicted and re-admitted multiple times, ensuring accurate wall time accounting across the workload's entire lifecycle.

### Internal Implementation

The implementation includes:

1. **LocalQueue Controller**: The LocalQueue controller is responsible for:
   - Mirroring the `WallTimePolicy` from the LocalQueue spec to its status
   - Monitoring workloads in the LocalQueue and tracking total wall time usage
   - Triggering the appropriate action (Hold or HoldAndDrain) when budgets are exhausted

2. **Workload-level Tracking**: Each workload tracks its own wall time consumption through WorkloadStatus fields:
   - When a workload is admitted, `wallTimeSeconds` begins tracking the current admission period
   - When a workload completes, its total wall time is used to update the ClusterQueue's `WallTimeFlavorUsage`

3. **LocalQueue Wall Time Calculation**: The LocalQueue controller calculates total wall time usage by:
   - Iterating through all workloads belonging to the LocalQueue
   - Summing the total wall time (`wallTimeSeconds` + `accumulatedPastExexcutionTimeSeconds`) for each workload
   - Converting seconds to hours and comparing against the `wallTimeAllocatedHours` budget

4. **Usage Updates**: Wall time consumption is calculated and updated when workloads complete or are preempted:
   - Workload controller periodically updates `wallTimeSeconds` for admitted workloads
   - On completion, total wall time is accumulated into ClusterQueue's budget

5. **Integration with Existing Features**: The wall time budget enforcement integrates with:
   - LocalQueue `StopPolicy` mechanism for holding/draining workloads
   - Workload eviction flow for handling budget exhaustion with HoldAndDrain
   - Existing admission checks to respect LocalQueue stopped state

### Test Plan

#### Unit Tests

- Workload wall time tracking (wallTimeSeconds and accumulatedPastExexcutionTimeSeconds)
- Wall time accumulation across eviction/re-admission cycles in workload controller
- Wall time consumption calculation in workload utilities
- Wall time seconds to hours conversion

#### Integration Tests

- End-to-end wall time tracking for admitted workloads
- LocalQueue controller wall time budget monitoring and calculation
- Workload status updates during admission, eviction, and completion
- Wall time accumulation across multiple eviction/re-admission cycles
- Budget exhaustion scenarios and policy enforcement:
  - Hold: New workloads queued, existing workloads continue
  - HoldAndDrain: New workloads queued, existing workloads evicted
- LocalQueue status updates (WallTimePolicy mirroring)
- Integration with LocalQueue StopPolicy mechanism

### Graduation Criteria

**Alpha (v0.X)**
- Basic wall time tracking and budget enforcement
- Core API definitions stable
- Unit test coverage >80%
- Basic integration tests

**Beta (v0.Y)**
- Production-ready implementation
- Comprehensive integration tests
- Performance benchmarks showing minimal scheduler overhead
- Documentation and user guides

**Stable (v1.Z)**
- At least 2 minor releases as beta
- Production usage by multiple organizations
- No major API changes required

## Implementation History

- **2024-XX-XX**: Initial implementation in poc-budgets branch
- **2024-XX-XX**: KEP created and submitted for review

## Drawbacks

- Adds complexity to the LocalQueue API and controller logic
- Introduces new state that must be managed and persisted in Workload status
- Requires periodic updates to workload wall time, which adds controller overhead
- Clock-based measurements can be imprecise in distributed systems
- Initial implementation doesn't support per-resource-flavor budgets within a LocalQueue

## Alternatives

1. **ClusterQueue-level Budgets (Future Enhancement)**: Implement wall time budgets at the ClusterQueue level with per-resource-flavor tracking.
   - **Pros**: Provides more granular control over specific resource types (e.g., different limits for GPU flavors), aligns with ClusterQueue-centric resource management
   - **Cons**: More complex implementation, requires flavor-specific tracking and accounting
   - **Decision**: Start with LocalQueue-level budgets for simplicity and namespace/team-level control, add ClusterQueue support as a future enhancement

2. **External Budget Management**: Implement wall time budgets in an external system that integrates with Kueue via admission controllers or webhooks.
   - **Pros**: Keeps Kueue core simpler, allows for more sophisticated billing logic
   - **Cons**: Requires additional infrastructure, more complex integration, doesn't leverage Kueue's existing abstractions

3. **Resource-based Budgets**: Instead of time-based budgets, use cumulative resource consumption (e.g., GPU-hours = GPUs * time).
   - **Pros**: More flexible, could account for different resource amounts
   - **Cons**: More complex calculation, harder to understand and configure for users

The proposed LocalQueue-level wall time budgets provide the best balance of functionality and simplicity while aligning with Kueue's namespace-oriented multi-tenancy model. This approach enables administrators to set per-team or per-namespace compute time limits, which is a common use case for cost control and fair sharing.