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
  - [Baseline and target semantics](#baseline-and-target-semantics)
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
  - [Probe lifecycle](#probe-lifecycle)
  - [Eviction and readmission behavior](#eviction-and-readmission-behavior)
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

Also, a new Workload will be created and added to the queue to advance the scale up step by step, increasing the PodSet counts as quota becomes available (opportunistic scale up); see [Probe lifecycle](#probe-lifecycle).

### Baseline and target semantics

Every Workload is defined by two per-PodSet count vectors, read for every PodSet `i`:

- **Baseline** (`baseline[i]`): `spec.podSets[i].minCount`.
- **Target** (`target[i]`): the requested count in the Workload's spec, `spec.podSets[i].count`.

For a scale-up replacement Workload (a workload-slice replacement, annotated via `kueue.x-k8s.io/workload-slice-replacement-for`), the workload controller sets `minCount[i]` to the previously granted count of the replaced Workload for every PodSet that is growing, read from the replaced Workload's `status.admission.podSetAssignments[i].count`. The reducer itself always just reads `baseline[i] = spec.podSets[i].minCount`.

The reducer returns an assignment (`granted[i]`) satisfying, for every PodSet `i`:

```
baseline[i] <= granted[i] <= target[i]
```

For a scale-up replacement Workload, the assignment must additionally make progress in **at least one** PodSet:

```
exists i: granted[i] > baseline[i]
```

The unchanged baseline (`granted[i] == baseline[i]` for every `i`) is not a successful scale-up assignment for a replacement Workload — it keeps the previously admitted Workload slice running unchanged. This requirement doesn't apply to an ordinary (non-replacement) Workload, which has no prior admission to progress from.

Progress is evaluated globally across the Workload, not independently per PodSet: at least one PodSet must grow past its baseline, but not every PodSet needs to.

```
required:     exists i: granted[i] > baseline[i]
not required: forall i: granted[i] > baseline[i]
```

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

The partial admission mechanism will be applied for the workload that represents scale up, using the reducer described in [Order-Based policy](#order-based-policy-order-based).

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
    * `spec.podSets.count` (target) = 10
    * `spec.podSets.minCount` (baseline, set from `wl-A`'s granted count) = 5
    * `status.admission.count` (granted) = 7
  * `wl-C` (Pending, since there is no capacity above baseline for 10 pods)
    * `spec.podSets.count` (target) = 10
    * `spec.podSets.minCount` (baseline, set from `wl-B`'s granted count) = 7
* **Controller Actions**:
  1. **KubeRay Controller**: Increase worker group replica count and creates 5 new Pods (total 10 pods: 5 running, 5 gated). The new pods are created with the `kueue.x-k8s.io/elastic-job` scheduling gate.
  2. **Workload Controller**: Observes the scale-up and creates a new Workload slice `wl-B` with `spec.podSets.count = 10` (target) and `spec.podSets.minCount = 5` (baseline, taken from `wl-A`'s granted count). It is annotated as a replacement for `wl-A` via `kueue.x-k8s.io/workload-slice-replacement-for`.
  3. **Kueue Scheduler**: Evaluates `wl-B`. Its baseline (5) and target (10) bound the search. The available quota is only 2, so the reducer searches for the largest count in `[5, 10]` that fits, and admits `wl-B` with `granted = 5 + 2 = 7` (`status.admission.count = 7`), reserving 2 more units of quota (total 7). This satisfies `baseline (5) <= granted (7) <= target (10)` and makes progress since `granted > baseline`.
  4. **WorkloadSlice Controller**: Creates another WorkloadSlice `wl-C` that represents the current state of the job with `.spec.podSets[0].count` = 10 (target) and `.spec.podSets[0].minCount` = 7 (baseline, taken from `wl-B`'s granted count). This workload is added to the queue to be evaluated when capacity becomes available, and it currently stays `Pending`. `wl-A` is marked as finished.
  5. **ElasticJobUngater Controller**: Detects that `wl-B` is admitted with count 7. It removes the scheduling gate from 2 of the new pods (bringing running pods to 7). The other 3 new pods remain gated.
* **Quota usage**: 7/7 (0 available).

##### Step 2: Scale Up to 12 (Quota Constraint: 7), scale up isn't admitted
* **RayCluster worker group replicas**: 12
* **Workloads**:
  * `wl-B` (Admitted)
  * `wl-C` (Updated, keep pending):
    * `spec.podSets.count` (target) = 12
    * `spec.podSets.minCount` (baseline) = 7
* **Controller Actions**:
  1. **KubeRay Controller**: Increase worker group replica count and creates 2 more Pods (total 12 pods: 7 running, 5 gated). The new pods are created with the `kueue.x-k8s.io/elastic-job` scheduling gate.
  2. **Workload Controller**: Detects the update. Since the `RayCluster`'s worker group replica count is updated to 12, Kueue updates the pending workload `wl-C` with `spec.podSets.count = 12` (target).
  3. **Kueue Scheduler**: Evaluates `wl-C`. The baseline is 7 and the target is 12, so the reducible delta is `12 - 7 = 5`. The available quota is 0, so no assignment above the baseline fits. `wl-C` remains pending and `wl-B` keeps running unchanged.

##### Step 3: Quota increases to 12, opportunistic scale up when capacity is freed
If the available quota in the ClusterQueue increases to 12 (or more) in the future:
* **Workloads**:
  * `wl-B` (Finished)
  * `wl-C` (Admitted):
    * `spec.podSets.count` (target) = 12
    * `spec.podSets.minCount` (baseline) = 7
    * `status.admission.count` (granted) = 12
* **Controller Actions**:
  1. In the next scheduler loop, the Kueue scheduler re-evaluates `wl-C`. The baseline (7) and target (12) are unchanged, and the full delta of 5 now fits, so the scheduler admits `wl-C` with `granted = 12`.
  2. **ElasticJobUngater Controller**: Detects that `wl-C` is admitted with count 12 and removes the scheduling gate from the remaining 5 pods.
  3. Since `granted == target` for `wl-C`, no further scale-up probe is created — probing stops once granted counts equal target counts.

##### Step 4: Scale Down (e.g. from 12 to 8)
* **RayCluster worker group replicas**: 8
* **Workloads**:
  * `wl-C` (Updated/Replaced):
    * `spec.podSets.count` = 8
    * `status.admission.count` = 12 (the admission value remains the same after ScaleDown)
* **Controller Actions**:
  1. **KubeRay Controller**: Decreases worker group replica count to 8 and deletes 4 running pods.
  2. **Workload Controller**: Detects the scale down and updates the admitted Workload `wl-C` to set `spec.podSets.count = 8`.

### Probe lifecycle

This section is normative for how a scale-up replacement Workload ("probe") moves from creation to full admission.

1. When the Job's desired replica counts increase, the controller creates a probe Workload targeting the new desired counts (`target`), annotated as a replacement for the currently admitted Workload slice (see [Baseline and target semantics](#baseline-and-target-semantics)).
2. If no assignment above the baseline fits the available quota, the probe remains `Pending` and the previous slice keeps running unchanged; it is re-evaluated on every scheduling cycle like any other pending Workload.
3. If the probe is partially admitted, its granted counts become the baseline for the next probe (see [Opportunistic scale up when capacity is freed](#opportunistic-scale-up-when-capacity-is-freed)).
4. Probing stops once granted counts equal target counts for every PodSet.
5. The target must remain stable while a probe is pending: the job controller must not narrow the desired counts it reports to Kueue to match the currently running count, or a later quota increase would have nothing left to scale into.

### Eviction and readmission behavior

If the Workload slice a pending probe replaces is evicted (e.g. ClusterQueue drain, or preemption), the probe's `baseline` — set from that slice's granted count — no longer corresponds to anything achievable: the slice it was relative to is gone. Left alone, the probe would stay stuck pending indefinitely, even once quota that previously supported the job reappears.

To recover, Kueue finishes the stranded probe together with its evicted predecessor, so the job starts its next reconcile from zero Workloads instead of being left with an orphaned probe and a stale baseline. The Workload slice created afterward seeds its `baseline` from the job's last admitted count on record, so the job can be readmitted at the size it last ran at and resume scale-up toward `target` from there.

### RayJob/RayService/RayCluster controller

Only `RayJob`, `RayService`, and `RayCluster` integrations support the partial scale up feature (`batch/v1 Job` is not supported).

The `RayCluster.workerGroupSpec[i].replicas * numOfHosts` will be translated to `PodSet.Count`. Only RayCluster WorkingGroups with minReplicas value will be considered for partial scale up. For those WorkingGroups the `spec.podSets[i].minCount` will be equal to `PodSet.Count` for the initial Workload in order to prevent partial admission for initial creation (see [Non-Goals](#non-goals)).

For workloads representing scale up, `spec.podSets[i].minCount` is set to the previously granted count of the replaced Workload slice for every PodSet that is growing. An assignment must additionally make progress in at least one PodSet — this is not required for ordinary partial admission of a fresh Workload.

Note, that PodsReady() for Ray jobs rely on RayCluster.Status.State value, so the partial scale up won't affect the PodsReady() value.

### Partial ScaleUp for multiple PodSets

There are multiple ways how to approach multiple podsets shrinking in case of insufficient quota. For simplicity reasons we'll start with the order-based one and will expand options if needed in future.

- **`order-based`**: Shrinks the counts of the PodSets sequentially starting from the last one (suits for the cases when the podsets are ordered by priority). The Workload PodSet order is usually the same as the order of the PodSets in the Job spec.

#### Order-Based policy (`order-based`)

Under the `order-based` policy, the reducer works as follows, given each PodSet `i`'s `target[i]` (`spec.podSets[i].count`) and `baseline[i]` (`spec.podSets[i].minCount`):

1. **Reduction phase.** The reduction is expressed as a single budget: the total number of replicas to give up, spread across the PodSets from the *last* one defined in the Workload spec towards the first, each giving up as much as it can (down to its `baseline[i]`) before the next one is touched. Kueue binary-searches that budget for the smallest one whose resulting counts fit the available quota, which requires every PodSet's count to be monotonically non-increasing as the budget grows. This suits Jobs whose PodSets are ordered by priority (the Workload PodSet order usually matches the PodSet order in the Job spec): later, lower-priority PodSets absorb the reduction first, so earlier, higher-priority PodSets stay closer to their target for as long as possible.
2. **Failure case.** If every PodSet is reduced all the way to its `baseline[i]` and the total still doesn't fit, the search fails and the Workload is not admitted.
3. **Giveback phase.** Once a fitting combination is found, Kueue tries to restore capacity, similar to the preemption algorithm: iterating over all PodSets from first to last, for each one that was reduced, Kueue first tries to restore it fully to its `target[i]`; if that doesn't fit, it binary-searches between the reduced count and `target[i]` for the largest count that still fits. This lets PodSets pinned to independently constrained ResourceFlavors each get back as much capacity as they individually have room for, even if another PodSet's ResourceFlavor is the limiting constraint.
4. **Scale-up progress check.** For a scale-up replacement Workload (annotated via `kueue.x-k8s.io/workload-slice-replacement-for`, with `baseline[i]` set by the workload controller to the replaced Workload's granted count for every growing PodSet — see [Baseline and target semantics](#baseline-and-target-semantics)), the assignment produced by step 3 must satisfy `exists i: granted[i] > baseline[i]`. One that lands on `baseline[i]` for every PodSet makes no scale-up progress and is discarded, leaving the replacement Workload pending rather than admitted as a no-op (see [Probe lifecycle](#probe-lifecycle)).

   The check applies to the assignment step 3 finally produces, not to the candidates step 1 considers. Barring the all-baseline vector from the reduction search would lose valid scale-ups: where PodSets are pinned to independently constrained ResourceFlavors, the ordered shrink may only find a fit once every PodSet sits at its baseline, and the giveback phase then grows back the PodSets whose own flavor was never the constraint. Scenario D below is that case — with the all-baseline vector barred from step 1 the search would find nothing for step 3 to give back from.

   The check also applies only while the replaced Workload still holds its quota. Once that Workload is evicted, its granted counts are no longer something to grow on top of — they are the size the job was last running at, and the replacement must be admissible at exactly them so the job can recover (see [Eviction and readmission behavior](#eviction-and-readmission-behavior)).

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
      replicas: 4    # previously 2 (baseline); requesting to scale up to 4 (target)
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

The RayJob translates to a Workload with three PodSets. `ps1` and `ps2` are growing as part of a scale-up replacement Workload, so their `minCount` (`baseline`) is set to the previously granted count of the replaced Workload slice, rather than a fixed floor:
- `ps0` (head pod): `count: 1` (target), no `minCount` set (cannot be shrunk).
- `ps1` (workers-reservation): `count: 4` (target), `minCount: 2` (baseline; can be reduced by up to 2 pods).
- `ps2` (workers-spot): `count: 20` (target), `minCount: 10` (baseline; can be reduced by up to 10 pods).

Total requested (target) pods: `1 + 4 + 20 = 25` pods. Total baseline pods: `1 + 2 + 10 = 13` pods.

- **Scenario A: Available quota is 19 pods** (requires a reduction of 6 pods).
  1. Kueue targets the lowest priority PodSet, `ps2`, and decreases its count by 6 (from 20 to 14).
  2. The resulting counts are: `ps0: 1`, `ps1: 4`, `ps2: 14` (total 19 pods, fits the quota).
  3. Admitted counts: `ps0: 1`, `ps1: 4`, `ps2: 14`.

- **Scenario B: Available quota is 13 pods** (requires a reduction of 12 pods).
  1. Kueue targets the lowest priority PodSet, `ps2`, and decreases its count to its minimum: `10` (reduction of 10 pods). The current total count is now `1 + 4 + 10 = 15`.
  2. Since it still does not fit the quota of 13, Kueue keeps `ps2` at `10` and moves to the next lowest priority PodSet, `ps1`.
  3. Kueue decreases `ps1` by the remaining 2 pods (from 4 to 2). The resulting total count is `1 + 2 + 10 = 13` pods.
  4. `ps1` and `ps2` have landed exactly on their `minCount` (`baseline`) — the previously granted counts. The giveback phase cannot grow either of them against the shared quota of 13, so the assignment makes no scale-up progress and is discarded. The probe remains `Pending`; the previously admitted Workload slice — already running at `ps0: 1, ps1: 2, ps2: 10` — is left unchanged rather than being replaced by a new, redundant admission.

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
  7. `ps1` ends on its baseline and only `ps2` grew, which satisfies the scale-up progress check — progress is required across the Workload, not from every PodSet. Note that the only combination the first phase found was the all-baseline `ps0: 1`, `ps1: 2`, `ps2: 10`; it is the giveback phase that turns it into a real scale-up, which is why the check is applied after that phase rather than to the candidates of the first one.

The accepted number of pods in each PodSet is recorded in `workload.Status.Admission.PodSetAssignments[*].Count`.

### Test Plan

#### Unit Tests

- Verifying the workload controller sets a scale-up probe's `spec.podSets[i].minCount` (`baseline`) from the replaced Workload's granted `status.admission.podSetAssignments[*].count`.
- Verifying the reducer only returns assignments satisfying `baseline[i] <= granted[i] <= target[i]` for every PodSet `i`.
- Verifying the reducer excludes the fully-reduced baseline as a successful admission for a scale-up replacement Workload.
- Verifying one PodSet growing while another PodSet stays at its baseline is a valid, independently admissible assignment.
- Verifying a higher-priority (earlier-ordered) PodSet is preferred to grow first when capacity only allows one PodSet to grow.
- Verifying that when no assignment above the baseline fits available quota, the probe remains pending and the existing Workload slice is left running unchanged.
- Verifying that an all-baseline fit which the giveback phase grows into real progress is admitted, since the progress check is applied after that phase.
- Verifying that the progress check does not apply to a Workload with no admitted predecessor, so classic partial admission and post-eviction readmission can both land on `minCount`.
- Verifying ungater controller behavior when workloads are partially admitted.

#### Integration tests

- `test/integration/singlecluster/controller/jobs/raycluster/raycluster_controller_partial_scaleup_test.go`: the flow through Steps 0-3 of the worked example above — successive partial admissions, where each admitted probe's granted counts become the baseline for the next; a probe left pending while quota is exhausted; and the opportunistic admission once quota is raised. Also that spare capacity for two pods goes wholly to the earlier of two competing worker groups rather than one pod to each, and that partial scale-up and preemption interact correctly.
- `test/integration/singlecluster/controller/jobs/rayservice/rayservice_controller_partial_scaleup_test.go`: worker groups pinned to independently constrained ResourceFlavors — the giveback phase restoring a group that was drained on another group's behalf (Scenario D above), a group whose own flavor is exhausted not blocking a sibling that can still grow, and no admission at all when no group has room above its baseline.
- `test/integration/singlecluster/controller/jobs/rayjob/rayjob_controller_partial_scaleup_test.go`: the same flow for the RayJob integration, whose workload slice naming differs.

Deferred to the eviction handling in [#15417](https://github.com/kubernetes-sigs/kueue/pull/15417), since they depend on a stranded probe being finished together with its predecessor and the next slice reseeding its baseline: readmitting a previously runnable assignment without requiring scale-up progress, resuming probing from that restored baseline, and preserving the scale-up target while a probe is pending (see [Probe lifecycle](#probe-lifecycle)).

#### E2E tests

- Verifying end-to-end partial scale-up and opportunistic scale-up for elastic jobs under resource constraints.
- Verifying end-to-end eviction and readmission of a partially scaled-up elastic job, followed by resumed scale-up once capacity returns.

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
