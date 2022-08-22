# Metrics

Kueue exposes [prometheus](https://prometheus.io) metrics to monitor the health
of the system and the status of [ClusterQueues](/docs/concepts/cluster_queue.md).

## Kueue health

Use the following metrics to monitor the health of the kueue controllers:

- `kueue_admission_attempts_total` (Counter): Total number of attempts to [admit](/docs/concepts/README.md#admission)
  one or more workloads, broken down by `result` (`success` or `inadmissible`).
  Note that each admission attempt may try to admit more than one workload.
- `kueue_admission_attempt_duration_seconds` (Histogram): Latency of an
  admission attempt, broken down by `result` (`success` or `inadmissible`).

## ClusterQueue status

Use the following metrics to monitor the status of your ClusterQueues:

- `kueue_pending_workloads` (Gauge): The number of pending workloads, per
  `cluster_queue` and `status` (`active` or `inadmissible`).
- `kueue_admitted_workloads_total` (Counter): The total number of admitted
  workloads per `cluster_queue`.
- `kueue_admission_wait_time_seconds` (Histogram): The wait time since a
  Workload was created until it was admitted, per `cluster_queue`.
- `kueue_admitted_active_workloads` (Gauge): The number of admitted Workloads
  that are active (unsuspended and not finished), per `cluster_queue`.
- `kueue_cluster_queue_status` (Gauge): Reports `cluster_queue` with `status`
  (`pending`, `active` or `terminated`). Only one of the statuses will have a
  value of 1.
