## v0.2.0

Changes since `v0.1.0`:

- Fixed bug in a BestEffortFIFO ClusterQueue where a workload might not be
  retried after a transient error.
- Bumped the API version from v1alpha1 to v1alpha2. v1alpha1 is no longer supported and Queue is now named LocalQueue.
