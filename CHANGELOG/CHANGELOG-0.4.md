## v0.4.2

Changes since `v0.4.1`:

### Bug or Regression

- Adjust resources (based on LimitRanges, PodOverhead and resource limits) on existing Workloads when a LocalQueue is created (#1197, @alculquicondor)
- Fix resuming of RayJob after preempted. (#1190, @kerthcet)

## v0.4.1

Changes since `v0.4.0`:

### Bug or Regression

- Fixed missing create verb for webhook (#1053, @stuton)
- Fixed scheduler to only allow one admission or preemption per cycle within a cohort that has ClusterQueues borrowing quota (#1029, @alculquicondor)
- Prevent workloads in ClusterQueue with StrictFIFO from blocking higher priority workloads in other ClusterQueues in the same cohort that require preemption (#1030, @alculquicondor)

## v0.4.0

Changes since `v0.3.0`:

### Features

- Add LimitRange based validation before admission. #613
- Move the workloads evicted due to pods ready timeout to the end of the queue. #689
- Manage number of Pods as part of ClusterQueue quota. #732
- Add a new `withinClusterQueue` preemption policy, `LowerOrNewerEqualPriority`. #710
- Consider preempted workloads admitted until the owner job becomes inactive. #692
- Add `flavorUsage` to `status` in `LocalQueue`.  #737

### Production Readiness


### Bug fixes

- Fix a bug that updates a queue name in workloads with an empty value when using framework jobs that use batch/job internally, such as MPIJob. #713 
