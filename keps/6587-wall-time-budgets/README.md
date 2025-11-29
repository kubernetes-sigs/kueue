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

This KEP introduces Wall Time Budgets, a new feature that allows administrators to limit the total wall clock time that workloads can consume per resource flavor in a ClusterQueue. This enables cost control and fair resource allocation by setting time-based quotas for expensive resources like GPUs.

The feature adds a new `WallTimePolicy` field to the ClusterQueue spec that allows administrators to define wall time limits per resource flavor. When a workload consumes wall time against these budgets, the usage is tracked and enforced. When budgets are exhausted, the ClusterQueue can be configured to hold new workloads or both hold and drain existing workloads.

## Motivation

In multi-tenant Kubernetes environments, especially those using expensive resources like GPUs, organizations need mechanisms to control costs and ensure fair resource allocation. Traditional resource quotas only limit concurrent usage but don't account for the total time resources are consumed, which is often the primary cost factor for cloud resources.

Wall time budgets provide a time-based quota system that complements existing resource quotas by tracking and limiting the cumulative wall clock time that workloads spend using specific resource flavors.

### Goals

- Enable administrators to set wall time limits per resource flavor in ClusterQueues
- Track wall time consumption for workloads using specific resource flavors
- Provide configurable actions when wall time budgets are exhausted (Hold or HoldAndDrain)
- Integrate seamlessly with existing Kueue scheduling and admission logic
- Support visibility into wall time usage through ClusterQueue status

### Non-Goals

- Implementing fine-grained billing or accounting features
- Supporting wall time limits at the workload or namespace level (only ClusterQueue level)
- Implementing wall time budget sharing across ClusterQueues (cohorts)
- Providing automatic budget renewal or reset mechanisms

## Proposal

### User Stories

#### Story 1: Research Team GPU Time Management

As a cluster administrator for a research organization, I want to allocate specific GPU hours to different research teams so that I can ensure fair access to expensive GPU resources and prevent any single team from monopolizing the cluster.

I configure ClusterQueues with wall time budgets like:
- Team A gets 100 GPU hours per month on V100 flavors
- Team B gets 50 GPU hours per month on A100 flavors

When teams submit workloads, their wall time usage is tracked against these budgets, and new workloads are held when budgets are exhausted.

#### Story 2: Multi-tenant Cost Control

As a platform engineer managing a shared Kubernetes cluster, I want to implement cost controls based on actual resource usage time rather than just concurrent limits, so that I can provide predictable billing and prevent cost overruns.

I set up wall time budgets that correspond to our cloud provider's billing model (e.g., 1000 GPU hours per month) and configure alerts when budgets approach exhaustion.

#### Story 3: Budget Exhaustion Handling

As a cluster administrator, I want flexibility in how the system behaves when wall time budgets are exhausted, so that I can choose between holding new workloads or also draining existing ones based on my organization's policies.

I can configure different ClusterQueues with:
- `Hold`: New workloads are queued but existing workloads continue
- `HoldAndDrain`: New workloads are queued and existing workloads are preempted

### Notes/Constraints/Caveats

- Wall time tracking begins when a workload is admitted and ends when it completes
- Workloads maintain their own wall time accounting in `WorkloadStatus`, supporting eviction/re-admission scenarios
- Wall time budgets are enforced at admission time, not during workload execution
- Budget exhaustion affects the entire ClusterQueue, not individual workloads
- Wall time usage persists in both Workload and ClusterQueue status and is not automatically reset
- Wall time is tracked in seconds at the workload level and converted to hours at the ClusterQueue level

### Risks and Mitigations

- **Risk**: Wall time tracking overhead could impact scheduler performance
  - **Mitigation**: Use efficient data structures and update wall time usage asynchronously where possible

- **Risk**: Clock skew between nodes could affect accuracy
  - **Mitigation**: Document that wall time is based on workload admission/completion events recorded by the controller

- **Risk**: Budget exhaustion could lead to resource starvation
  - **Mitigation**: Provide clear monitoring and alerting capabilities through metrics and status fields

## Design Details

### Admin-facing API

The feature adds a new `WallTimePolicy` field to the ClusterQueue spec:

```go
type ClusterQueueSpec struct {
    // ... existing fields ...
    
    // wallTimePolicy defines wall time limits for resource flavors in this ClusterQueue.
    // When specified, workloads using the configured flavors will have their wall time
    // tracked against the defined budgets.
    WallTimePolicy *WallTimePolicy `json:"wallTimePolicy,omitempty"`
}

type WallTimePolicy struct {
    // WallTimeFlavors describes the wall time limits for specific resource flavors.
    WallTimeFlavors []WallTimeFlavor `json:"wallTimeFlavors"`
}

type WallTimeFlavor struct {
    // name of the resource flavor this wall time limit applies to.
    Name ResourceFlavorReference `json:"name"`
    
    // wallTimeAllocatedHours is the number of hours allocated for this flavor.
    WallTimeAllocatedHours int32 `json:"wallTimeAllocatedHours"`
    
    // actionWhenWallTimeExhausted defines the action when budget is exhausted.
    // - Hold: New workloads are held, existing workloads continue
    // - HoldAndDrain: New workloads are held, existing workloads are preempted
    ActionWhenWallTimeExhausted StopPolicy `json:"actionWhenWallTimeExhausted,omitempty"`
}
```

The ClusterQueue status includes wall time usage information:

