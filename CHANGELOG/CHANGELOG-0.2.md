## v0.2.0

Changes since `v0.1.0`:

- Fixed bug in a BestEffortFIFO ClusterQueue where a workload might not be
  retried after a transient error.
