## v0.1.1

Changes since `v0.1.0`:

- Fixed number of pending workloads in a BestEffortFIFO ClusterQueue.
- Fixed bug in a BestEffortFIFO ClusterQueue where a workload might not be
  retried after a transient error.
- Fixed requeuing an out-of-date workload when failed to admit it.
- Fixed bug in a BestEffortFIFO ClusterQueue where unadmissible workloads
  were not removed from the ClusterQueue when removing the corresponding Queue.
