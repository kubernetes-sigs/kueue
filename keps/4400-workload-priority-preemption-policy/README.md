# KEP-4400: WorkloadPriorityClass Preemption Policy

<!-- toc -->
- [Summary](#summary)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1](#story-1)
  - [Notes/Constraints/Caveats (Optional)](#notesconstraintscaveats-optional)
    - [Quota Constraints for Non-Preemptible Workloads](#quota-constraints-for-non-preemptible-workloads)
- [Design Details](#design-details)
  - [API Changes](#api-changes)
  - [Implementation Details](#implementation-details)
  - [Interaction with ClusterQueue Preemption Policies](#interaction-with-clusterqueue-preemption-policies)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

This KEP proposes extending the `WorkloadPriorityClass` API (introduced in KEP-973) with a `preemptionPolicy` field to provide administrators with fine-grained control over which workloads can be preempted. This allows multiple workloads within the same ClusterQueue to have different preemption behaviors irrespective of priority level.

## Proposal

Extend the `WorkloadPriorityClass` API with a `preemptionPolicy` field that controls whether workloads using that priority class can be preempted. This follows the same pattern as Kubernetes core `PriorityClass.preemptionPolicy`.

The preemption policy will be enforced at scheduling time, with additional quota constraints for non-preemptible workloads to prevent resource deadlocks.

### User Stories

#### Story 1

As a Kueue user, I want to make specific workloads non-preemptible because they cannot be resumed easily, and I'm willing to wait longer for these workloads to run. My administrator will use the PreemptionPolicy field in WorkloadPriorityClass to provide a lever for this behavior.

### Notes/Constraints/Caveats (Optional)

#### Quota Constraints for Non-Preemptible Workloads

Workloads using a `WorkloadPriorityClass` with `preemptionPolicy: Never` are subject to the additional quota constraint:

- **Nominal Quota Limit**: They can only consume resources up to the ClusterQueue's nominal quota
- **No Borrowing**: They cannot borrow resources from other ClusterQueues in the cohort  

This prevents scenarios where non-preemptible workloads consume borrowed quota that cannot be reclaimed

## Design Details

### API Changes

Extend the `WorkloadPriorityClass` type with a `preemptionPolicy` field:

```golang
// apis/kueue/v1beta1/workloadpriorityclass_types.go
type WorkloadPriorityClass struct {
    // ... existing fields ...
    
    // preemptionPolicy controls whether workloads using this priority class can be preempted.
    // Valid values are "Always" (default) and "Never".
    // "Always" means workloads can be preempted by higher priority workloads.
    // "Never" means workloads cannot be preempted, but are restricted to nominal quota only.
    // +kubebuilder:default=Always
    // +kubebuilder:validation:Enum=Always;Never
    // +optional
    PreemptionPolicy *WorkloadPriorityClassPreemptionPolicy `json:"preemptionPolicy,omitempty"`
}

// WorkloadPriorityClassPreemptionPolicy describes a policy for if/when to preempt a workload.
type WorkloadPriorityClassPreemptionPolicy string

const (
    // WorkloadPriorityClassPreemptionPolicyAlways means that workloads can always be preempted by
    // other workloads with higher priority.
    WorkloadPriorityClassPreemptionPolicyAlways WorkloadPriorityClassPreemptionPolicy = "Always"
    
    // WorkloadPriorityClassPreemptionPolicyNever means that workloads should never be preempted.
    // Workloads with this policy are restricted to nominal quota and cannot
    // borrow resources from other ClusterQueues.
    WorkloadPriorityClassPreemptionPolicyNever WorkloadPriorityClassPreemptionPolicy = "Never"
)
```

### Implementation Details

1. **Add PreemptionPolicy field to WorkloadPriorityClass** - Add a field that can be `Always` or `Never` (extensible for future values)

2. **Track NonPreemptibleUsage in clusterqueue_snapshot.go** - Calculate current usage by non-preemptible workloads for each FlavorResource

3. **Update flavorassigner.go/fitsResourceQuota** - If a workload is non-preemptible, check that it doesn't exceed the non-preemptible quota constraint

4. **Update preemption.go/findCandidates** - Skip non-preemptible workloads when selecting preemption candidates

5. **Define IsNonPreemptible helper** - `getWorkloadPriorityClass(w).preemptionPolicy == Never`

### Interaction with ClusterQueue Preemption Policies

The preemption policy hierarchy works as follows:

1. **ClusterQueue policy is checked first** - If the ClusterQueue disables preemption, no workloads in that queue can be preempted regardless of their WorkloadPriorityClass. However, the quota constraints for non-preemptible workloads still apply.
2. **WorkloadPriorityClass policy is checked second** - If the ClusterQueue allows preemption, then WorkloadPriorityClass `preemptionPolicy` takes effect

This ensures ClusterQueue administrators maintain top-level control over preemption behavior.

### Test Plan

#### Unit Tests

- `WorkloadPriorityClass` API validation
- Preemption candidate selection with different policies
- Quota constraint enforcement for non-preemptible workloads
- Priority resolution with preemption policy

#### Integration tests

- Workloads with `preemptionPolicy: Never` cannot be preempted within ClusterQueue
- Workloads with `preemptionPolicy: Never` are subject to nominal quota limits  
- Workloads with `preemptionPolicy: Always` can be preempted normally
- Cross-ClusterQueue reclamation behavior with different preemption policies
- Multiple priority classes with same priority value but different policies

### Graduation Criteria

## Implementation History

## Drawbacks

- Additional complexity in priority class management
- Potential for confusion about quota limitations for non-preemptible workloads

## Alternatives

1. **Workload-level annotation**: Continue with annotation-based approach, but this lacks administrative control
2. **Separate PreemptionClass resource**: Create a separate resource, but this adds complexity and doesn't follow Kubernetes patterns
3. **ClusterQueue-level policies only**: Use only ClusterQueue-level policies, but this lacks the granularity needed within a single queue 