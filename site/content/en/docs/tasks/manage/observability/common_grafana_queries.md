---
title: "Common Grafana Queries"
date: 2026-02-03
weight: 2
description: >
  Common PromQL queries for monitoring Kueue in Grafana.
---

This page shows you how to use common PromQL queries to monitor Kueue metrics in Grafana.

The intended audience for this page are [batch administrators](/docs/tasks#batch-administrator).

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster is running.
- The kubectl command-line tool has communication with your cluster.
- [Kueue is installed](/docs/installation).
- [kube-prometheus](https://github.com/prometheus-operator/kube-prometheus) is installed.
- Kueue Prometheus metrics are enabled (see [Setup Prometheus](/docs/tasks/manage/observability/setup_prometheus)).

## Quota utilization

{{% alert title="Note" color="primary" %}}
The queries in this section require `metrics.enableClusterQueueResources: true`
in the Kueue configuration. See [Installation](/docs/installation/#install-a-custom-configured-released-version) for details.
{{% /alert %}}

To monitor the percentage of CPU quota being used in a ClusterQueue:

```promql
(sum by (cluster_queue) (kueue_cluster_queue_resource_usage{resource="cpu"}))
/
(sum by (cluster_queue) (kueue_cluster_queue_nominal_quota{resource="cpu"}))
* 100
```

To see utilization broken down by resource in a ClusterQueue:

```promql
(sum by (cluster_queue, resource) (kueue_cluster_queue_resource_usage))
/
(sum by (cluster_queue, resource) (kueue_cluster_queue_nominal_quota))
* 100
```

To see the average CPU quota utilization over the last week in a ClusterQueue:

```promql
avg_over_time(
  (
    (sum by (cluster_queue) (kueue_cluster_queue_resource_usage{resource="cpu"}))
    /
    (sum by (cluster_queue) (kueue_cluster_queue_nominal_quota{resource="cpu"}))
    * 100
  )[1w:1h]
)
```

To find the top 5 ClusterQueues by CPU utilization:

```promql
topk(5,
  (sum by (cluster_queue) (kueue_cluster_queue_resource_usage{resource="cpu"}))
  /
  (sum by (cluster_queue) (kueue_cluster_queue_nominal_quota{resource="cpu"}))
  * 100
)
```

## Pending workloads

To monitor the number of pending workloads per ClusterQueue:

```promql
sum by (cluster_queue) (kueue_pending_workloads{status="active"})
```

To see both active and inadmissible pending workloads per ClusterQueue:

```promql
sum by (cluster_queue, status) (kueue_pending_workloads)
```

## Admission wait time

To monitor how long workloads wait before admission, use histogram percentile queries.

For the 95th percentile (P95) admission wait time:

```promql
histogram_quantile(0.95,
  sum by (le, cluster_queue) (
    rate(kueue_admission_wait_time_seconds_bucket[5m])
  )
)
```

For the 50th percentile (median):

```promql
histogram_quantile(0.50,
  sum by (le, cluster_queue) (
    rate(kueue_admission_wait_time_seconds_bucket[5m])
  )
)
```

For the 99th percentile (P99):

```promql
histogram_quantile(0.99,
  sum by (le, cluster_queue) (
    rate(kueue_admission_wait_time_seconds_bucket[5m])
  )
)
```

## Post-admission startup latency

Monitor the time from Workload admission until Kueue removes one of its scheduling
gates from the associated Pods. Kueue records this latency for the
`kueue.x-k8s.io/admission` (Pod integration), the `kueue.x-k8s.io/topology`
 (Topology-Aware Scheduling, for any supported job framework), and the
`kueue.x-k8s.io/elastic-job` scheduling gates (elastic jobs). For example, the following query
shows the 95th percentile (P95) by ClusterQueue, gate name, and whether the Pods
belong to a group:

```promql
histogram_quantile(0.95,
  sum by (le, cluster_queue, name, is_group) (
    rate(kueue_pod_scheduling_gate_removal_seconds_bucket[5m])
  )
)
```

This latency isolates the controller handoff after admission. It does not include
the time that Kubernetes takes to schedule or start the Pods.

When `waitForPodsReady` is enabled, monitor the time from Workload admission until
the Workload reaches `PodsReady=True`. For example, the following query shows the
P95 by ClusterQueue and priority class:

```promql
histogram_quantile(0.95,
  sum by (le, cluster_queue, priority_class) (
    rate(kueue_admitted_until_ready_wait_time_seconds_bucket[5m])
  )
)
```

This latency includes Kubernetes scheduling, container startup, and Pod readiness.
It does not establish that the application has started useful work.

## Workload throughput

To monitor how many workloads are being admitted per hour:

```promql
sum by (cluster_queue) (
  increase(kueue_admitted_workloads_total[1h])
)
```

To monitor finished workloads per hour:

```promql
sum by (cluster_queue) (
  increase(kueue_finished_workloads_total[1h])
)
```

To see the admission rate over time (workloads per minute):

```promql
sum by (cluster_queue) (
  rate(kueue_admitted_workloads_total[5m])
) * 60
```

## Eviction rate

To monitor evictions per hour by reason:

```promql
sum by (cluster_queue, reason) (
  increase(kueue_evicted_workloads_total[1h])
)
```

See [Prometheus Metrics](/docs/reference/metrics) for the full list of `reason` label values.

## Eviction recovery latency

To monitor the time from Workload eviction until quota is released and the
Workload returns to Pending, use the workload eviction latency histogram. For
example, the following query shows the P95 by ClusterQueue and eviction reason:

```promql
histogram_quantile(0.95,
  sum by (le, cluster_queue, reason) (
    rate(kueue_workload_eviction_latency_seconds_bucket[5m])
  )
)
```

## ClusterQueue status

To see which ClusterQueues are active:

```promql
kueue_cluster_queue_status{status="active"} == 1
```

To see ClusterQueues that are not active (pending or terminating):

```promql
kueue_cluster_queue_status{status!="active"} == 1
```

## What's next

- See [Pending Workloads in Grafana](/docs/tasks/manage/monitor_pending_workloads/pending_workloads_in_grafana) for visibility dashboards using the on-demand API.
