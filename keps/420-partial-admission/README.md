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
    - [Order-Based Policy (<code>order-based</code>)](#order-based-policy-order-based)
    - [Proportional Policy (<code>proportional</code> - default)](#proportional-policy-proportional---default)
  - [Jobframework](#jobframework)
  - [batch/Job controller](#batchjob-controller)
  - [kubeflow/MPIJob controller](#kubeflowmpijob-controller)
  - [RayJob/RayService/RayCluster controller](#rayjobrayserviceraycluster-controller)
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

In case the workload proposed for the current scheduling cycle does not fit, with or without preemption, in the current available quota and any of its PodSets allow partial admission, try to find a lower counts combination that fits the available quota with or without borrowing.

The policy for shrinking PodSet counts is configured via the `kueue.x-k8s.io/partial-admission-policy` annotation on the Job. The supported policies are:
- **`proportional` (default)**: Shrinks the counts of all variable PodSets proportionally to their allowable reduction ranges.
- **`order-based`**: Shrinks the counts of the PodSets sequentially starting from the last one (suits for the cases when the podsets are ordered by priority). The Workload PodSet order is usually the same as the order of the PodSet in the Job spec. For the RayCluster the Workload PodSets starting from Head PodSet and following by WorkerGroupPodSets in the same order as in RayCluster.

#### Order-Based Policy (`order-based`)

Under the `order-based` policy, Kueue shrinks the PodSets starting from the last one and moving towards the beginning as needed.
Specifically, if multiple PodSets have variable counts, Kueue iterates over them in the order they are defined in the Workload spec, starting from the last one. It decreases the count of the current PodSet down to its `minCount` until the workload fits the available quota. If shrinking the last PodSet to its `minCount` is still not enough to fit, Kueue keeps it at its `minCount` and moves to the second-to-last PodSet, decreasing its count down to its `minCount`, and so on.
As an optimization, we will introduce a second phase (similar to the preemption algorithm): when a workload finds a combination that fits the available quota, Kueue tries to gradually put the reduced counts back. In this phase, Kueue iterates over all PodSets from the first to the last one. For each PodSet that was reduced, Kueue tries to increase its count back to the original count. If that fits, Kueue keeps it. Otherwise, Kueue performs a binary search on the PodSet's count between the current count and the original count to find the maximum count that fits.

One example when order-based policy is used, is when the RayCluster has identical WorkerGroupPodSets, that have different node selectors that are tight to different node group capacity — for example, reservation/on-demand/spot. In this case, it is preferable to keep workers to run on reservation nodes than on-demand/spot nodes.

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

#### Proportional Policy (`proportional` - default)

Under the `proportional` policy, Kueue reduces the counts of all variable PodSets proportionally to their maximum allowable reduction range (i.e., `count - minCount`).

For each PodSet $j$ that allows partial admission, let:
- $C_j$ be the maximum requested count.
- $M_j$ be the minimum acceptable count (`minCount`).
- $D_j = C_j - M_j$ be the maximum reduction delta for PodSet $j$.
- $T_D = \sum D_j$ be the sum of all maximum reduction deltas.

To find the optimal fit, Kueue performs a binary search on a scaling index $i$ in the range $[0, T_D]$. For a given index $i$, the count for PodSet $j$ is computed as:
$$currentCount_j = C_j - \lfloor \frac{D_j \times i}{T_D} \rfloor$$

Kueue finds the smallest index $i$ (corresponding to the minimal reduction) for which the resulting counts fit the available quota.

**Example:**
Consider a Job with three PodSets:
- `ps0`: `count: 1`, no `minCount` ($D_0 = 0$).
- `ps1`: `count: 4`, `minCount: 2` ($D_1 = 2$).
- `ps2`: `count: 20`, `minCount: 10` ($D_2 = 10$).

Total requested pods: `1 + 4 + 20 = 25` pods.
Total reduction delta: $T_D = 0 + 2 + 10 = 12$ pods.

- **Scenario A: Available quota is 19 pods** (requires a reduction of at least 6 pods).
  - For $i = 6$:
    - `ps0` count: $1 - \lfloor \frac{0 \times 6}{12} \rfloor = 1 - 0 = 1$
    - `ps1` count: $4 - \lfloor \frac{2 \times 6}{12} \rfloor = 4 - 1 = 3$
    - `ps2` count: $20 - \lfloor \frac{10 \times 6}{12} \rfloor = 20 - 5 = 15$
    - Total count: `1 + 3 + 15 = 19` pods. This fits the 19-pod quota.
  - For $i = 5$:
    - `ps0` count: $1 - \lfloor \frac{0 \times 5}{12} \rfloor = 1 - 0 = 1$
    - `ps1` count: $4 - \lfloor \frac{2 \times 5}{12} \rfloor = 4 - 0 = 4$
    - `ps2` count: $20 - \lfloor \frac{10 \times 5}{12} \rfloor = 20 - 4 = 16$
    - Total count: `1 + 4 + 16 = 21` pods. This does not fit the 19-pod quota.
  - Thus, the binary search selects $i = 6$.
  - Admitted counts: `ps0: 1`, `ps1: 3`, `ps2: 15` (total 19 pods).

- **Scenario B: Available quota is 13 pods** (requires a reduction of at least 12 pods).
  - For $i = 12$:
    - `ps0` count: $1 - \lfloor \frac{0 \times 12}{12} \rfloor = 1 - 0 = 1$
    - `ps1` count: $4 - \lfloor \frac{2 \times 12}{12} \rfloor = 4 - 2 = 2$
    - `ps2` count: $20 - \lfloor \frac{10 \times 12}{12} \rfloor = 20 - 10 = 10$
    - Total count: `1 + 2 + 10 = 13` pods. This fits the 13-pod quota.
  - For $i = 11$:
    - `ps0` count: $1 - \lfloor \frac{0 \times 11}{12} \rfloor = 1 - 0 = 1$
    - `ps1` count: $4 - \lfloor \frac{2 \times 11}{12} \rfloor = 4 - 1 = 3$
    - `ps2` count: $20 - \lfloor \frac{10 \times 11}{12} \rfloor = 20 - 9 = 11$
    - Total count: `1 + 3 + 11 = 15` pods. This does not fit the 13-pod quota.
  - Thus, the binary search selects $i = 12$.
  - Admitted counts: `ps0: 1`, `ps1: 2`, `ps2: 10` (total 13 pods).

- **Scenario C: Available quota is 10 pods** (requires a reduction of at least 15 pods).
  - Even with the maximum possible index $i = 12$ (maximum reduction), the total count is `1 + 2 + 10 = 13` pods, which does not fit in 10 pods.
  - Thus, the binary search fails, and the job remains unadmitted.

As an optimization, we will introduce an increased step: when a workload finds a combination that fits the available quota, Kueue could try to find the combination that can be reached with minimum pod loss by checking if any of the PodSets with count == min count can be increased up to the original count. 
The increased step is not necessary for elastic workloads, but the non-elastic workloads will benefit from it.

The accepted number of pods in each PodSet is recorded in `workload.Status.Admission.PodSetAssignments[*].ResourceUsage.Count`.

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

### kubeflow/MPIJob controller

In case of MPIJob `j.Spec.RunPolicy.SchedulingPolicy.MinAvailable` can be used to provide a `minimumCount` for the `Worker` PodSets while updating `j.Spec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeWorker].Replicas` before unsuspending the job and after suspending it.

Whether an MPIJob supports partial admission or not can be deduced based on `MinAvailable` without the need of a dedicated annotation.
Additional research is needed into the potential usage of multiple variable count PodSets.

### RayJob/RayService/RayCluster controller

RayClusters with 'kueue.x-k8s.io/partial-admission=true' annotation are eligible for partial admission. The
RayCluster.workerGroupSpec[i].minReplicas will be translated to the PodSet.MinCount for the Worker PodSet.
There will be no update on RayCluster object. If the number of admitted pods is less than requested count, the leftover pods should remain scheduling gated. In order to achieve that, the same mechanism as for elastic workloads will be applied – the podTemplate will be initially gated and then the correct amount of pods will be ungated.

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
    - `partial admission multiple variable pod sets, proportional policy`: verifies shrinking order and flavor assignment when multiple variable count PodSets are defined using the default proportional policy.
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
  - `test/integration/singlecluster/controller/jobs/raycluster/raycluster_controller_test.go`:
    - `Should schedule RayClusters with partial admission order policy`: verifies a complete integration flow where a RayCluster with partial admission annotation is admitted with reduced worker count according to the order.
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
- 07/07/2023 - Partial admission for batch.job was added that support only 1 podSet with minCount value.


## Drawbacks


## Alternatives