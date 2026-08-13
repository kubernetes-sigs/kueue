# KEP-12100: Partial ScaleUp for ElasticJob

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1 (graceful scale up handling)](#story-1-graceful-scale-up-handling)
    - [Story 2 (opportunistic scale up)](#story-2-opportunistic-scale-up)
    - [Story 3 (multi-podset RayJob)](#story-3-multi-podset-rayjob)
- [Design Details](#design-details)
  - [Enablement](#enablement)
    - [Features](#features)
    - [Partial ScaleUp Annotation](#partial-scaleup-annotation)
  - [Workload API](#workload-api)
  - [Scheduler / Flavorassignment](#scheduler--flavorassignment)
  - [Opportunistic scale up when capacity is freed](#opportunistic-scale-up-when-capacity-is-freed)
    - [Example: Two-Step Scale Up under Quota Constraints (with Partial Admission)](#example-two-step-scale-up-under-quota-constraints-with-partial-admission)
      - [Step 0: Job Creation (Initial Size: 5)](#step-0-job-creation-initial-size-5)
      - [Step 1: Scale Up from 5 to 10 (Quota Constraint: 7), partial admission of scale up](#step-1-scale-up-from-5-to-10-quota-constraint-7-partial-admission-of-scale-up)
      - [Step 2: Scale Up to 12 (Quota Constraint: 7), scale up isn't admitted](#step-2-scale-up-to-12-quota-constraint-7-scale-up-isnt-admitted)
      - [Step 3: Quota increases to 12, opportunistic scale up when capacity is freed](#step-3-quota-increases-to-12-opportunistic-scale-up-when-capacity-is-freed)
  - [RayJob/RayService/RayCluster controller](#rayjobrayserviceraycluster-controller)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [E2E tests](#e2e-tests)
<!-- /toc -->

## Summary

Add an optional way of allowing partial scale up for elastic workloads if the full scale up could not be admitted due to quota constraints. Only `RayJob`, `RayService`, and `RayCluster` integrations will initially support this feature.

## Motivation

In elastic workloads (such as RayJob with autoscaling), jobs dynamically scale up their pod counts during execution. When a scale-up request cannot be fully satisfied due to cluster quota constraints, rejecting the scale-up request entirely leaves available quota unused. Conversely, admitting a scale-up request partially allows the workload to make progress with the currently available resources while waiting for additional capacity.

### Goals

- Provide an opt-in mechanism for elastic jobs to partially scale up when requested scale-up capacity exceeds available quota.
- Opportunistically scale up the remaining requested pods as quota becomes available.
- Support multi-podset elastic jobs (e.g., RayJob with multiple worker groups).
- Support partial scale up  for `RayJob`, `RayService`, and `RayCluster` integrations.

### Non-Goals

- Partial admission for initial job creation when only partial scale-up is configured.
- Support for `batch/v1 Job` (`batch.job`) integration.

## Proposal

Using partial admission mechanism to support partial scale up for elastic workloads (`RayJob`, `RayService`, `RayCluster`).

### User Stories

#### Story 1 (graceful scale up handling)

As a user running an autoscaled job (like a RayJob), when my job requests to scale up (e.g. from 1000 to 5000 pods) but the cluster only has capacity for a fraction of the scale-up request (e.g., 2000 more pods), I want Kueue to gracefully admit the scale-up up to the available capacity (admitting 2000 additional pods) rather than rejecting the scale-up request entirely.

#### Story 2 (opportunistic scale up)

As a user running an elastic job (like a RayJob), when my job is admitted with partial capacity due to resource constraints, I want the job to dynamically scale up to its full requested capacity as soon as other workloads complete and resources become available in the cluster, maximizing resource utilization and reducing the job's overall completion time.

#### Story 3 (multi-podset RayJob)

As a user of a multi-podset RayJob (which defines a head pod and multiple worker groups, potentially targeting different resource flavors or node groups like reservation, on-demand, or spot), I want to enable partial admission such that Kueue reduces the worker groups sequentially starting from the least critical (e.g., spot or low-priority worker groups defined last in the spec) while preserving the capacity of the more critical worker groups.

## Design Details

For ElasticJobs, updating `job.spec.parallelism` could cause race conditions between partial scale up and scaling up/down activity. 
To avoid this, the `job.spec.parallelism` won't be updated in `RunWithPodSetsInfo` for elastic jobs. Instead, the workload controller will use the `workload.Status.Admission.PodSetAssignments[*].Count` value to calculate the number of pods from which Kueue should remove scheduling gates. 
The `Workload.Spec.PodSets[].MinCount` for an ElasticJob workload will represent the currently admitted value + 1.
Also, a new Workload representing the full job will be created and added to the queue, to admit the remaining capacity once it becomes available (opportunistic scale up).

### Enablement

PartialScaleUpForElasticJob for elastic jobs in Kueue is enabled through a combination of a Kubernetes feature gate and an opt-in annotation on individual Workload objects. At the cluster level, the PartialScaleUpForElasticJob feature (disabled by default) must be enabled via the corresponding Kueue feature gate.

Once the feature gate is enabled, individual Job objects can opt into partial admission by including the `kueue.x-k8s.io/elastic-job-partial-scale-up="true"` annotation. 
When both conditions are met, Kueue treats the Workload as eligible for partial scale up. 

#### Features
```go
	// Enables partial scale up for elastic jobs.
	PartialScaleUpForElasticJob featuregate.Feature = "PartialScaleUpForElasticJob"
```

#### Partial ScaleUp Annotation
```go
const (
  // EnabledAnnotationKey refers to the annotation key present on Jobs that support
  // partial scale up.
  // This annotation is alpha-level.
  EnabledPartialScaleUpForElasticJob = "kueue.x-k8s.io/partial-scale-up-for-elastic-job"
)
```
The proposal relies on following existing API:

### Workload API

```go
// +kubebuilder:validation:XValidation:rule="has(self.minCount) ? self.minCount <= self.count : true", message="minCount should be positive and less or equal to count"
type PodSet struct {
  // .......

  // count is the number of pods requested by the Job.
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

The partial admission mechanism will be applied for the workload that represents scale up.

### Opportunistic scale up when capacity is freed

In order to schedule remaining pods after partial scale up, the workload controller will create a new workload representing the full job and add it to the queue. The scheduler will admit the new workload and replace the old workload via the workload slice mechanism as capacity becomes available.
The newly created workload for opportunistic scale up should have a different name from the admitted workload.

#### Example: Two-Step Scale Up under Quota Constraints (with Partial Admission)

Consider a scenario where:
1. The ClusterQueue has a total quota of **7** for the requested resource flavor.
2. The `RayCluster` is configured for both `ElasticJobs` and `PartialScaleUpForElasticJob`.
3. The user performs a two-step scale up of the `RayCluster`: starting at **5** replicas, scaling up to **10**, and then to **12**.

##### Step 0: Job Creation (Initial Size: 5)
* **RayCluster worker group replicas**: 5
* **Workloads**:
  * `wl-A` (Admitted):
    * `spec.podSets.count` = 5
    * `spec.podSets.minCount` = 5 (partial admission disabled for initial creation)
    * `status.admission.count` = 5
* **Controller Actions**:
  1. **KubeRay Controller**: Creates `RayCluster`.
  2. **Workload Controller**: Detects the `RayCluster` and creates `wl-A` with `spec.podSets.count = 5` and `spec.podSets.minCount = 5`.
  3. **Kueue Scheduler**: Evaluates `wl-A`. Since the requested 5 pods fit within the available quota of 7, it admits `wl-A` (`status.admission.count = 5`), reserving 5 units of quota.
  4. **ElasticJobUngater Controller**: Detects that `wl-A` is admitted and removes the scheduling gate from the 5 pods respecting the pod indexing.
  5. **Kube-scheduler**: Schedules the 5 ungated pods, which transition to the Running state.
* **Quota usage**: 5/7 (2 available).

##### Step 1: Scale Up from 5 to 10 (Quota Constraint: 7), partial admission of scale up
* **RayCluster worker group replicas**: 10
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
  1. **KubeRay Controller**: Increase worker group replica count and creates 5 new Pods (total 10 pods: 5 running, 5 gated). The new pods are created with the `kueue.x-k8s.io/elastic-job` scheduling gate.
  2. **Workload Controller**: Observes the scale-up and creates a new Workload slice `wl-B` with `spec.podSets.count = 10` and `spec.podSets.minCount = 6` (inheriting/adjusting the minimum count based on the currently running/admitted count of 5 + 1 from `wl-A`). It is annotated as a replacement for `wl-A` via `kueue.x-k8s.io/workload-slice-replacement-for`.
  3. **Kueue Scheduler**: Evaluates `wl-B`. Since it replaces `wl-A`, it calculates the demand: `10 (new request) - 5 (already admitted in wl-A) = 5`. The available quota is only 2. Since the Workload could not be fully admitted, the Kueue scheduler evaluates whether it can partially admit the workload with any count between 6 and 10. Given the available quota of 2, the scheduler admits `wl-B` with a count of `5 + 2 = 7` (`status.admission.count = 7`), reserving 2 more units of quota (total 7).
  4. **WorkloadSlice Controller**: Creates another WorkloadSlice `wl-C` that represents the current state of the job with `.spec.podSets[0].count` = 10 and `spec.podSets.minCount` = 8. This workload is added to the queue to be evaluated when capacity becomes available, and it currently stays `Pending`. `wl-A` is marked as finished.
  5. **ElasticJobUngater Controller**: Detects that `wl-B` is admitted with count 7. It removes the scheduling gate from 2 of the new pods (bringing running pods to 7). The other 3 new pods remain gated.
* **Quota usage**: 7/7 (0 available).

##### Step 2: Scale Up to 12 (Quota Constraint: 7), scale up isn't admitted
* **RayCluster worker group replicas**: 12
* **Workloads**:
  * `wl-B` (Admitted)
  * `wl-C` (Updated, keep pending):
    * `spec.podSets.count` = 12
    * `spec.podSets.minCount` = 8 (7 + 1)
* **Controller Actions**:
  1. **KubeRay Controller**: Increase worker group replica count and creates 2 more Pods (total 12 pods: 7 running, 5 gated). The new pods are created with the `kueue.x-k8s.io/elastic-job` scheduling gate.
  2. **Workload Controller**: Detects the update. Since the `RayCluster`'s worker group replica count is updated to 12, Kueue updates the pending workload `wl-C` with `spec.podSets.count = 12`.
  3. **Kueue Scheduler**: Evaluates `wl-C`. The demand is `12 - 7 = 5`. The available quota is 0, so the scheduler puts `wl-C` in the queue.

##### Step 3: Quota increases to 12, opportunistic scale up when capacity is freed
If the available quota in the ClusterQueue increases to 12 (or more) in the future:
* **Workloads**:
  * `wl-B` (Finished)
  * `wl-C` (Admitted):
    * `spec.podSets.count` = 12
    * `spec.podSets.minCount` = 8 (7 + 1)
    * `status.admission.count` = 12
* **Controller Actions**:
  1. In the next scheduler loop, the Kueue scheduler evaluates and admits `wl-C`.
  2. **ElasticJobUngater Controller**: Detects that `wl-C` is admitted with count 12 and removes the scheduling gate from the remaining 5 pods.

### RayJob/RayService/RayCluster controller

Only `RayJob`, `RayService`, and `RayCluster` integrations support the partial scale up feature (`batch/v1 Job` is not supported).

The `RayCluster.workerGroupSpec[i].replicas * numOfHosts` will be translated to `PodSet.Count`. For the initial workload, `spec.podSets[i].minCount` will be equal to `PodSet.Count` to disable partial admission. For workloads representing scale up, `spec.podSets[i].minCount` will be equal to the currently admitted pods count increased by 1 for worker groups that are scaling up.

### Test Plan

#### Unit Tests

- Verifying workload creation for elastic jobs with `minCount` set to `admitted + 1` during scale up.
- Verifying ungater controller behavior when workloads are partially admitted.

#### Integration tests

- `test/integration/singlecluster/controller/jobs/raycluster/raycluster_controller_test.go`:
  - `Should partially scale up the RayCluster when the full scale up is rejected`: verifies a complete integration flow where a RayCluster with partial scale-up enabled is admitted with reduced worker count according to order.

#### E2E tests

- Verifying end-to-end partial scale-up and opportunistic scale-up for elastic jobs under resource constraints.
