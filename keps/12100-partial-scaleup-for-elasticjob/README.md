# KEP-12100: Partial Replica ScaleUp for ElasticJob

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
    - [ElasticJob ScaleUp Annotation](#elasticjob-scaleup-annotation)
  - [Scheduler / Flavorassignment](#scheduler--flavorassignment)
  - [Opportunistic scale up when capacity is freed](#opportunistic-scale-up-when-capacity-is-freed)
    - [WorkloadSlice Name](#workloadslice-name)
    - [StrictFIFO Constraint](#strictfifo-constraint)
    - [Example:  Two-Step Scale Up under Quota Constraints](#example--two-step-scale-up-under-quota-constraints)
      - [Step 1: Scale Up from 5 to 10 (Quota Constraint: 7), partial admission of scale up](#step-1-scale-up-from-5-to-10-quota-constraint-7-partial-admission-of-scale-up)
      - [Step 2: Scale Up to 12 (Quota Constraint: 7), scale up isn't admitted](#step-2-scale-up-to-12-quota-constraint-7-scale-up-isnt-admitted)
      - [Step 3: Quota increases to 12, opportunistic scale up when capacity is freed](#step-3-quota-increases-to-12-opportunistic-scale-up-when-capacity-is-freed)
      - [Step 4: Scale Down (e.g. from 12 to 8)](#step-4-scale-down-eg-from-12-to-8)
  - [RayJob/RayService/RayCluster controller](#rayjobrayserviceraycluster-controller)
  - [Partial ScaleUp for multiple PodSets](#partial-scaleup-for-multiple-podsets)
    - [Order-Based policy (<code>order-based</code>)](#order-based-policy-order-based)
      - [Example of RayJob with multiple PodSets](#example-of-rayjob-with-multiple-podsets)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [E2E tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Limitations](#limitations)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
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
- Respecting pod indexing when ungating pods. Specifically, this means that only single-host worker replicas are supported in RayCluster (NumOfHosts = 1)

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

For ElasticJobs, updating `job.spec.parallelism` or `rayClusterSpec.workerGroupSpecs[*].replicas` could cause race conditions between partial scale up and scaling up/down activity. 
To avoid this, the `job.spec.parallelism` or `rayClusterSpec.workerGroupSpecs[*].replicas` won't be updated in `RunWithPodSetsInfo` for elastic jobs. Instead, the workload controller will use the `workload.Status.Admission.PodSetAssignments[*].Count` value to calculate the number of pods from which Kueue should remove scheduling gates. 
The `Workload.Spec.PodSets[].MinCount` for an ElasticJob workload will equal to `min(admitted.count + 1, podset.count + 1)` of the previous workload and will represent the currently running pods + 1. The `podset.count` will represent currently running pods after scale down event, while `admitted.count` represents currently running pods after scale up event.
Also, a new Workload representing the full job will be created and added to the queue, to admit the remaining capacity once it becomes available (opportunistic scale up).

### Enablement

Partial ScaleUp for elastic jobs in Kueue is enabled through a combination of a Kubernetes feature gate and an opt-in annotation on individual Workload objects. At the cluster level, the ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp feature (disabled by default) must be enabled via the corresponding Kueue feature gate.

Once the feature gate is enabled, individual Job objects can opt into partial admission by including the `kueue.x-k8s.io/elastic-job-scale-up-strategy="partial"` annotation. If the annotation is not set, the default value is `"atomic"`.
When both conditions are met, Kueue treats the Workload as eligible for partial scale up. 

#### Features
```go
	// Enables partial scale up for elastic jobs.
	ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp featuregate.Feature = "ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp"
```

#### ElasticJob ScaleUp Annotation
```go
type ElasticJobScaleUpStrategyAnnotationValue string

const (
	// ElasticJobScaleUpAnnotationKey refers to the annotation key present on Jobs that support
	// partial scale up.
	// This annotation is alpha-level.
	ElasticJobScaleUpStrategyAnnotationKey = "kueue.x-k8s.io/elastic-job-scale-up-strategy"

	ElasticJobScaleUpStrategyAtomic  ElasticJobScaleUpStrategyAnnotationValue = "atomic"
	ElasticJobScaleUpStrategyPartial ElasticJobScaleUpStrategyAnnotationValue = "partial"
)
```

### Scheduler / Flavorassignment

The partial admission mechanism will be applied for the workload that represents scale up.

### Opportunistic scale up when capacity is freed

In order to schedule remaining pods after partial scale up, the workload controller will create a new workload representing the full job and add it to the queue. The scheduler will admit the new workload and replace the old workload via the workload slice mechanism as capacity becomes available.

#### WorkloadSlice Name

The newly created workload for opportunistic scale up should have a different name from the admitted workload. This will be done by adding an extra parameter "full-scaleup-probe" when calculating the hash suffix. The extra parameter will influence the hash value, thus resulting in a different WorkloadSlice name. At the moment, the hash suffix is limited to 5 characters and there is no plan to increase it. Since the extra parameter will change only the hash value, the length of WorkloadSlice name remains the same.

#### StrictFIFO Constraint

When a Job scale-up is partially admitted, Kueue creates a new Workload representing the remaining scale-up capacity. In a `StrictFIFO` queue, if another Job is submitted before this new Workload is created and enqueued, the newly submitted Job will take precedence in the queue. Consequently, the remaining scale-up request will not be admitted until all preceding jobs in the queue are processed.

This is a constraint of partial scale-up that users should be aware of when using `StrictFIFO` queues.

#### Example:  Two-Step Scale Up under Quota Constraints

Consider a scenario where:
1. The ClusterQueue has a total quota of **7** for the requested resource flavor.
2. The `RayCluster` is configured for both `ElasticJobsViaWorkloadSlices` and `ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp`.
3. The user performs a two-step scale up of the `RayCluster`: starting at **5** replicas, scaling up to **10**, and then to **12**.

Step 0: Job Creation (Initial Size: 5)
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
  4. **ElasticJobUngater Controller**: Detects that `wl-A` is admitted and removes the scheduling gate from the 5 pods.
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

##### Step 4: Scale Down (e.g. from 12 to 8)
* **RayCluster worker group replicas**: 8
* **Workloads**:
  * `wl-C` (Updated/Replaced):
    * `spec.podSets.count` = 8
    * `spec.podSets.minCount` = 8
    * `status.admission.count` = 12 (the admission value remains the same after ScaleDown)
* **Controller Actions**:
  1. **KubeRay Controller**: Decreases worker group replica count to 8 and deletes 4 running pods.
  2. **Workload Controller**: Detects the scale down and updates the admitted Workload `wl-C` to set `spec.podSets.count = 8`, `spec.podSets.minCount`.

### RayJob/RayService/RayCluster controller

Only `RayJob`, `RayService`, and `RayCluster` integrations support the partial scale up feature (`batch/v1 Job` is not supported).

The `RayCluster.workerGroupSpec[i].replicas * numOfHosts` will be translated to `PodSet.Count`. Only RayCluster WorkingGroups with minReplicas value will be considered for partial scale up. For those WorkingGroups the `spec.podSets[i].minCount` will be equal to `PodSet.Count` for the initial Workload in order to prevent partial admission. For workloads representing scale up, `spec.podSets[i].minCount` will be equal to the currently admitted pods count increased by 1 for worker groups that are scaling up.

Note, that PodsReady() for Ray jobs rely on RayCluster.Status.State value, so the partial scale up won't affect the PodsReady() value.

### Partial ScaleUp for multiple PodSets

There are multiple ways how to approach multiple podsets shrinking in case of insufficient quota. For simplicity reasons we'll start with the order-based one and will expand options if needed in future.

- **`order-based`**: Shrinks the counts of the PodSets sequentially starting from the last one (suits for the cases when the podsets are ordered by priority). The Workload PodSet order is usually the same as the order of the PodSets in the Job spec.

#### Order-Based policy (`order-based`)

Under the `order-based` policy, Kueue shrinks the PodSets starting from the last one in the list and moving towards the beginning as needed.
Specifically, if multiple PodSets have variable counts, Kueue iterates over them in the order they are defined in the Workload spec, starting from the last one. It decreases the count of the current PodSet down to its `minCount` until the workload fits the available quota. If shrinking the last PodSet to its `minCount` is still not enough to fit, Kueue keeps it at its `minCount` and moves to the second-to-last PodSet, decreasing its count down to its `minCount`, and so on.
As an optimization, we will introduce a second phase (similar to the preemption algorithm): when a workload finds a combination that fits the available quota, Kueue tries to gradually put the reduced counts back. In this phase, Kueue iterates over all PodSets from the first to the last one. For each PodSet that was reduced, Kueue tries to increase its count back to the original count. If that fits, Kueue keeps it. Otherwise, Kueue performs a binary search on the PodSet's count between the current count and the original count to find the maximum count that fits.

One example when order-based policy is used, is when a multi-podset Job has identical PodSets that have different node selectors tied to different node group capacity — for example, reservation/on-demand/spot. In this case, it is preferable to keep pods running on reservation nodes rather than on-demand/spot nodes.

##### Example of RayJob with multiple PodSets

```yaml
apiVersion: ray.io/v1
kind: RayJob
metadata:
  name: rayjob-multi-podset
  namespace: default
  labels:
    kueue.x-k8s.io/queue-name: user-queue
  annotations:
    kueue.x-k8s.io/elastic-job: "true"
    kueue.x-k8s.io/elastic-job-scale-up-strategy: partial
spec:
  rayClusterSpec:
    rayVersion: "2.58.0"
    enableInTreeAutoscaling: true
    headGroupSpec:
      rayStartParams: {}
      template:
        spec:
          containers:
          - name: ray-head
            image: rayproject/ray:2.58.0
            resources:
              requests:
                cpu: "1"
    workerGroupSpecs:
    - groupName: workers-reservation  # High-priority / critical group, defined first
      replicas: 2    # scaled up to 4
      minReplicas: 0
      maxReplicas: 20
      template:
        spec:
          nodeSelector:
            instance-type: reservation
          containers:
          - name: ray-worker
            image: rayproject/ray:2.58.0
            resources:
              requests:
                cpu: "1"
    - groupName: workers-spot    # Low-priority / spot group, defined last (shrunk first)
      replicas: 10   # scaled up to 20
      minReplicas: 0
      maxReplicas: 40
      template:
        spec:
          nodeSelector:
            instance-type: spot
          containers:
          - name: ray-worker
            image: rayproject/ray:2.58.0
            resources:
              requests:
                cpu: "1"
```

The RayJob will translated to the Workload with three PodSets:
- `ps0` (head pod): `count: 1`, no `minCount` (cannot be shrunk).
- `ps1` (workers-reservation): `count: 4`, `minCount: 2` (can be reduced by up to 2 pods).
- `ps2` (workers-spot): `count: 20`, `minCount: 10` (can be reduced by up to 10 pods).

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

### Test Plan

#### Unit Tests

- Verifying workload creation for elastic jobs with `minCount` set to minimum of `admitted.count` + 1 and `podset.count` + 1during scale up.
- Verifying ungater controller behavior when workloads are partially admitted.

#### Integration tests

- `test/integration/singlecluster/controller/jobs/raycluster/raycluster_controller_test.go`:
  - `Should partially scale up the RayCluster when the full scale up is rejected`: verifies a complete integration flow where a RayCluster with partial scale-up enabled is admitted with reduced worker count according to order.

#### E2E tests

- Verifying end-to-end partial scale-up and opportunistic scale-up for elastic jobs under resource constraints.

### Graduation Criteria

**Alpha (v0.20):**
- Feature gate `ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp` disabled by default.
- Add integration for RayJob, RayCluster, RayService
- Unit and integration tests.

**Beta:**
- Feature gate enabled by default.
- Address feedback from Alpha usage.
- Add integration for batch.Job

**GA:**
- Feature gate locked to true.
- Integration for other job types that implements ElasticJob is added.

## Implementation History

## Limitations
The feature was not evaluated on Multikueue.

## Drawbacks
- The feature defines the kueue behavior and the user should make sure partial scale up is compatible with the job controller.
- For Ray, gated pods waiting for capacity are recycled by the Ray autoscaler rather than waiting indefinitely. On Ray 2.47+ this is RAY_AUTOSCALER_RECONCILE_ALLOCATE_STATUS_TIMEOUT_S, one hour by default.
- see [StrictFIFO Constraint](#strictfifo-constraint)

## Alternatives
