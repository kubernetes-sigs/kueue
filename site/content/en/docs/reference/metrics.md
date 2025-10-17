---
title: "Prometheus Metrics"
linkTitle: "Prometheus Metrics"
date: 2022-02-14
description: >
  Prometheus metrics exported by Kueue
---
Kueue exposes [prometheus](https://prometheus.io) metrics to monitor the health
of the system and the status of [ClusterQueues](/docs/concepts/cluster_queue)
and [LocalQueues](/docs/concepts/local_queue).

<!-- NOTE: Sections below contain tables generated from code. Do not edit content between BEGIN/END GENERATED TABLE markers. Use `make generate-metrics-tables` to update. -->

## Kueue health

Use the following metrics to monitor the health of the kueue controllers:

<!-- BEGIN GENERATED TABLE: health -->
| Metric name | Type | Description | Labels |
| --- | --- | --- | --- |
| `kueue_admission_attempt_duration_seconds` | Histogram | The latency of an admission attempt.<br>The label 'result' can have the following values:<br>- 'success' means that at least one workload was admitted.,<br>- 'inadmissible' means that no workload was admitted. | `result`: possible values are `success` or `inadmissible` |
| `kueue_admission_attempts_total` | Counter | The total number of attempts to admit workloads.<br>Each admission attempt might try to admit more than one workload.<br>The label 'result' can have the following values:<br>- 'success' means that at least one workload was admitted.,<br>- 'inadmissible' means that no workload was admitted. | `result`: possible values are `success` or `inadmissible` |
<!-- END GENERATED TABLE: health -->

## ClusterQueue status

Use the following metrics to monitor the status of your ClusterQueues:

<!-- BEGIN GENERATED TABLE: clusterqueue -->
| Metric name | Type | Description | Labels |
| --- | --- | --- | --- |
| `kueue_admission_checks_wait_time_seconds` | Histogram | The time from when a workload got the quota reservation until admission, per 'cluster_queue' | `cluster_queue`: the name of the ClusterQueue<br> `priority_class`: the priority class name |
| `kueue_admission_cycle_preemption_skips` | Gauge | The number of Workloads in the ClusterQueue that got preemption candidates but had to be skipped because other ClusterQueues needed the same resources in the same cycle | `cluster_queue`: the name of the ClusterQueue |
| `kueue_admission_wait_time_seconds` | Histogram | The time between a workload was created or requeued until admission, per 'cluster_queue' | `cluster_queue`: the name of the ClusterQueue<br> `priority_class`: the priority class name |
| `kueue_admitted_active_workloads` | Gauge | The number of admitted Workloads that are active (unsuspended and not finished), per 'cluster_queue' | `cluster_queue`: the name of the ClusterQueue |
| `kueue_admitted_workloads_total` | Counter | The total number of admitted workloads per 'cluster_queue' | `cluster_queue`: the name of the ClusterQueue<br> `priority_class`: the priority class name |
| `kueue_build_info` | Gauge | Kueue build information. 1 labeled by git version, git commit, build date, go version, compiler, platform | `git_version`<br> `git_commit`<br> `build_date`<br> `go_version`<br> `compiler`<br> `platform` |
| `kueue_cluster_queue_status` | Gauge | Reports 'cluster_queue' with its 'status' (with possible values 'pending', 'active' or 'terminated').<br>For a ClusterQueue, the metric only reports a value of 1 for one of the statuses. | `cluster_queue`: the name of the ClusterQueue<br> `status`: status label (varies by metric) |
| `kueue_evicted_workloads_once_total` | Counter | The number of unique workload evictions per 'cluster_queue',<br>The label 'reason' can have the following values:<br>- "Preempted" means that the workload was evicted in order to free resources for a workload with a higher priority or reclamation of nominal quota.<br>- "PodsReadyTimeout" means that the eviction took place due to a PodsReady timeout.<br>- "AdmissionCheck" means that the workload was evicted because at least one admission check transitioned to False.<br>- "ClusterQueueStopped" means that the workload was evicted because the ClusterQueue is stopped.<br>- "LocalQueueStopped" means that the workload was evicted because the LocalQueue is stopped.<br>- "NodeFailures" means that the workload was evicted due to node failures when using TopologyAwareScheduling.<br>- "Deactivated" means that the workload was evicted because spec.active is set to false.<br>The label 'detailed_reason' can have the following values:<br>- "" means that the value in 'reason' label is the root cause for eviction.<br>- "WaitForStart" means that the pods have not been ready since admission, or the workload is not admitted.<br>- "WaitForRecovery" means that the Pods were ready since the workload admission, but some pod has failed.<br>- "AdmissionCheck" means that the workload was evicted by Kueue due to a rejected admission check.<br>- "MaximumExecutionTimeExceeded" means that the workload was evicted by Kueue due to maximum execution time exceeded.<br>- "RequeuingLimitExceeded" means that the workload was evicted by Kueue due to requeuing limit exceeded. | `cluster_queue`: the name of the ClusterQueue<br> `reason`: eviction or preemption reason<br> `detailed_reason`: finer-grained eviction cause<br> `priority_class`: the priority class name |
| `kueue_evicted_workloads_total` | Counter | The number of evicted workloads per 'cluster_queue',<br>The label 'reason' can have the following values:<br>- "Preempted" means that the workload was evicted in order to free resources for a workload with a higher priority or reclamation of nominal quota.<br>- "PodsReadyTimeout" means that the eviction took place due to a PodsReady timeout.<br>- "AdmissionCheck" means that the workload was evicted because at least one admission check transitioned to False.<br>- "ClusterQueueStopped" means that the workload was evicted because the ClusterQueue is stopped.<br>- "LocalQueueStopped" means that the workload was evicted because the LocalQueue is stopped.<br>- "NodeFailures" means that the workload was evicted due to node failures when using TopologyAwareScheduling.<br>- "Deactivated" means that the workload was evicted because spec.active is set to false.<br>The label 'underlying_cause' can have the following values:<br>- "" means that the value in 'reason' label is the root cause for eviction.<br>- "AdmissionCheck" means that the workload was evicted by Kueue due to a rejected admission check.<br>- "MaximumExecutionTimeExceeded" means that the workload was evicted by Kueue due to maximum execution time exceeded.<br>- "RequeuingLimitExceeded" means that the workload was evicted by Kueue due to requeuing limit exceeded. | `cluster_queue`: the name of the ClusterQueue<br> `reason`: eviction or preemption reason<br> `underlying_cause`: root cause for eviction<br> `priority_class`: the priority class name |
| `kueue_pending_workloads` | Gauge | The number of pending workloads, per 'cluster_queue' and 'status'.<br>'status' can have the following values:<br>- "active" means that the workloads are in the admission queue.<br>- "inadmissible" means there was a failed admission attempt for these workloads and they won't be retried until cluster conditions, which could make this workload admissible, change | `cluster_queue`: the name of the ClusterQueue<br> `status`: status label (varies by metric) |
| `kueue_pods_ready_to_evicted_time_seconds` | Histogram | The number of seconds between a workload's pods being ready and eviction workloads per 'cluster_queue',<br>The label 'reason' can have the following values:<br>- "Preempted" means that the workload was evicted in order to free resources for a workload with a higher priority or reclamation of nominal quota.<br>- "PodsReadyTimeout" means that the eviction took place due to a PodsReady timeout.<br>- "AdmissionCheck" means that the workload was evicted because at least one admission check transitioned to False.<br>- "ClusterQueueStopped" means that the workload was evicted because the ClusterQueue is stopped.<br>- "LocalQueueStopped" means that the workload was evicted because the LocalQueue is stopped.<br>- "NodeFailures" means that the workload was evicted due to node failures when using TopologyAwareScheduling.<br>- "Deactivated" means that the workload was evicted because spec.active is set to false.<br>The label 'underlying_cause' can have the following values:<br>- "" means that the value in 'reason' label is the root cause for eviction.<br>- "AdmissionCheck" means that the workload was evicted by Kueue due to a rejected admission check.<br>- "MaximumExecutionTimeExceeded" means that the workload was evicted by Kueue due to maximum execution time exceeded.<br>- "RequeuingLimitExceeded" means that the workload was evicted by Kueue due to requeuing limit exceeded. | `cluster_queue`: the name of the ClusterQueue<br> `reason`: eviction or preemption reason<br> `underlying_cause`: root cause for eviction |
| `kueue_preempted_workloads_total` | Counter | The number of preempted workloads per 'preempting_cluster_queue',<br>The label 'reason' can have the following values:<br>- "InClusterQueue" means that the workload was preempted by a workload in the same ClusterQueue.<br>- "InCohortReclamation" means that the workload was preempted by a workload in the same cohort due to reclamation of nominal quota.<br>- "InCohortFairSharing" means that the workload was preempted by a workload in the same cohort Fair Sharing.<br>- "InCohortReclaimWhileBorrowing" means that the workload was preempted by a workload in the same cohort due to reclamation of nominal quota while borrowing. | `preempting_cluster_queue`: the ClusterQueue executing preemption<br> `reason`: eviction or preemption reason |
| `kueue_quota_reserved_wait_time_seconds` | Histogram | The time between a workload was created or requeued until it got quota reservation, per 'cluster_queue' | `cluster_queue`: the name of the ClusterQueue<br> `priority_class`: the priority class name |
| `kueue_quota_reserved_workloads_total` | Counter | The total number of quota reserved workloads per 'cluster_queue' | `cluster_queue`: the name of the ClusterQueue<br> `priority_class`: the priority class name |
| `kueue_replaced_workload_slices_total` | Counter | The number of replaced workload slices per 'cluster_queue' | `cluster_queue`: the name of the ClusterQueue |
| `kueue_reserving_active_workloads` | Gauge | The number of Workloads that are reserving quota, per 'cluster_queue' | `cluster_queue`: the name of the ClusterQueue |
<!-- END GENERATED TABLE: clusterqueue -->

## LocalQueue Status (alpha)

The following metrics are available only if `LocalQueueMetrics` feature gate is enabled. Check the [Change the feature gates configuration](/docs/installation/#change-the-feature-gates-configuration) section of the [Installation](/docs/installation/) for details.

<!-- BEGIN GENERATED TABLE: localqueue -->
| Metric name | Type | Description | Labels |
| --- | --- | --- | --- |
| `kueue_local_queue_admission_checks_wait_time_seconds` | Histogram | The time from when a workload got the quota reservation until admission, per 'local_queue' | `name`: the name of the LocalQueue<br> `namespace`: the namespace of the LocalQueue<br> `priority_class`: the priority class name |
| `kueue_local_queue_admission_wait_time_seconds` | Histogram | The time between a workload was created or requeued until admission, per 'local_queue' | `name`: the name of the LocalQueue<br> `namespace`: the namespace of the LocalQueue<br> `priority_class`: the priority class name |
| `kueue_local_queue_admitted_active_workloads` | Gauge | The number of admitted Workloads that are active (unsuspended and not finished), per 'localQueue' | `name`: the name of the LocalQueue<br> `namespace`: the namespace of the LocalQueue |
| `kueue_local_queue_admitted_workloads_total` | Counter | The total number of admitted workloads per 'local_queue' | `name`: the name of the LocalQueue<br> `namespace`: the namespace of the LocalQueue<br> `priority_class`: the priority class name |
| `kueue_local_queue_evicted_workloads_total` | Counter | The number of evicted workloads per 'local_queue',<br>The label 'reason' can have the following values:<br>- "Preempted" means that the workload was evicted in order to free resources for a workload with a higher priority or reclamation of nominal quota.<br>- "PodsReadyTimeout" means that the eviction took place due to a PodsReady timeout.<br>- "AdmissionCheck" means that the workload was evicted because at least one admission check transitioned to False.<br>- "ClusterQueueStopped" means that the workload was evicted because the ClusterQueue is stopped.<br>- "LocalQueueStopped" means that the workload was evicted because the LocalQueue is stopped.<br>- "NodeFailures" means that the workload was evicted due to node failures when using TopologyAwareScheduling.<br>- "Deactivated" means that the workload was evicted because spec.active is set to false.<br>The label 'underlying_cause' can have the following values:<br>- "" means that the value in 'reason' label is the root cause for eviction.<br>- "AdmissionCheck" means that the workload was evicted by Kueue due to a rejected admission check.<br>- "MaximumExecutionTimeExceeded" means that the workload was evicted by Kueue due to maximum execution time exceeded.<br>- "RequeuingLimitExceeded" means that the workload was evicted by Kueue due to requeuing limit exceeded. | `name`: the name of the LocalQueue<br> `namespace`: the namespace of the LocalQueue<br> `reason`: eviction or preemption reason<br> `underlying_cause`: root cause for eviction<br> `priority_class`: the priority class name |
| `kueue_local_queue_pending_workloads` | Gauge | The number of pending workloads, per 'local_queue' and 'status'.<br>'status' can have the following values:<br>- "active" means that the workloads are in the admission queue.<br>- "inadmissible" means there was a failed admission attempt for these workloads and they won't be retried until cluster conditions, which could make this workload admissible, change | `name`: the name of the LocalQueue<br> `namespace`: the namespace of the LocalQueue<br> `status`: status label (varies by metric) |
| `kueue_local_queue_quota_reserved_wait_time_seconds` | Histogram | The time between a workload was created or requeued until it got quota reservation, per 'local_queue' | `name`: the name of the LocalQueue<br> `namespace`: the namespace of the LocalQueue<br> `priority_class`: the priority class name |
| `kueue_local_queue_quota_reserved_workloads_total` | Counter | The total number of quota reserved workloads per 'local_queue' | `name`: the name of the LocalQueue<br> `namespace`: the namespace of the LocalQueue<br> `priority_class`: the priority class name |
| `kueue_local_queue_reserving_active_workloads` | Gauge | The number of Workloads that are reserving quota, per 'localQueue' | `name`: the name of the LocalQueue<br> `namespace`: the namespace of the LocalQueue |
| `kueue_local_queue_resource_reservation` | Gauge | Reports the localQueue's total resource reservation within all the flavors | `name`: the name of the LocalQueue<br> `namespace`: the namespace of the LocalQueue<br> `flavor`: the resource flavor name<br> `resource`: the resource name |
| `kueue_local_queue_resource_usage` | Gauge | Reports the localQueue's total resource usage within all the flavors | `name`: the name of the LocalQueue<br> `namespace`: the namespace of the LocalQueue<br> `flavor`: the resource flavor name<br> `resource`: the resource name |
| `kueue_local_queue_status` | Gauge | Reports 'localQueue' with its 'active' status (with possible values 'True', 'False', or 'Unknown').<br>For a LocalQueue, the metric only reports a value of 1 for one of the statuses. | `name`: the name of the LocalQueue<br> `namespace`: the namespace of the LocalQueue<br> `active`: one of `True`, `False`, or `Unknown` |
<!-- END GENERATED TABLE: localqueue -->

## Cohort Status

<!-- BEGIN GENERATED TABLE: cohort -->
| Metric name | Type | Description | Labels |
| --- | --- | --- | --- |
| `kueue_cohort_weighted_share` | Gauge | Reports a value that representing the maximum of the ratios of usage above nominal<br>quota to the lendable resources in the Cohort, among all the resources provided by<br>the Cohort, and divided by the weight.<br>If zero, it means that the usage of the Cohort is below the nominal quota.<br>If the Cohort has a weight of zero and is borrowing, this will return 9223372036854775807,<br>the maximum possible share value. | `cohort`: the name of the Cohort |
<!-- END GENERATED TABLE: cohort -->

### Optional metrics

The following metrics are available only if `metrics.enableClusterQueueResources` is enabled in the [manager's configuration](/docs/installation/#install-a-custom-configured-released-version).

<!-- BEGIN GENERATED TABLE: optional_clusterqueue_resources -->
| Metric name | Type | Description | Labels |
| --- | --- | --- | --- |
| `kueue_cluster_queue_borrowing_limit` | Gauge | Reports the cluster_queue's resource borrowing limit within all the flavors | `cohort`: the name of the Cohort<br> `cluster_queue`: the name of the ClusterQueue<br> `flavor`: the resource flavor name<br> `resource`: the resource name |
| `kueue_cluster_queue_lending_limit` | Gauge | Reports the cluster_queue's resource lending limit within all the flavors | `cohort`: the name of the Cohort<br> `cluster_queue`: the name of the ClusterQueue<br> `flavor`: the resource flavor name<br> `resource`: the resource name |
| `kueue_cluster_queue_nominal_quota` | Gauge | Reports the cluster_queue's resource nominal quota within all the flavors | `cohort`: the name of the Cohort<br> `cluster_queue`: the name of the ClusterQueue<br> `flavor`: the resource flavor name<br> `resource`: the resource name |
| `kueue_cluster_queue_resource_reservation` | Gauge | Reports the cluster_queue's total resource reservation within all the flavors | `cohort`: the name of the Cohort<br> `cluster_queue`: the name of the ClusterQueue<br> `flavor`: the resource flavor name<br> `resource`: the resource name |
| `kueue_cluster_queue_resource_usage` | Gauge | Reports the cluster_queue's total resource usage within all the flavors | `cohort`: the name of the Cohort<br> `cluster_queue`: the name of the ClusterQueue<br> `flavor`: the resource flavor name<br> `resource`: the resource name |
| `kueue_cluster_queue_weighted_share` | Gauge | Reports a value that representing the maximum of the ratios of usage above nominal<br>quota to the lendable resources in the cohort, among all the resources provided by<br>the ClusterQueue, and divided by the weight.<br>If zero, it means that the usage of the ClusterQueue is below the nominal quota.<br>If the ClusterQueue has a weight of zero and is borrowing, this will return 9223372036854775807,<br>the maximum possible share value. | `cluster_queue`: the name of the ClusterQueue |
<!-- END GENERATED TABLE: optional_clusterqueue_resources -->


The following metrics are available only if `waitForPodsReady` is enabled in the [manager's configuration](/docs/installation/#install-a-custom-configured-released-version).
For more details [see](/docs/tasks/manage/setup_wait_for_pods_ready).

<!-- BEGIN GENERATED TABLE: optional_wait_for_pods_ready -->
| Metric name | Type | Description | Labels |
| --- | --- | --- | --- |
| `kueue_admitted_until_ready_wait_time_seconds` | Histogram | The time between a workload was admitted until ready, per 'cluster_queue' | `cluster_queue`: the name of the ClusterQueue<br> `priority_class`: the priority class name |
| `kueue_local_queue_admitted_until_ready_wait_time_seconds` | Histogram | The time between a workload was admitted until ready, per 'local_queue' | `name`: the name of the LocalQueue<br> `namespace`: the namespace of the LocalQueue<br> `priority_class`: the priority class name |
| `kueue_local_queue_ready_wait_time_seconds` | Histogram | The time between a workload was created or requeued until ready, per 'local_queue' | `name`: the name of the LocalQueue<br> `namespace`: the namespace of the LocalQueue<br> `priority_class`: the priority class name |
| `kueue_ready_wait_time_seconds` | Histogram | The time between a workload was created or requeued until ready, per 'cluster_queue' | `cluster_queue`: the name of the ClusterQueue<br> `priority_class`: the priority class name |
<!-- END GENERATED TABLE: optional_wait_for_pods_ready -->