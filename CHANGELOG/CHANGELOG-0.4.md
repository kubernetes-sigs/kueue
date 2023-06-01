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
