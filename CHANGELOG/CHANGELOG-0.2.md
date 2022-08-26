## v0.2.1

Changes since `v0.1.0`:

### Features

- Upgrade the API version from v1alpha1 to v1alpha2. v1alpha1 is no longer supported.
  v1alpha2 includes the following changes:
  - Rename Queue to LocalQueue.
  - Remove ResourceFlavor.labels. Use ResourceFlavor.metadata.labels instead.
- Add webhooks to validate and to add defaults to all kueue APIs.
- Add internal cert manager to serve webhooks with TLS.
- Use finalizers to prevent ClusterQueues and ResourceFlavors in use from being
  deleted prematurely.
- Support [codependent resources](/docs/concepts/cluster_queue.md#codepedent-resources)
  by assigning the same flavor to codependent resources in a pod set.
- Support [pod overhead](https://kubernetes.io/docs/concepts/scheduling-eviction/pod-overhead/)
  in Workload pod sets.
- Set requests to limits if requests are not set in a Workload pod set,
  matching [internal defaulting for k8s Pods](https://kubernetes.io/docs/reference/kubernetes-api/workload-resources/pod-v1/#resources).
- Add [prometheus metrics](/docs/reference/metrics.md) to monitor health of
  the system and the status of ClusterQueues.
- Use Server Side Apply for Workload admission to reduce API conflicts.

### Bug fixes

- Fix bug that caused Workloads that don't match the ClusterQueue's
  namespaceSelector to block other Workloads in StrictFIFO ClusterQueues.
- Fix the number of pending workloads in BestEffortFIFO ClusterQueues status.
- Fix a bug in BestEffortFIFO ClusterQueues where a workload might not be
  retried after a transient error.
- Fix requeuing an out-of-date workload when failed to admit it.
- Fix a bug in BestEffortFIFO ClusterQueues where inadmissible workloads
  were not removed from the ClusterQueue when removing the corresponding Queue.
