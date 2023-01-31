---
title: "Prometheus Metrics"
linkTitle: "Prometheus Metrics"
date: 2017-01-05
description: >
  Prometheus metrics exported by Kueue
---

Kueue exposes [prometheus](https://prometheus.io) metrics to monitor the health
of the system and the status of [ClusterQueues](/docs/concepts/cluster_queue).

## Kueue health

Use the following metrics to monitor the health of the kueue controllers:

| Metric name | Type | Description | Labels |
| ----------- | ---- | ----------- | ------ |
| `kueue_admission_attempts_total` | Counter | The total number of attempts to [admit](/docs/concepts#admission) workloads. Each admission attempt might try to admit more than one workload. | `result`: possible values are `success` or `inadmissible` |
| `kueue_admission_attempt_duration_seconds` | Histogram | The latency of an admission attempt. | `result`: possible values are `success` or `inadmissible` |

## ClusterQueue status

Use the following metrics to monitor the status of your ClusterQueues:

| Metric name | Type | Description | Labels |
| ----------- | ---- | ----------- | ------ |
| `kueue_pending_workloads` | Gauge | The number of pending workloads. | `cluster_queue`: the name of the ClusterQueue<br> `status`: possible values are `active` or `inadmissible` |
| `kueue_admitted_workloads_total` | Counter | The total number of admitted workloads. | `cluster_queue`: the name of the ClusterQueue |
| `kueue_admission_wait_time_seconds` | Histogram | The time between a Workload was created until it was admitted. | `cluster_queue`: the name of the ClusterQueue |
| `kueue_admitted_active_workloads` | Gauge | The number of admitted Workloads that are active (unsuspended and not finished) | `cluster_queue`: the name of the ClusterQueue |
| `kueue_cluster_queue_status` | Gauge | Reports the status of the ClusterQueue | `cluster_queue`: The name of the ClusterQueue<br> `status`: Possible values are `pending`, `active` or `terminated`. For a ClusterQueue, the metric only reports a value of 1 for one of the statuses. |
