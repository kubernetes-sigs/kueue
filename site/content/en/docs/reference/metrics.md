---
title: "Prometheus Metrics"
linkTitle: "Prometheus Metrics"
date: 2022-02-14
description: >
  Prometheus metrics exported by Kueue
---

Kueue exposes [prometheus](https://prometheus.io) metrics to monitor the health
of the system and the status of [ClusterQueues](/docs/concepts/cluster_queue).

## Kueue health

Use the following metrics to monitor the health of the kueue controllers:

| Metric name                                | Type      | Description                                                                                                                                    | Labels                                                    |
|--------------------------------------------|-----------|------------------------------------------------------------------------------------------------------------------------------------------------|-----------------------------------------------------------|
| `kueue_admission_attempts_total`           | Counter   | The total number of attempts to [admit](/docs/concepts#admission) workloads. Each admission attempt might try to admit more than one workload. | `result`: possible values are `success` or `inadmissible` |
| `kueue_admission_attempt_duration_seconds` | Histogram | The latency of an admission attempt.                                                                                                           | `result`: possible values are `success` or `inadmissible` |

## ClusterQueue status

Use the following metrics to monitor the status of your ClusterQueues:

| Metric name                                | Type      | Description                                                                         | Labels                                                                                                                                                                                                 |
|--------------------------------------------|-----------|-------------------------------------------------------------------------------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `kueue_pending_workloads`                  | Gauge     | The number of pending workloads.                                                    | `cluster_queue`: the name of the ClusterQueue<br> `status`: possible values are `active` or `inadmissible`                                                                                             |
| `kueue_quota_reserved_workloads_total`     | Counter   | The total number of quota reserved workloads.                                       | `cluster_queue`: the name of the ClusterQueue                                                                                                                                                          |
| `kueue_quota_reserved_wait_time_seconds`   | Histogram | The time between a workload was created or requeued until it got quota reservation. | `cluster_queue`: the name of the ClusterQueue                                                                                                                                                          |
| `kueue_admitted_workloads_total`           | Counter   | The total number of admitted workloads.                                             | `cluster_queue`: the name of the ClusterQueue                                                                                                                                                          |
| `kueue_evicted_workloads_total`            | Counter   | The total number of evicted workloads.                                              | `cluster_queue`: the name of the ClusterQueue<br> `reason`: Possible values are `Preempted`, `PodsReadyTimeout`, `AdmissionCheck`, `ClusterQueueStopped` or `Deactivated`                              |
| `kueue_admission_wait_time_seconds`        | Histogram | The time between a workload was created or requeued until admission.                | `cluster_queue`: the name of the ClusterQueue                                                                                                                                                          |
| `kueue_admission_checks_wait_time_seconds` | Histogram | The time from when a workload got the quota reservation until admission.            | `cluster_queue`: the name of the ClusterQueue                                                                                                                                                          |
| `kueue_admitted_active_workloads`          | Gauge     | The number of admitted Workloads that are active (unsuspended and not finished)     | `cluster_queue`: the name of the ClusterQueue                                                                                                                                                          |
| `kueue_cluster_queue_status`               | Gauge     | Reports the status of the ClusterQueue                                              | `cluster_queue`: The name of the ClusterQueue<br> `status`: Possible values are `pending`, `active` or `terminated`. For a ClusterQueue, the metric only reports a value of 1 for one of the statuses. |

### Optional metrics

The following metrics are available only if `metrics.enableClusterQueueResources` is enabled in the [manager's configuration](/docs/installation/#install-a-custom-configured-released-version).

| Metric name                           | Type   | Description                                                                                                                                                                             | Labels                                                                                                                                                              |
|---------------------------------------|--------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `kueue_cluster_queue_resource_usage`  | Gauge  | Reports the ClusterQueue's total resource usage                                                                                                                                         | `cohort`: The cohort in which the queue belongs<br> `cluster_queue`: The name of the ClusterQueue<br> `flavor`: referenced flavor<br> `resource`: The resource name |
| `kueue_cluster_queue_nominal_quota`   | Gauge  | Reports the ClusterQueue's resource quota                                                                                                                                               | `cohort`: The cohort in which the queue belongs<br> `cluster_queue`: The name of the ClusterQueue<br> `flavor`: referenced flavor<br> `resource`: The resource name |
| `kueue_cluster_queue_borrowing_limit` | Gauge  | Reports the ClusterQueue's resource borrowing limit                                                                                                                                     | `cohort`: The cohort in which the queue belongs<br> `cluster_queue`: The name of the ClusterQueue<br> `flavor`: referenced flavor<br> `resource`: The resource name |
| `kueue_cluster_queue_weighted_share`  | Gauge  | Reports a value that representing the maximum of the ratios of usage above nominal quota to the lendable resources in the cohort, among all the resources provided by the ClusterQueue. | `cluster_queue`: The name of the ClusterQueue                                                                                                                       |