```go
type ClusterQueueStatus struct {
    // ... existing fields ...

    // wallTimeFlavorUsage tracks wall time consumption per flavor.
    WallTimeFlavorUsage []WallTimeFlavorUsage `json:"wallTimeFlavorUsage,omitempty"`
}

type WallTimeFlavorUsage struct {
    // name of the resource flavor.
    Name ResourceFlavorReference `json:"name"`

    // wallTimeAllocated is the total number of hours allocated for this ClusterQueue.
    WallTimeAllocated int32 `json:"wallTimeAllocated,omitempty"`

    // wallTimeUsed is the number of hours used.
    WallTimeUsed int32 `json:"wallTimeUsed"`
}
```

#### Workload API Changes

To support wall time tracking, the WorkloadStatus is extended with fields that track wall time consumption at the workload level:

```go
type WorkloadStatus struct {
    // ... existing fields ...

    // accumulatedPastExecutionTimeSeconds holds the total time, in seconds, the workload spent
    // in Admitted state, in the previous `Admit` - `Evict` cycles.
    //
    // This field accumulates time from all previous admission periods before the current one.
    // When a workload is evicted and re-admitted, the time from the previous admission period
    // is added to this field.
    AccumulatedPastExecutionTimeSeconds *int32 `json:"accumulatedPastExecutionTimeSeconds,omitempty"`

    // wallTimeSeconds holds the total time, in seconds, the workload spent
    // in Admitted state.
    //
    // This represents the current wall time for an admitted workload. When combined with
    // accumulatedPastExecutionTimeSeconds, it provides the complete wall time consumption
    // across all admission cycles.
    WallTimeSeconds *int32 `json:"wallTimeSeconds,omitempty"`
}
```

These fields work together to provide complete wall time tracking:
- `wallTimeSeconds`: Tracks the current admission period's wall time (from admission to now/eviction)
- `accumulatedPastExecutionTimeSeconds`: Accumulates wall time from all previous admission cycles
- **Total wall time** = `wallTimeSeconds` + `accumulatedPastExecutionTimeSeconds`

This design supports workloads that may be evicted and re-admitted multiple times, ensuring accurate wall time accounting across the workload's entire lifecycle.

### Internal Implementation

The implementation includes:

1. **Wall Time Tracking**: New `WallTimeResourceQuota` and `WallTimeFlavorGroup` types in the scheduler cache to manage wall time budgets and track usage.

2. **Workload-level Tracking**: Each workload tracks its own wall time consumption through WorkloadStatus fields:
   - When a workload is admitted, `wallTimeSeconds` begins tracking the current admission period
   - When a workload is evicted, the current `wallTimeSeconds` value is added to `accumulatedPastExecutionTimeSeconds`, and `wallTimeSeconds` is reset
   - When a workload completes, its total wall time (`wallTimeSeconds` + `accumulatedPastExecutionTimeSeconds`) is used to update the ClusterQueue's `WallTimeFlavorUsage`

3. **Admission Logic**: Extended admission checks to verify wall time budget availability before admitting workloads:
   - Check if the ClusterQueue has a `WallTimePolicy` configured
   - Verify that sufficient wall time budget remains for the requested resource flavors
   - Reject admission if budgets are exhausted and the policy requires it

4. **Usage Updates**: Wall time consumption is calculated and updated when workloads complete or are preempted:
   - Workload controller periodically updates `wallTimeSeconds` for admitted workloads
   - On eviction, current wall time is accumulated into `accumulatedPastExecutionTimeSeconds`
   - On completion, total wall time is deducted from the ClusterQueue's budget

5. **Policy Enforcement**: When budgets are exhausted, the configured `ActionWhenWallTimeExhausted` policy (Hold or HoldAndDrain) is applied:
   - **Hold**: New workloads are queued but existing admitted workloads continue running
   - **HoldAndDrain**: New workloads are queued and existing admitted workloads are evicted

### Test Plan

#### Unit Tests

- Wall time quota creation and management
- Workload wall time tracking (wallTimeSeconds and accumulatedPastExecutionTimeSeconds)
- Wall time accumulation across eviction/re-admission cycles
- Wall time consumption calculation and conversion (seconds to hours)
- Admission logic with wall time budget checks
- Policy enforcement (Hold vs HoldAndDrain)
- ClusterQueue status updates (WallTimeFlavorUsage)

#### Integration Tests

- End-to-end wall time tracking for admitted workloads
- Workload status updates during admission, eviction, and completion
- Wall time accumulation across multiple eviction/re-admission cycles
- Budget exhaustion scenarios and policy enforcement
- Integration with existing Kueue scheduling features (preemption, borrowing)
- ClusterQueue and Workload status reporting accuracy

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

- Adds complexity to the ClusterQueue API and scheduler logic
- Introduces new state that must be managed and persisted
- May require additional monitoring and alerting infrastructure
- Clock-based measurements can be imprecise in distributed systems

## Alternatives

1. **External Budget Management**: Implement wall time budgets in an external system that integrates with Kueue via admission controllers or webhooks.
   - **Pros**: Keeps Kueue core simpler, allows for more sophisticated billing logic
   - **Cons**: Requires additional infrastructure, more complex integration

2. **Resource-based Budgets**: Instead of time-based budgets, use cumulative resource consumption (e.g., GPU-hours = GPUs * time).
   - **Pros**: More flexible, could account for different resource amounts
   - **Cons**: More complex calculation, harder to understand and configure

3. **Namespace-level Budgets**: Implement wall time budgets at the namespace level instead of ClusterQueue level.
   - **Pros**: More granular control, easier multi-tenancy
   - **Cons**: Doesn't align with Kueue's ClusterQueue-centric model

The proposed ClusterQueue-level wall time budgets provide the best balance of functionality and simplicity while aligning with Kueue's existing architecture and concepts.