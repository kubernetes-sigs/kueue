# KEP-420: Allow partial admission of PodSets

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1 (partial admission)](#story-1-partial-admission)
    - [Story 2 (graceful scale up handling)](#story-2-graceful-scale-up-handling)
    - [Story 3 (opportunistic scale up)](#story-3-opportunistic-scale-up)
    - [Story 4 (multi-podset RayJob)](#story-4-multi-podset-rayjob)
- [Design Details](#design-details)
  - [Enablement](#enablement)
    - [Features](#features)
    - [Partial Admission Annotation](#partial-admission-annotation)
    - [Scenario Summary](#scenario-summary)
  - [Workload API](#workload-api)
  - [Scheduler / Flavorassignment](#scheduler--flavorassignment)
    - [Partial Admission for one PodSets](#partial-admission-for-one-podsets)
    - [Partial Admission for multiple PodSets](#partial-admission-for-multiple-podsets)
    - [Order-Based policy (<code>order-based</code>)](#order-based-policy-order-based)
  - [Jobframework](#jobframework)
  - [ElasticJob](#elasticjob)
    - [Opportunistic scale up when capacity is freed](#opportunistic-scale-up-when-capacity-is-freed)
    - [Example: Two-Step Scale Up under Quota Constraints (with Partial Admission)](#example-two-step-scale-up-under-quota-constraints-with-partial-admission)
      - [Step 0: Job Creation (Initial Size: 5)](#step-0-job-creation-initial-size-5)
      - [Step 1: Scale Up from 5 to 10 (Quota Constraint: 7), partial admission of scale up](#step-1-scale-up-from-5-to-10-quota-constraint-7-partial-admission-of-scale-up)
      - [Step 2: Scale Up to 12 (Quota Constraint: 7), scale up isn't admitted](#step-2-scale-up-to-12-quota-constraint-7-scale-up-isnt-admitted)
      - [Step 3: Quota increases to 12, opportunistic scale up when capacity is freed](#step-3-quota-increases-to-12-opportunistic-scale-up-when-capacity-is-freed)
  - [batch/Job controller](#batchjob-controller)
  - [kubeflow/MPIJob controller](#kubeflowmpijob-controller)
  - [RayJob/RayService/RayCluster controller](#rayjobrayserviceraycluster-controller)
  - [Limitations](#limitations)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [E2E tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [Stable(GA)](#stablega)
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

#### Story 2 (graceful scale up handling)

As a user running an autoscaled job (like a RayJob), when my job requests to scale up (e.g. from 1000 to 5000 pods) but the cluster only has capacity for a fraction of the scale-up request (e.g., 2000 more pods), I want Kueue to gracefully admit the scale-up up to the available capacity (admitting 2000 additional pods) rather than rejecting the scale-up request entirely.

#### Story 3 (opportunistic scale up)

As a user running an elastic job (like a RayJob), when my job is admitted with partial capacity due to resource constraints, I want the job to dynamically scale up to its full requested capacity as soon as other workloads complete and resources become available in the cluster, maximizing resource utilization and reducing the job's overall completion time.

#### Story 4 (multi-podset RayJob)

As a user of a multi-podset RayJob (which defines a head pod and multiple worker groups, potentially targeting different resource flavors or node groups like reservation, on-demand, or spot), I want to enable partial admission such that Kueue reduces the worker groups sequentially starting from the least critical (e.g., spot or low-priority worker groups defined last in the spec) while preserving the capacity of the more critical worker groups.

## Design Details

### Enablement

PartialAdmission in Kueue is enabled through a combination of a Kubernetes feature gate and an opt-in annotation on individual Workload objects. At the cluster level, the PartialAdmission feature (enabled by default) and PartialAdmissionForElasticJobs feature for ElasticJobs, must be enabled via the corresponding Kueue feature gate.

Once the feature gate is enabled, individual Job objects can opt into partial admission by including the ``kueue.x-k8s.io/partial-admission="true"` annotation. 
When both conditions are met, Kueue treats the Workload as eligible for partial admission. 

#### Features
```go
	// Enables partial admission for non-elastic jobs.
	PartialAdmission featuregate.Feature = "PartialAdmission"

	// Enables partial admission for elastic jobs.
	PartialAdmissionForElasticJobs featuregate.Feature = "PartialAdmissionForElasticJobs"
```

#### Partial Admission Annotation
```go
const (
  // EnabledAnnotationKey refers to the annotation key present on Job's that support
  // workload slicing.
  // This annotation is alpha-level.
  EnabledPartialAdmission = "kueue.x-k8s.io/partial-admission"
)
```

#### Scenario Summary

The table below summarizes the scheduling and admission behaviors for different combinations of the `kueue.x-k8s.io/partial-admission` and `kueue.x-k8s.io/elastic-job` annotations for `batch/v1.Job` and KubeRay (`RayJob` / `RayCluster`) objects:

| Framework | `partial-admission` | `elastic-job` | Scheduling & Admission Behavior |
| --- | --- | --- | --- |
| **batch/Job** | `true` | `true` | **Partial Admission + Workload Slicing**: Kueue can shrink the Job's podset down to its `job-min-parallelism`. The Job's spec (`.spec.parallelism`) remains unmodified. Kueue uses scheduling gates to ungate only the admitted pod count. A pending workload slice is created to dynamically scale up (opportunistic scale up) when more quota becomes available. |
| **batch/Job** | `true` | `false`/not present | **Partial Admission (Traditional)**: Kueue shrinks the Job's podset down to its `job-min-parallelism`. Upon admission, Kueue modifies the Job's spec (`.spec.parallelism`) directly to match the admitted count. No scheduling gates are used, and no future scale-up occurs for this workload. |
| **batch/Job** | `false`/not present | `true` | **Workload Slicing Only**: Kueue cannot shrink the Job's podset during initial scheduling; the job must fit its full requested capacity to be admitted. If admitted, scheduling gates are used. If the user manually scales up the Job, Kueue creates a new workload slice for the scale-up request, but this scale-up will only be admitted if it can be fully satisfied (no partial admission of the scale-up). |
| **KubeRay** | `true` | `true` | **Partial Admission + Workload Slicing**: Kueue can shrink the worker groups down to their `minReplicas` (defined in the worker spec). The Ray object's spec remains unmodified. Admission is managed via scheduling gates. A pending workload slice is created to allow KubeRay to scale up (opportunistic scale up) worker pods as more quota becomes available. |
| **KubeRay** | `true` | `false`/not present | **Not supported / Out of scope**: Kueue does not support partial admission for non-autoscaled KubeRay jobs at this time. |
| **KubeRay** | `false`/not present | `true` | **Workload Slicing Only**: Kueue cannot shrink the worker groups; they must fit their full requested replicas to be admitted. Once admitted, scheduling gates are used. If the autoscaler scales up the worker groups, Kueue creates a workload slice representing the scale-up request, but this scale-up is only admitted if there is enough quota to satisfy the full scale-up request. |

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

### Scheduler / Flavorassignment

In case the workload proposed for the current scheduling cycle does not fit, with or without preemption, in the current available quota and any of its PodSets allow partial admission, kueue will try to find a lower counts combination that fits the available quota with or without borrowing.

#### Partial Admission for one PodSets

The search for appopriate count value is done using binary search algorithm.

#### Partial Admission for multiple PodSets

The are multiple ways how to approach multiple podsets shrinking in case of insufficient quota. For simplisity reason we'll start with the order-based one and will expand option if needed in future.
Another approch that was considered is proportional. However it was disgarded because of insufficiency for some use cases.

- **`order-based` (default)**: Shrinks the counts of the PodSets sequentially starting from the last one (suits for the cases when the podsets are ordered by priority). The Workload PodSet order is usually the same as the order of the PodSet in the Job spec. For the RayCluster the Workload PodSets starting from Head PodSet and following by WorkerGroupPodSets in the same order as in RayCluster.

#### Order-Based policy (`order-based`)

Under the `order-based` policy, Kueue shrinks the PodSets starting from the last one in the list and moving towards the beginning as needed.
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

### ElasticJob

For ElasticJobs, updating `job.spec.parallelism` or `job.spec.count` could cause race conditions between partial admission and scaling up/down activity. 
To avoid this, the `podSets` count won't be updated in `RunWithPodSetsInfo` for elastic jobs. Instead, the workload controller will use the `workload.Status.Admission.PodSetAssignments[*].Count` value to calculate the number of pods from which Kueue should remove scheduling gates. 
The `minCount` value for an ElasticJob workload will represent the currently admitted value + 1.
Also, a new Workload representing the full job will be created and added to the queue, to admit the remaining capacity once it becomes available.

#### Opportunistic scale up when capacity is freed

In order to schedule remaining pods after partial admission, workload controller will create a new workload representing the full job and add it to the queue. The scheduler will admit new workload and replace the old workload via workload slice mechanism as the capacity becomes available.

#### Example: Two-Step Scale Up under Quota Constraints (with Partial Admission)

Consider a scenario where:
1. The ClusterQueue has a total quota of **7** for the requested resource flavor.
2. The Job is configured for both `ElasticJobs` and `PartialAdmission`.
3. The user performs a two-step scale up of the Job: starting at **5** replicas, scaling up to **10**, and then to **12**.

##### Step 0: Job Creation (Initial Size: 5)
* **Job spec.parallelism**: 5
* **Workloads**:
  * `wl-A` (Admitted):
    * `spec.podSets.count` = 5
    * `spec.podSets.minCount` = 2
    * `status.admission.count` = 5
* **Controller Actions**:
  1. **Job Controller**: Detects the Job creation, sets `.spec.suspend = false`, and creates 5 Pods. Due to Kueue's webhook mutation, the Pods are created with the `kueue.x-k8s.io/elastic-job` scheduling gate.
  2. **Kueue Job Framework / Workload Controller**: Detects the Job and creates `wl-A` with `spec.podSets.count = 5` and `spec.podSets.minCount = 2`.
  3. **Kueue Scheduler**: Evaluates `wl-A`. Since the requested 5 pods fit within the available quota of 7, it admits `wl-A` (`status.admission.count = 5`), reserving 5 units of quota.
  4. **ElasticJobUngater Controller**: Detects that `wl-A` is admitted and removes the scheduling gate from the 5 pods.
  5. **Kube-scheduler**: Schedules the 5 ungated pods, which transition to the Running state.
* **Quota usage**: 5/7 (2 available).

##### Step 1: Scale Up from 5 to 10 (Quota Constraint: 7), partial admission of scale up
* **Job spec.parallelism**: 10
* **Workloads**:
  * `wl-A` (Finished - aggregated/replaced by `wl-B`)
  * `wl-B` (Admitted - Partially):
    * `spec.podSets.count` = 10
    * `spec.podSets.minCount` = 6 (the current running count 5 + 1)
    * `status.admission.count` = 7
  * `wl-C` (Pending, since there is no capacity for 10 pods)
    * `spec.podSets.count` = 10
    * `spec.podSets.minCount` = 8 (the current running count 7 + 1)
* **Controller Actions**:
  1. **Job Controller**: Detects the parallelism increase and creates 5 new Pods (total 10 pods: 5 running, 5 gated). The new pods are created with the `kueue.x-k8s.io/elastic-job` scheduling gate.
  2. **Workload Controller**: Observes the scale-up and creates a new Workload slice `wl-B` with `spec.podSets.count = 10` and `spec.podSets.minCount = 6` (inheriting/adjusting the minimum count based on the currently running/admitted count of 5 + 1 from `wl-A`). It is annotated as a replacement for `wl-A` via `kueue.x-k8s.io/workload-slice-replacement-for`.
  3. **Kueue Scheduler**: Evaluates `wl-B`. Since it replaces `wl-A`, it calculates the demand: `10 (new request) - 5 (already admitted in wl-A) = 5`. The available quota is only 2. Since the Workload could not be fully admitted, the Kueue scheduler evaluates whether it can partially admit the workload with any count between 6 and 10. Given the available quota of 2, the scheduler admits `wl-B` with a count of `5 + 2 = 7` (`status.admission.count = 7`), reserving 2 more units of quota (total 7).
  4. **WorkloadSlice Controller**: Creates another WorkloadSlice `wl-C` that represents the current state of the job with `.spec.podSets[0].count` = 10 and `spec.podSets.minCount` = 8. This workload is added to the queue to be evaluated when capacity becomes available, and it currently stays `Pending`. `wl-A` is marked as finished.
  5. **ElasticJobUngater Controller**: Detects that `wl-B` is admitted with count 7. It removes the scheduling gate from 2 of the new pods (bringing running pods to 7). The other 3 new pods remain gated.
* **Quota usage**: 7/7 (0 available).

##### Step 2: Scale Up to 12 (Quota Constraint: 7), scale up isn't admitted
* **Job spec.parallelism**: 12
* **Workloads**:
  * `wl-B` (Admitted)
  * `wl-C` (Updated, keep pending):
    * `spec.podSets.count` = 12
    * `spec.podSets.minCount` = 8 (7 + 1)
* **Controller Actions**:
  1. **Job Controller**: Detects the parallelism increase and creates 2 more Pods (total 12 pods: 7 running, 5 gated). The new pods are created with the `kueue.x-k8s.io/elastic-job` scheduling gate.
  2. **Workload Controller**: Detects the update. Since the Job's parallelism is updated to 12, Kueue updates the pending workload `wl-C` with `spec.podSets.count = 12`.
  3. **Kueue Scheduler**: Evaluates `wl-C`. The demand is `12 - 7 = 5`. The available quota is 0, so the scheduler puts `wl-C` in the queue.

##### Step 3: Quota increases to 12, opportunistic scale up when capacity is freed
If the available quota in the ClusterQueue increases to 12 (or more) in the future:
* **Workloads**:
  * `wl-B` (Finished)
  * `wl-C` (Admitted):
    * `spec.podSets.count` = 12
    * `spec.podSets.minCount` = 8 (7 + 1)
    * `status.admission.count` = 12

In the next scheduler loop, the Kueue scheduler will evaluate and admit `wl-C`. It will also update its `spec.podSets.minCount` to match the job's minCount.


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

The RayCluster.workerGroupSpec[i].replicas * numOfHosts will be translated to the PodSet.Count, and minReplicas * numOfHosts will be translated to the PodSet.MinCount for the Worker PodSet.

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

#### Alpha
Partial admission for elastic workloads is implemented behind the
`PartialAdmissionForElasticJobs` feature gate, with basic functionality (such as RayCluster
and RayJob support) covered by integration tests and documentation.

#### Beta
Positive feedback from Alpha, broader test coverage (handling scale up/down race conditions),
and any documented follow-ups addressed.

#### Stable(GA)
The feature has spent at least one release cycle in beta with no
major outstanding bugs.


## Implementation History
- 07/07/2023 - Partial admission for batch.job was added that support only 1 podSet with minCount value.


## Drawbacks


## Alternatives