## v0.2.0

Changes since `v0.1.0`:

### Features
- Bumped the API version from v1alpha1 to v1alpha2. v1alpha1 is no longer supported and Queue is now named LocalQueue.
- Add webhooks to validate and add defaults to all kueue APIs.
- Support [codependent resources](/docs/concepts/cluster_queue.md#codepedent-resources)
  by assigning the same flavor to codependent resources in a pod set.
- Support [pod overhead](https://kubernetes.io/docs/concepts/scheduling-eviction/pod-overhead/)
  in Workload pod sets.
- Default requests to limits if requests are not set in a Workload pod set, to
  match internal defaulting for k8s Pods.
- Added [prometheus metrics](/docs/reference/metrics.md) to monitor health of
  the system and the status of ClusterQueues.

### Bug fixes

- Prevent Workloads that don't match the ClusterQueue's namespaceSelector from
  blocking other Workloads in a StrictFIFO ClusterQueue.
- Fixed number of pending workloads in a BestEffortFIFO ClusterQueue.
- Fixed bug in a BestEffortFIFO ClusterQueue where a workload might not be
  retried after a transient error.
- Fixed requeuing an out-of-date workload when failed to admit it.
- Fixed bug in a BestEffortFIFO ClusterQueue where unadmissible workloads
  were not removed from the ClusterQueue when removing the corresponding Queue.
