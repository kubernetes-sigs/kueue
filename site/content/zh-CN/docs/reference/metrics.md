---
title: "Prometheus 指标"
linkTitle: "Prometheus 指标"
date: 2022-02-14
description: >
  Kueue 的 Prometheus 指标
---

{{% alert title="Note" color="primary" %}}
要启用指标抓取，请参阅[设置 Prometheus](/docs/tasks/manage/observability/setup_prometheus)。
{{% /alert %}}

Kueue 暴露了 [prometheus](https://prometheus.io) 指标来监控系统的健康状况和
[ClusterQueues](/docs/concepts/cluster_queue)
以及 [LocalQueues](/docs/concepts/local_queue) 的状态。

<!-- NOTE: Sections below contain tables generated from code. Do not edit content between BEGIN/END GENERATED TABLE markers. Use `make generate-metrics-tables` to update. -->

## Kueue 健康状态

使用以下指标来监控 kueue 控制器的健康状况：

<!-- BEGIN GENERATED TABLE: health -->
| 指标名称 | 类型 | 描述 | 标签 |
| --- | --- | --- | --- |
| `kueue_cohort_subtree_admitted_workloads_total` | 计数器 | 每个队列子树中已接受的工作负载总数 | `cohort`：队列的名称<br> `priority_class`：优先级类名称<br> `replica_role`：其中之一 `leader`、`follower` 或 `standalone` |
| `kueue_admission_attempt_duration_seconds` | 直方图 | 一次准入尝试的延迟。<br>标签 'result' 可以有以下值：<br>- 'success' 表示至少有一个工作负载被接纳，<br>- 'inadmissible' 表示没有工作负载被接纳。 | `result`: 可能的值为 `success` 或 `inadmissible`<br>`replica_role`: 可以为 `leader`、`follower` 或 `standalone` |
| `kueue_admission_attempts_total` | 计数器 | 尝试接纳工作负载的总次数。<br>每次接纳尝试可能尝试接纳多于一个工作负载。<br>标签 'result' 可以有以下值：<br>- 'success' 表示至少有一个工作负载被接纳，<br>- 'inadmissible' 表示没有工作负载被接纳。 | `result`: 可能的值为 `success` 或 `inadmissible`<br>`replica_role`: 可以为 `leader`、`follower` 或 `standalone` |
<!-- END GENERATED TABLE: health -->

## ClusterQueue 状态

使用以下指标来监控你的 ClusterQueues 的状态：

<!-- BEGIN GENERATED TABLE: clusterqueue -->
| 指标名称 | 类型 | 描述 | 标签 |
| --- | --- | --- | --- |
| `kueue_admission_checks_wait_time_seconds` | Histogram | 从工作负载获得配额预留到准入的时间，按 'cluster_queue' | `cluster_queue`: ClusterQueue 的名称<br> `priority_class`: 优先级类名称<br> `replica_role`: `leader`, `follower` 或 `standalone` 其中之一 |
| `kueue_admission_cycle_preemption_skips` | Gauge | 在 ClusterQueue 中由于其他 ClusterQueues 在同一周期需要相同的资源而必须跳过的具有抢占候选资格的工作负载数量 | `cluster_queue`: ClusterQueue 的名称<br> `replica_role`: `leader`, `follower` 或 `standalone` 其中之一 |
| `kueue_admission_wait_time_seconds` | Histogram | 工作负载创建或重新排队直到准入的时间，按 'cluster_queue' | `cluster_queue`: ClusterQueue 的名称<br> `priority_class`: 优先级类名称<br> `replica_role`: `leader`, `follower` 或 `standalone` 其中之一 |
| `kueue_admitted_active_workloads` | Gauge | 按 'cluster_queue' 统计已准入且活动（未挂起且未完成）的工作负载数量 | `cluster_queue`: ClusterQueue 的名称<br> `replica_role`: `leader`, `follower` 或 `standalone` 其中之一 |
| `kueue_admitted_workloads_total` | Counter | 每个 'cluster_queue' 已准入的工作负载总数 | `cluster_queue`: ClusterQueue 的名称<br> `priority_class`: 优先级类名称<br> `replica_role`: `leader`, `follower` 或 `standalone` 其中之一 |
| `kueue_build_info` | Gauge | Kueue 构建信息。1 标记了 git 版本、git 提交、构建日期、go 版本、编译器和平台 | `git_version`: git 版本<br> `git_commit`: git 提交<br> `build_date`: 构建日期<br> `go_version`: go 版本<br> `compiler`: 编译器<br> `platform`: 平台 |
| `kueue_cluster_queue_status` | Gauge | 报告 'cluster_queue' 及其 'status' （可能的值为 'pending', 'active' 或 'terminated'）。<br>对于一个 ClusterQueue，该指标仅报告其中一个状态的值为 1 | `cluster_queue`: ClusterQueue 的名称<br> `status`: `pending`, `active` 或 `terminated` 其中之一<br> `replica_role`: `leader`、`follower` 或 `standalone` 其中之一 |
| `kueue_evicted_workloads_once_total` | Counter | 每个 'cluster_queue' 独特工作负载驱逐的数量,<br>标签 'reason' 可能的值如下:<br>- "Preempted" 表示为了释放资源给更高优先级的工作负载或回收名义配额而驱逐。<br>- "PodsReadyTimeout" 表示由于 PodsReady 超时发生驱逐。<br>- "AdmissionCheck" 表示由于至少一个准入检查变为 False 而驱逐。<br>- "ClusterQueueStopped" 表示由于 ClusterQueue 停止而驱逐。<br>- "LocalQueueStopped" 表示由于 LocalQueue 停止而驱逐。<br>- "NodeFailures" 表示在使用 TopologyAwareScheduling 时由于节点故障而驱逐。<br>- "Deactivated" 表示由于 spec.active 设置为 false 而驱逐。<br>标签 'underlying_cause' 可能的值如下:<br>- "" 表示 'reason' 标签中的值是驱逐的根本原因。<br>- "WaitForStart" 表示自准入以来 Pod 尚未准备好，或者工作负载尚未被接纳。<br>- "WaitForRecovery" 表示自工作负载准入以来 Pod 已经准备好，但某些 Pod 发生了故障。<br>- "AdmissionCheck" 表示由 Kueue 因拒绝准入检查而驱逐。<br>- "MaximumExecutionTimeExceeded" 表示由 Kueue 因超过最大执行时间而驱逐。<br>- "RequeuingLimitExceeded" 表示由 Kueue 因超过重新排队限制而驱逐。 | `cluster_queue`: ClusterQueue 的名称<br> `reason`: 驱逐或抢占的原因<br> `underlying_cause`: 驱逐的根本原因<br> `priority_class`: 优先级类名称<br> `replica_role`: `leader`, `follower` 或 `standalone` 其中之一 |
| `kueue_evicted_workloads_total` | Counter | 每个 'cluster_queue' 驱逐的工作负载数量,<br>标签 'reason' 可能的值如下:<br>- "Preempted" 表示为了释放资源给更高优先级的工作负载或回收名义配额而驱逐。<br>- "PodsReadyTimeout" 表示由于 PodsReady 超时发生驱逐。<br>- "AdmissionCheck" 表示由于至少一个准入检查变为 False 而驱逐。<br>- "ClusterQueueStopped" 表示由于 ClusterQueue 停止而驱逐。<br>- "LocalQueueStopped" 表示由于 LocalQueue 停止而驱逐。<br>- "NodeFailures" 表示在使用 TopologyAwareScheduling 时由于节点故障而驱逐。<br>- "Deactivated" 表示由于 spec.active 设置为 false 而驱逐。<br>标签 'underlying_cause' 可能的值如下:<br>- "" 表示 'reason' 标签中的值是驱逐的根本原因。<br>- "AdmissionCheck" 表示由 Kueue 因拒绝准入检查而驱逐。<br>- "MaximumExecutionTimeExceeded" 表示由 Kueue 因超过最大执行时间而驱逐。<br>- "RequeuingLimitExceeded" 表示由 Kueue 因超过重新排队限制而驱逐。 | `cluster_queue`: ClusterQueue 的名称<br> `reason`: 驱逐或抢占的原因<br> `underlying_cause`: 驱逐的根本原因<br> `priority_class`: 优先级类名称<br> `replica_role`: `leader`, `follower` 或 `standalone` 其中之一 |
| `kueue_finished_workloads` | Gauge | 每个 'cluster_queue' 完成的工作负载数量 | `cluster_queue`: ClusterQueue 的名称<br> `replica_role`: `leader`, `follower` 或 `standalone` 其中之一 |
| `kueue_finished_workloads_total` | Counter | 每个 'cluster_queue' 总共完成的工作负载数量 | `cluster_queue`: ClusterQueue 的名称<br> `priority_class`: 优先级类名称<br> `replica_role`: `leader`、`follower` 或 `standalone` 其中之一 |
| `kueue_pending_workloads` | Gauge | 每个 'cluster_queue' 和 'status' 的待处理工作负载数量。<br>'status' 可能的值如下:<br>- "active" 表示工作负载在准入队列中。<br>- "inadmissible" 表示这些工作负载有一个失败的准入尝试，并且不会重试，直到集群条件发生变化，这可能会使此工作负载变得可接受 | `cluster_queue`: ClusterQueue 的名称<br> `status`: 状态标签（随指标变化）<br> `replica_role`: `leader`、`follower` 或 `standalone` 其中之一 |

<!-- END GENERATED TABLE: clusterqueue -->

## LocalQueue 状态（Alpha）

只有在启用了 `LocalQueueMetrics` 特性门控时，以下度量才可用。
详情请参阅[安装](/docs/installation/)的[更改特性门控配置](/docs/installation/#change-the-feature-gates-configuration)部分。

<!-- BEGIN GENERATED TABLE: localqueue -->
| 指标名称 | 类型 | 描述 | 标签 |
| `kueue_local_queue_admission_checks_wait_time_seconds` | 直方图 | 从工作负载获得配额预留到准入的时间，按 'local_queue' 计 | `name`: LocalQueue 的名称<br> `namespace`: LocalQueue 的命名空间<br> `priority_class`: 优先级类名称<br> `replica_role`: `leader`、`follower` 或 `standalone` 其中之一 |
| `kueue_local_queue_admission_wait_time_seconds` | 直方图 | 从工作负载创建或重新排队到准入的时间，按 'local_queue' 计 | `name`: LocalQueue 的名称<br> `namespace`: LocalQueue 的命名空间<br> `priority_class`: 优先级类名称<br> `replica_role`: `leader`、`follower` 或 `standalone` 其中之一 |
| `kueue_local_queue_admitted_active_workloads` | 仪表盘 | 每个 'localQueue' 处于活动状态（未挂起且未完成）的已准入工作负载数 | `name`: LocalQueue 的名称<br> `namespace`: LocalQueue 的命名空间<br> `replica_role`: `leader`、`follower` 或 `standalone` 其中之一 |
| `kueue_local_queue_admitted_workloads_total` | 计数器 | 每个 'local_queue' 已准入的工作负载总数 | `name`: LocalQueue 的名称<br> `namespace`: LocalQueue 的命名空间<br> `priority_class`: 优先级类名称<br> `replica_role`: `leader`、`follower` 或 `standalone` 其中之一 |
| `kueue_local_queue_evicted_workloads_total` | 计数器 | 每个 'local_queue' 驱逐的工作负载数<br>标签 'reason' 可能有以下值：<br>- "Preempted" 表示由于为更高优先级的工作负载腾出资源或回收名义配额而驱逐了工作负载。<br>- "PodsReadyTimeout" 表示由于 PodsReady 超时导致驱逐。<br>- "AdmissionCheck" 表示由于至少一个准入检查变为 False 导致工作负载被驱逐。<br>- "ClusterQueueStopped" 表示由于 ClusterQueue 停止而导致工作负载被驱逐。<br>- "LocalQueueStopped" 表示由于 LocalQueue 停止而导致工作负载被驱逐。<br>- "NodeFailures" 表示在使用 TopologyAwareScheduling 时，由于节点故障导致工作负载被驱逐。<br>- "Deactivated" 表示因为 spec.active 设置为 false 而驱逐了工作负载。<br>标签 'underlying_cause' 可能有以下值：<br>- "" 表示 'reason' 标签中的值是驱逐的根本原因。<br>- "AdmissionCheck" 表示 Kueue 因拒绝的准入检查而驱逐了工作负载。<br>- "MaximumExecutionTimeExceeded" 表示 Kueue 因超过最大执行时间而驱逐了工作负载。<br>- "RequeuingLimitExceeded" 表示 Kueue 因超过重新排队限制而驱逐了工作负载。 | `name`: LocalQueue 的名称<br> `namespace`: LocalQueue 的命名空间<br> `reason`: 驱逐或抢占原因<br> `underlying_cause`: 驱逐的根本原因<br> `priority_class`: 优先级类名称<br> `replica_role`: `leader`、`follower` 或 `standalone` 其中之一 |
| `kueue_local_queue_finished_workloads` | 仪表盘 | 每个 'local_queue' 完成的工作负载数 | `name`: LocalQueue 的名称<br> `namespace`: LocalQueue 的命名空间<br> `replica_role`: `leader`、`follower` 或 `standalone` 其中之一 |
| `kueue_local_queue_finished_workloads_total` | 计数器 | 每个 'local_queue' 完成的工作负载总数 | `name`: LocalQueue 的名称<br> `namespace`: LocalQueue 的命名空间<br> `priority_class`: 优先级类名称<br> `replica_role`: `leader`、`follower` 或 `standalone` 其中之一 |
| `kueue_local_queue_pending_workloads` | 仪表盘 | 每个 'local_queue' 和 'status' 的待处理工作负载数。<br>'status' 可以有以下值：<br>- "active" 表示工作负载在准入队列中。<br>- "inadmissible" 表示这些工作负载有一次失败的准入尝试，它们不会重试，直到集群条件发生变化，可能使该工作负载可准入 | `name`: LocalQueue 的名称<br> `namespace`: LocalQueue 的命名空间<br> `status`: 状态标签（随度量变化）<br> `replica_role`: `leader`、`follower` 或 `standalone` 其中之一 |
| `kueue_local_queue_quota_reserved_wait_time_seconds` | 直方图 | 从工作负载创建或重新排队到它获得配额预留的时间，按 'local_queue' 计 | `name`: LocalQueue 的名称<br> `namespace`: LocalQueue 的命名空间<br> `priority_class`: 优先级类名称<br> `replica_role`: `leader`、`follower` 或 `standalone` 其中之一 |
| `kueue_local_queue_quota_reserved_workloads_total` | 计数器 | 每个 'local_queue' 配额预留的工作负载总数 | `name`: LocalQueue 的名称<br> `namespace`: LocalQueue 的命名空间<br> `priority_class`: 优先级类名称<br> `replica_role`: `leader`、`follower` 或 `standalone` 其中之一 |
| `kueue_local_queue_reserving_active_workloads` | 仪表盘 | 正在预留配额的工作负载数，按 'localQueue' 计 | `name`: LocalQueue 的名称<br> `namespace`: LocalQueue 的命名空间<br> `replica_role`: `leader`、`follower` 或 `standalone` 其中之一 |
| `kueue_local_queue_resource_reservation` | 仪表盘 | 报告 localQueue 在所有 flavors 中的总资源预留 | `name`: LocalQueue 的名称<br> `namespace`: LocalQueue 的命名空间<br> `flavor`: 资源 flavor 名称<br> `resource`: 资源名称<br> `replica_role`: `leader`、`follower` 或 `standalone` 其中之一 |
| `kueue_local_queue_resource_usage` | 仪表盘 | 报告 localQueue 在所有 flavors 中的总资源使用情况 | `name`: LocalQueue 的名称<br> `namespace`: LocalQueue 的命名空间<br> `flavor`: 资源 flavor 名称<br> `resource`: 资源名称<br> `replica_role`: `leader`、`follower` 或 `standalone` 其中之一 |
| `kueue_local_queue_status` | 仪表盘 | 报告 'localQueue' 的 'active' 状态（可能的值为 'True'、'False' 或 'Unknown'）。<br>对于 LocalQueue，度量仅报告其中一个状态的值为 1 | `name`: LocalQueue 的名称<br> `namespace`: LocalQueue 的命名空间<br> `active`: `True`、`False` 或 `Unknown` 其中之一<br> `replica_role`: `leader`、`follower` 或 `standalone` 其中之一 |
<!-- END GENERATED TABLE: localqueue -->

## Cohort 状态

<!-- BEGIN GENERATED TABLE: cohort -->
| 指标名称 | 类型 | 描述 | 标签 |
| --- | --- | --- | --- |
| `kueue_cohort_weighted_share` | 仪表盘 | 报告一个值，该值表示在 Cohort 提供的所有资源中，使用量高于名义配额与可借资源比率的最大值除以权重。<br>如果为零，意味着 Cohort 的使用量低于名义配额。<br>如果 Cohort 的权重为零且正在借用，这将返回 NaN。 | `cohort`: Cohort 的名称<br> `replica_role`: `leader`、`follower` 或 `standalone` 其中之一 |
<!-- END GENERATED TABLE: cohort -->

### 可选指标

以下指标仅在[管理器配置](/docs/installation/#install-a-custom-configured-released-version)中启用了
`metrics.enableClusterQueueResources` 时可用。

<!-- BEGIN GENERATED TABLE: optional_clusterqueue_resources -->
| 指标名称 | 类型 | 描述 | 标签 |
| --- | --- | --- | --- |
| `kueue_cluster_queue_borrowing_limit` | 仪表盘 | 报告 cluster_queue 在所有 flavors 中的资源借用限制 | `cohort`: Cohort 的名称<br> `cluster_queue`: ClusterQueue 的名称<br> `flavor`: 资源 flavor 名称<br> `resource`: 资源名称<br> `replica_role`: `leader`、`follower` 或 `standalone` 其中之一 |
| `kueue_cluster_queue_lending_limit` | 仪表盘 | 报告 cluster_queue 在所有 flavors 中的资源借出限制 | `cohort`: Cohort 的名称<br> `cluster_queue`: ClusterQueue 的名称<br> `flavor`: 资源 flavor 名称<br> `resource`: 资源名称<br> `replica_role`: `leader`、`follower` 或 `standalone` 其中之一 |
| `kueue_cluster_queue_nominal_quota` | 仪表盘 | 报告 cluster_queue 在所有 flavors 中的资源名义配额 | `cohort`: Cohort 的名称<br> `cluster_queue`: ClusterQueue 的名称<br> `flavor`: 资源 flavor 名称<br> `resource`: 资源名称<br> `replica_role`: `leader`、`follower` 或 `standalone` 其中之一 |
| `kueue_cluster_queue_resource_reservation` | 仪表盘 | 报告 cluster_queue 在所有 flavors 中的总资源预留 | `cohort`: Cohort 的名称<br> `cluster_queue`: ClusterQueue 的名称<br> `flavor`: 资源 flavor 名称<br> `resource`: 资源名称<br> `replica_role`: `leader`、`follower` 或 `standalone` 其中之一 |
| `kueue_cluster_queue_resource_usage` | 仪表盘 | 报告 cluster_queue 在所有 flavors 中的总资源使用量 | `cohort`: Cohort 的名称<br> `cluster_queue`: ClusterQueue 的名称<br> `flavor`: 资源 flavor 名称<br> `resource`: 资源名称<br> `replica_role`: `leader`、`follower` 或 `standalone` 其中之一 |
| `kueue_cluster_queue_weighted_share` | 仪表盘 | 报告一个值，该值表示由 ClusterQueue 提供的所有资源中，使用量高于名义配额与可借资源比率的最大值除以权重。<br>如果为零，意味着 ClusterQueue 的使用量低于名义配额。<br>如果 ClusterQueue 的权重为零且正在借用，这将返回 NaN。 | `cluster_queue`: ClusterQueue 的名称<br> `cohort`: Cohort 的名称<br> `replica_role`: `leader`、`follower` 或 `standalone` 其中之一 |
<!-- END GENERATED TABLE: optional_clusterqueue_resources -->

以下指标仅在[管理器配置](/docs/installation/#install-a-custom-configured-released-version)中启用了 `waitForPodsReady` 时可用。
更多详情[见](/docs/tasks/manage/setup_wait_for_pods_ready)。

<!-- BEGIN GENERATED TABLE: optional_wait_for_pods_ready -->
| 指标名称 | 类型 | 描述 | 标签 |
| --- | --- | --- | --- |
| `kueue_admitted_until_ready_wait_time_seconds` | 直方图 | 工作负载从被接纳到准备就绪的时间，按 'cluster_queue' 分组 | `cluster_queue`: ClusterQueue 的名称<br> `priority_class`: 优先级类名称<br> `replica_role`: `leader`、`follower` 或 `standalone` 其中之一 |
| `kueue_local_queue_admitted_until_ready_wait_time_seconds` | 直方图 | 工作负载从被接纳到准备就绪的时间，按 'local_queue' 分组 | `name`: LocalQueue 的名称<br> `namespace`: LocalQueue 的命名空间<br> `priority_class`: 优先级类名称<br> `replica_role`: `leader`、`follower` 或 `standalone` 其中之一 |
| `kueue_local_queue_ready_wait_time_seconds` | 直方图 | 工作负载从创建或重新排队到准备就绪的时间，按 'local_queue' 分组 | `name`: LocalQueue 的名称<br> `namespace`: LocalQueue 的命名空间<br> `priority_class`: 优先级类名称<br> `replica_role`: `leader`、`follower` 或 `standalone` 其中之一 |
| `kueue_ready_wait_time_seconds` | 直方图 | 工作负载从创建或重新排队到准备就绪的时间，按 'cluster_queue' 分组 | `cluster_queue`: ClusterQueue 的名称<br> `priority_class`: 优先级类名称<br> `replica_role`: `leader`、`follower` 或 `standalone` 其中之一 |
<!-- END GENERATED TABLE: optional_wait_for_pods_ready -->
