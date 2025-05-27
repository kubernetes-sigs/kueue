# KEP-1618: Optional garbage collection of finished and deactivated Workloads

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1 - Configurable retention for finished Workloads](#story-1---configurable-retention-for-finished-workloads)
    - [Story 2 - Configurable retention for deactivated Workloads](#story-2---configurable-retention-for-deactivated-workloads)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API](#api)
  - [Behavior](#behavior)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

This KEP proposes a mechanism for garbage collection of finished and deactivated Workloads in Kueue. 
Currently, Kueue does not delete its own Kubernetes objects, leading to potential accumulation 
and unnecessary resource consumption. The proposed solution involves adding a new 
API section called `objectRetentionPolicies` to allow users to specify retention 
policies for finished and deactivated Workloads. This can be set to a specific duration or 
disabled entirely to maintain backward compatibility.

Benefits of implementing this KEP include the deletion of superfluous information, 
decreased memory footprint for Kueue, and freeing up etcd storage. The proposed changes 
are designed to be explicitly enabled and configured by users, ensuring backward compatibility. 
While the primary focus is on Workloads, the API section can be extended to include 
other Kueue-authored objects in the future. The implementation involves incorporating the 
deletion logic into the reconciliation loop and includes considerations for 
potential risks, such as handling a large number of existing Workloads. 
Overall, this KEP aims to enhance Kueue's resource management and provide users with 
more control over object retention.

## Motivation

Currently, Kueue does not delete its own Kubernetes objects, leaving it to Kubernetes' 
garbage collector (GC) to manage the retention of objects created by Kueue, such as Pods, Jobs, and Workloads. 
We do not set any expiration on these objects, so by default, they are kept indefinitely. Based on usage patterns, 
including the amount of jobs created, their complexity, and frequency over time, these objects can accumulate, 
leading to unnecessary use of etcd storage and a gradual increase in Kueue's memory footprint. Some of these objects, 
like finished Workloads, do not contain any useful information that could be used for additional purposes, such as debugging. 
That's why, based on user feedback in [#1618](https://github.com/kubernetes-sigs/kueue/issues/1618), a mechanism for garbage collection of finished Workloads 
is being proposed.

With this mechanism in place, deleting finished and deactivated Workloads has several benefits:
- **Deletion of superfluous information**: Finished Workloads do not store any useful runtime-related information 
(which is stored by Jobs), nor are they useful for debugging. They could possibly be useful for auditing purposes, 
but this is not a primary concern.
- **Decreasing Kueue memory footprint**: Finished and deactivated Workloads are loaded during Kueue's initialization and kept in memory, 
consuming resources unnecessarily.
- **Freeing up etcd storage**: Finished and deactivated Workloads are stored in etcd memory until Kubernetes' built-in GC mechanism 
collects them, but by default, Custom Resources are stored indefinitely.

### Goals

- Support the deletion of finished and deactivated Workload objects.
- Introduce configuration of global retention policies for Kueue-authored Kubernetes objects, 
- Maintain backward compatibility (the feature must be explicitly enabled and configured).

### Non-Goals

## Proposal

Add a new field called `objectRetentionPolicies` to the Kueue Configuration API, 
which will enable specifying a retention policy for finished and deactivated Workloads first under a
`WorkloadRetentionPolicy` section. This API would be extended in the future 
with options for other Kueue-managed objects.


### User Stories

#### Story 1 - Configurable retention for finished Workloads

As a Kueue administrator, I want to control the retention of 
finished Workloads to minimize the memory footprint and optimize 
storage usage in my Kubernetes cluster. I want the flexibility to 
configure a retention period to automatically delete finished Workloads after a 
specified duration or to immediately delete them upon completion.

#### Story 2 - Configurable retention for deactivated Workloads

As a Kueue administrator, I want to control the retention of 
Workloads that have been deactivated due to exceeding 
the backoff limit to minimize the memory footprint and optimize
storage usage in my Kubernetes cluster. I want the flexibility to
configure a retention period to automatically delete failed Workloads after a
specified duration or to immediately delete them upon exceeding the backoff limit.

**Note:** Immediate deletion can be configured by setting the retention period to 0 seconds.

### Notes/Constraints/Caveats

- Initially, other naming was considered for config keys. Namely, instead of "Objects," 
the word "Resources" was used, but based on feedback, it was changed 
because "Resources" bears too many meanings in the context of Kueue 
(Kubernetes resources, physical resources, etc.).
- The deletion mechanism assumes that finished Workload objects do not have 
any finalizers attached to them. After an investigation, 
the [exact place in the code](https://github.com/kubernetes-sigs/kueue/blob/a128ae3354a56670ffa58695fef1eca270fe3a47/pkg/controller/jobframework/reconciler.go#L332-L340) 
was found that removes the resource-in-use 
finalizer when the Workload state transitions to finished. If that behavior ever 
changes, or if there is an external source of a finalizer being attached 
to the Workload, it will be marked for deletion by the Kubernetes client 
but will not actually be deleted until the finalizer is removed.
- If the retention policy is misconfigured 
(the provided value is not a valid time.Duration), Kueue will fail to start 
during configuration parsing.
- In the default scenario, where the object retention policy section 
is not configured or Workload retention is either not configured 
or set to null, the existing behavior is maintained for backward compatibility. 
This means that Workload objects will not be deleted by Kueue, 
aligning with the behavior before the introduction of this feature.

### Risks and Mitigations

- **R**: In clusters with a large number of existing finished and deactivated Workloads 
(thousands, tens of thousands, etc.), it may take a significant amount of time
to delete all Workloads that qualify for deletion. Since the 
reconciliation loop is effectively synchronous, this may impact the reconciliation 
of new jobs. From the user's perspective, it may seem like Kueue's initialization 
time is very long for that initial run until all expired objects are deleted.
**M**: Unfortunately, there is no easy way to directly mitigate this without complicating the code.
A potential improvement would be to have a dedicated internal queue that handles deletion 
outside the reconciliation loop, or to delete expired objects in batches so that after 
a batch of deletions is completed, any remaining expired objects would be skipped until 
the next run of the loop. However, this would go against the synchronous nature of the reconciliation loop.
Another currently available option would be to limit client qps and burst when the feature is enabled,
allowing to mitigate the burst kube-apiserver load.

## Design Details

### API
```go
// Configuration is the Schema for the kueueconfigurations API
type Configuration struct {
    // …
    // ObjectRetentionPolicies provides configuration options for automatic deletion
    // of Kueue-managed objects. A nil value disables all automatic deletions.
    // +optional
    ObjectRetentionPolicies *ObjectRetentionPolicies `json:"objectRetentionPolicies,omitempty"`
}

// ObjectRetentionPolicies holds retention settings for different object types.
type ObjectRetentionPolicies struct {
    // Workloads configures retention for Workloads.
    // A nil value disables automatic deletion of Workloads.
    // +optional
    Workloads *WorkloadRetentionPolicy `json:"workloads,omitempty"`
}

// WorkloadRetentionPolicy defines the policies for when Workloads should be deleted.
type WorkloadRetentionPolicy struct {
    // AfterFinished is the duration to wait after a Workload finishes
    // before deleting it.
    // A duration of 0 will delete immediately.
    // A nil value disables automatic deletion.
    // Represented using metav1.Duration (e.g. "10m", "1h30m").
    // +optional
    AfterFinished *metav1.Duration `json:"afterFinished,omitempty"`
    // AfterDeactivatedByKueue is the duration to wait after *any* Kueue-managed Workload
    // (such as a Job, JobSet, or other custom workload types) has been marked
    // as deactivated by Kueue before automatically deleting it.
    // Deletion of deactivated workloads may cascade to objects not created by
    // Kueue, since deleting the parent Workload owner (e.g. JobSet) can trigger
    // garbage-collection of dependent resources.
    // A duration of 0 will delete immediately.
    // A nil value disables automatic deletion.
    // Represented using metav1.Duration (e.g. "10m", "1h30m").
    // +optional
    AfterDeactivatedByKueue *metav1.Duration `json:"afterDeactivatedByKueue,omitempty"`
}
```

### Behavior
The new behavior is as follows:

1. During Kueue's initial reconciliation loop, all previously finished and deactivated Workloads
   will be evaluated against the Workload retention policy. They will either be
   deleted immediately if they are already expired or requeued for reconciliation
   after their retention period has expired.

2. During subsequent reconciliation loops, each Workload will be evaluated
   using the same approach as in step 1. However, the evaluation will occur
   during the next reconciliation loop after the Workload was declared finished or deactivated.
   Based on the evaluation result against the retention policy, the Workload will
   either be requeued for later reconciliation or deleted.

### Test Plan

[X] I understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Unit Tests

The following new unit tests or modifications to existing ones were added to test 
the new functionality:

- `apis/config/v1beta1/defaults_test.go`
  - updated existing tests with `defaultObjectRetentionPolicies` which sets 
  `FinishedWorkloadRetention` to nil which ensures that the default 
  scenario maintains backward compatibility and no retention settings are set.
  - new unit test: `set object retention policy for finished workloads` which
  checks if provided can configuration overrides default values.
- `pkg/config/config_test.go`
  - updated existing tests with `defaultObjectRetentionPolicies` set to nil 
  which tests if for existing tests the default behavior maintains backward
  compatibility and results in no retention settings.
  - new unit test: `objectRetentionPolicies config` which sets some example 
  retention policy and then checks if it can be retrieved.
- `pkg/controller/core/workload_controller_test.go`
  - added following new tests:
    - `shouldn't delete the workload because, object retention not configured`
    - `shouldn't try to delete the workload (no event emitted) because it is already being deleted by kubernetes, object retention configured`
    - `shouldn't try to delete the workload because the retention period hasn't elapsed yet, object retention configured`
    - `should delete the workload because the retention period has elapsed, object retention configured`
    - `should delete the workload immediately after it completed because the retention period is set to 0s`

#### Integration tests

- Added behavior `Workload controller with resource retention` 
in `test/integration/controller/core/workload_controller_test.go`


### Graduation Criteria

## Implementation History

- Initial PR created for the feature pre-KEP [#2686](https://github.com/kubernetes-sigs/kueue/pull/2686).

## Drawbacks

- Deleting objects is inherently complex, and garbage collection (GC) behavior can often be 
surprising for end-users who did not configure the service themselves. Even though the 
mechanism proposed in this KEP is relatively simple and must be explicitly enabled, 
it is a global mechanism applied at the controller level. The author has added some 
observability to the controller itself (logging and emitting deletion events), 
but Kubernetes users and developers accustomed to tracking object lifecycles 
using per-object policies might be surprised by this behavior.


## Alternatives

- Leave the current state of things as is, not implementing any garbage 
collection mechanism for finished Workloads.
- Initially, a different approach was hypothesized where the deletion logic
  would not be integrated into the `Reconcile` function. Instead, a
  dedicated handler and watcher pair would manage the cleanup of finished Workloads. This approach had the following advantages and disadvantages::
  - Cons:
    - Kueue's internal state management for Workloads happens within the `WorkloadReconciler`. Moving the deletion logic elsewhere would split up code meant to handle a single resource's lifecycle.
    - Implementing a watcher specifically for expired Workloads turned out to be unexpectedly complex, leading to this approach being abandoned.
  - Pros:
    - A separate handler/watcher could offload the deletion logic, making the already large and intricate Reconcile function more focused and potentially easier to maintain.
