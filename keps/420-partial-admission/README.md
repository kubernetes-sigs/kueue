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
    - [Partial Admission Annotation](#partial-admission-annotation)
  - [Workload API](#workload-api)
  - [Validation](#validation)
  - [Scheduler / Flavorassignment](#scheduler--flavorassignment)
    - [Partial Admission for one PodSet](#partial-admission-for-one-podset)
    - [Partial Admission for multiple PodSets](#partial-admission-for-multiple-podsets)
    - [Order-Based policy (<code>order-based</code>)](#order-based-policy-order-based)
  - [Jobframework](#jobframework)
  - [batch/Job controller](#batchjob-controller)
  - [Limitations](#limitations)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [E2E tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
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

PartialAdmission in Kueue is enabled through a combination of a Kubernetes feature gate and an opt-in annotation on individual Workload objects. At the cluster level, the PartialAdmission feature (enabled by default). The job that defined minCount value for the PodSet is eligible for partial admission.

#### Features
```go
	// Enables partial admission.
	PartialAdmission featuregate.Feature = "PartialAdmission"
```

#### Partial Admission Annotation
```go
const (
  // EnabledAnnotationKey refers to the annotation key present on Jobs that support
  // partial admission.
  // This annotation is alpha-level.
  EnabledPartialAdmission = "kueue.x-k8s.io/partial-admission"
)
```

### Workload API

```go
// +kubebuilder:validation:XValidation:rule="has(self.minCount) ? self.minCount <= self.count : true", message="minCount should be positive and less or equal to count"
type PodSet struct {
  // .......

  // // count is the number of pods requested by the Job.
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
- `.spec.podSets.minCount >= 0`

### Scheduler / Flavorassignment

In case the workload proposed for the current scheduling cycle does not fit, with or without preemption, in the current available quota and any of its PodSets allow partial admission, Kueue will try to find a lower counts combination that fits the available quota with or without borrowing.

#### Partial Admission for one PodSet

The search for appropriate count value is done using binary search algorithm.

#### Partial Admission for multiple PodSets

There are multiple ways how to approach multiple podsets shrinking in case of insufficient quota. For simplicity reasons we'll start with the order-based one and will expand options if needed in future.

- **`order-based` (default)**: Shrinks the counts of the PodSets sequentially starting from the last one (suits for the cases when the podsets are ordered by priority). The Workload PodSet order is usually the same as the order of the PodSets in the Job spec.

#### Order-Based policy (`order-based`)

Under the `order-based` policy, Kueue shrinks the PodSets starting from the last one in the list and moving towards the beginning as needed.
Specifically, if multiple PodSets have variable counts, Kueue iterates over them in the order they are defined in the Workload spec, starting from the last one. It decreases the count of the current PodSet down to its `minCount` until the workload fits the available quota. If shrinking the last PodSet to its `minCount` is still not enough to fit, Kueue keeps it at its `minCount` and moves to the second-to-last PodSet, decreasing its count down to its `minCount`, and so on.
As an optimization, we will introduce a second phase (similar to the preemption algorithm): when a workload finds a combination that fits the available quota, Kueue tries to gradually put the reduced counts back. In this phase, Kueue iterates over all PodSets from the first to the last one. For each PodSet that was reduced, Kueue tries to increase its count back to the original count. If that fits, Kueue keeps it. Otherwise, Kueue performs a binary search on the PodSet's count between the current count and the original count to find the maximum count that fits.

One example when order-based policy is used, is when a multi-podset Job has identical PodSets that have different node selectors tied to different node group capacity — for example, reservation/on-demand/spot. In this case, it is preferable to keep pods running on reservation nodes rather than on-demand/spot nodes.

**Examples:**
Consider a Job with three PodSets:
- `ps0` (highest priority): `count: 1`, no `minCount` (cannot be shrunk).
- `ps1` (medium priority): `count: 4`, `minCount: 2` (can be reduced by up to 2 pods).
- `ps2` (lowest priority): `count: 20`, `minCount: 10` (can be reduced by up to 10 pods).

Total requested pods: `1 + 4 + 20 = 25` pods.

- **Scenario A: Available quota is 19 pods** (requires a reduction of 6 pods).
  1. Kueue targets the lowest priority PodSet, `ps2`, and decreases its count by 6 (from 20 to 14).
  2. The resulting counts are: `ps0: 1`, `ps1: 4`, `ps2: 14` (total 19 pods, fits the quota).
  3. Admitted counts: `ps0: 1`, `ps1: 4`, `ps2: 14`.

- **Scenario B: Available quota is 13 pods** (requires a reduction of 12 pods).
  1. Kueue targets the lowest priority PodSet, `ps2`, and decreases its count to its minimum: `10` (reduction of 10 pods). The current total count is now `1 + 4 + 10 = 15`.
  2. Since it still does not fit the quota of 13, Kueue keeps `ps2` at `10` and moves to the next lowest priority PodSet, `ps1`.
  3. Kueue decreases `ps1` by the remaining 2 pods (from 4 to 2). The resulting total count is `1 + 2 + 10 = 13` pods.
  4. Admitted counts: `ps0: 1`, `ps1: 2`, `ps2: 10`.

- **Scenario C: Available quota is 10 pods** (requires a reduction of 15 pods).
  1. Kueue targets the lowest priority PodSet, `ps2`, and decreases its count to its minimum: `10` (reduction of 10 pods). The current total count is now `1 + 4 + 10 = 15`.
  2. Since it does not fit the quota of 10, Kueue keeps `ps2` at `10` and moves to the next lowest priority PodSet, `ps1`.
  3. Kueue decreases `ps1` to its minimum: `2` (reduction of 2 pods). The current total count is now `1 + 2 + 10 = 13`.
  4. Since it still does not fit the quota of 10, and the remaining PodSet `ps0` does not allow partial admission (has no `minCount`), the search fails.
  5. The job remains unadmitted.

- **Scenario D: Multiple resource flavors (illustrates the second phase)**
  Assume `ps1` and `ps2` are tied to different resource flavors, `rf1` and `rf2`, respectively.
  The available quota for `rf1` is 2 pods (requires a reduction of at least 2 pods for `ps1`), and the available quota for `rf2` is 20 pods (full capacity for `ps2`).
  1. In the first phase, Kueue targets the lowest priority PodSet, `ps2` (tied to `rf2`), and decreases its count to its minimum `10` (reduction of 10 pods) in search of a fit. The intermediate total count is `1 + 4 + 10 = 15` pods.
  2. Since the workload still does not fit because of the constraint on `rf1` (which only allows 2 pods for `ps1` but it requests 4), Kueue keeps `ps2` at `10` and moves to the next lowest priority PodSet, `ps1`.
  3. Kueue decreases `ps1` by 2 pods (from 4 to 2) to fit the available quota of `rf1`. The resulting total count is `1 + 2 + 10 = 13` pods.
  4. The first phase successfully finds a combination (`ps0: 1`, `ps1: 2`, `ps2: 10`) that fits the available quotas.
  5. In the second phase (optimization), Kueue iterates over all PodSets from the first to the last (`ps0`, `ps1`, `ps2`) and tries to restore the reduced counts.
     - `ps1` was reduced to 2. Kueue tries to increase its count back to 4, but this fails since `rf1` only has a quota of 2. `ps1` remains at 2.
     - `ps2` was reduced to 10. Kueue tries to increase its count back to 20. This succeeds since `rf2` has 20 available quota.
  6. Admitted counts: `ps0: 1`, `ps1: 2`, `ps2: 20`.

The accepted number of pods in each PodSet is recorded in `workload.Status.Admission.PodSetAssignments[*].Count`.

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

### Limitations

Partial admission is not supported for concurrent admission to avoid increasing complexity. The webhook will reject the creation of workloads that support partial admission for a ClusterQueue with a concurrent admission policy. 

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
    - `partial admission multiple variable pod sets, order-based policy`: verifies shrinking order when the order-based policy is set, starting from the last PodSet.
    - `partial admission disabled, multiple variable pod sets`: verifies that no partial admission is performed if features/annotations are not active.
  - `pkg/scheduler/scheduler_tas_test.go`:
    - `TAS workload gets scheduled as trimmed by partial admission`: verifies that Topology Aware Scheduling is compatible with partial admission.
    - `reclaim within cohort; preempting with partial admission`: verifies that reclaiming quota within a cohort works alongside preemption and partial admission.

- **Webhooks**:
  - `pkg/webhooks/workload_webhook_test.go`: validates that `minCount` cannot be negative or larger than the base `count`.

- **Controllers**:
  - `pkg/controller/jobs/job/job_controller_test.go`: verifies job controller's `RunWithPodSetsInfo` and `RestorePodSetsInfo` logic, updating/restoring parallelism correctly when job is unsuspended or suspended.

#### Integration tests

- **Controllers & Scheduler**:
  - `test/integration/singlecluster/controller/jobs/job/job_controller_test.go`:
    - `Should schedule jobs with partial admission`: verifies a complete integration flow where a Job with `kueue.x-k8s.io/job-min-parallelism` is suspended, partially admitted with reduced parallelism, and its original parallelism is restored when the workload is stopped.
- **Workload Webhook**:
  - `test/integration/singlecluster/webhook/core/workload_test.go`:
    - `invalid podSet minCount (negative)`: verifies negative minCount values are rejected.
    - `invalid podSet minCount (too big)`: verifies minCount larger than count is rejected.
    - `too many variable count podSets`: verifies that workloads with multiple variable count PodSets are rejected.

#### E2E tests

- `test/e2e/singlecluster/baseline/job_test.go`:
  - `Should partially admit the Job if configured and not fully fits`: verifies that a real Job is successfully admitted with a reduced parallelism count matching the available cluster resources.

### Graduation Criteria

## Implementation History
- 07/07/2023 - Partial admission for batch/Job was added that supports only 1 podSet with minCount value.

## Drawbacks

## Alternatives

For partial admission of multiple PodSets, another approach that was considered is proportional shrinking. However it was discarded because of insufficiency for some use cases. For example, when shrinking two PodSets from initial counts of (2, 2) down to 3 pods, a proportional approach might result in (1, 1) due to rounding, rather than an optimal (2, 1) or (1, 2).