---
title: "常用 Grafana 查询"
date: 2026-02-03
weight: 2
description: >
  Grafana 中用于监控 Kueue 的常用 PromQL 查询。
---

此页面向你展示如何使用常见的 PromQL 查询在 Grafana 中监控 Kueue 指标。

此页面的目标读者为[批处理管理员](/docs/tasks#batch-administrator)。

## 开始之前

确保满足以下条件：

- Kubernetes 集群正在运行。
- kubectl 命令行工具能够与你的集群通信。
- [Kueue 已安装](/docs/installation)。
- [kube-prometheus](https://github.com/prometheus-operator/kube-prometheus) 已安装。
- Kueue Prometheus 指标已启用（参见[设置 Prometheus](/docs/tasks/manage/observability/setup_prometheus)）。

## 配额利用率

{{% alert title="注意" color="primary" %}}
本部分的查询需要在 Kueue 配置中设置 `metrics.enableClusterQueueResources: true`。
详情参见[安装](/docs/installation/#install-a-custom-configured-released-version)。
{{% /alert %}}

要监控 ClusterQueue 中正在使用的 CPU 配额百分比：

```promql
(sum by (cluster_queue) (kueue_cluster_queue_resource_usage{resource="cpu"}))
/
(sum by (cluster_queue) (kueue_cluster_queue_nominal_quota{resource="cpu"}))
* 100
```

查看 ClusterQueue 中按资源划分的利用率：

```promql
(sum by (cluster_queue, resource) (kueue_cluster_queue_resource_usage))
/
(sum by (cluster_queue, resource) (kueue_cluster_queue_nominal_quota))
* 100
```

查看 ClusterQueue 中过去一周的平均 CPU 配额利用率：

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

找出 CPU 利用率排名前 5 的集群队列：

```promql
topk(5,
  (sum by (cluster_queue) (kueue_cluster_queue_resource_usage{resource="cpu"}))
  /
  (sum by (cluster_queue) (kueue_cluster_queue_nominal_quota{resource="cpu"}))
  * 100
)
```

## 待处理的工作负载

要监控每个 ClusterQueue 的待处理工作负载数量：

```promql
sum by (cluster_queue) (kueue_pending_workloads{status="active"})
```

要查看每个 ClusterQueue 的活动和不可受理的待处理工作负载：

```promql
sum by (cluster_queue, status) (kueue_pending_workloads)
```

## 准入等待时间

要监控工作负载在准入前的等待时间，请使用直方图百分位查询。

对于第 95 百分位（P95）的准入等待时间：

```promql
histogram_quantile(0.95,
  sum by (le, cluster_queue) (
    rate(kueue_admission_wait_time_seconds_bucket[5m])
  )
)
```

第 50 百分位数（中位数）：

```promql
histogram_quantile(0.50,
  sum by (le, cluster_queue) (
    rate(kueue_admission_wait_time_seconds_bucket[5m])
  )
)
```

对于第 99 百分位数 (P99)：

```promql
histogram_quantile(0.99,
  sum by (le, cluster_queue) (
    rate(kueue_admission_wait_time_seconds_bucket[5m])
  )
)
```

## 工作负载吞吐量

要监控每小时允许的工作负载数量：

```promql
sum by (cluster_queue) (
  increase(kueue_admitted_workloads_total[1h])
)
```

监控每小时完成的工作量：

```promql
sum by (cluster_queue) (
  increase(kueue_finished_workloads_total[1h])
)
```

查看一段时间内的接收率（每分钟工作量）：

```promql
sum by (cluster_queue) (
  rate(kueue_admitted_workloads_total[5m])
) * 60
```

## 驱逐率

按原因监测每小时的驱逐数量：

```promql
sum by (cluster_queue, reason) (
  increase(kueue_evicted_workloads_total[1h])
)
```

请参阅 [Prometheus 指标](/docs/reference/metrics)获取完整的 `reason` 标签值列表。

## 集群队列状态

要查看哪些集群队列处于活动状态：

```promql
kueue_cluster_queue_status{status="active"} == 1
```

要查看未激活（待处理或正在终止）的集群队列：

```promql
kueue_cluster_queue_status{status!="active"} == 1
```

## 下一步

- 请参阅 [Grafana 中的待处理工作负载](/docs/tasks/manage/monitor_pending_workloads/pending_workloads_in_grafana)，
  了解如何使用按需 API 创建可视化仪表板。