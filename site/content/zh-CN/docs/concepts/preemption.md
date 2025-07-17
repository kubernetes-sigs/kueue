---
title: "抢占（Preemption）"
date: 2024-05-28
weight: 7
description: >
  抢占是为了容纳另一个 Workload 而驱逐一个或多个已准入 Workload 的过程。
---

在抢占过程中，以下术语相关：
- **被抢占者（Preemptees）**：被抢占的 Workload。
- **目标 ClusterQueue**：被抢占者所属的 ClusterQueue。
- **抢占者（Preemptor）**：需要被容纳的 Workload。
- **抢占 ClusterQueue**：抢占者所属的 ClusterQueue。

## 抢占的原因

如果 Workload 被准入到[启用了抢占的 ClusterQueue](/docs/concepts/cluster_queue/#preemption)，并且发生以下任一事件，则该 Workload 可以抢占一个或多个 Workload：
- 被抢占者与抢占者属于同一个 [ClusterQueue](/docs/concepts/cluster_queue)，且被抢占者的优先级较低。
- 被抢占者与抢占者属于同一个 [cohort](/docs/concepts/cluster_queue#cohort)，且被抢占者的 ClusterQueue 至少有一种资源的使用量高于[名义配额](/docs/concepts/cluster_queue#resources)，而该资源是被抢占者和抢占者都需要的。

在 [Kueue 配置](/docs/reference/kueue-config.v1beta1#FairSharing) 和 [ClusterQueue](/docs/concepts/cluster_queue#preemption) 中配置的抢占设置，除了上述标准外，还可以限制 Workload 是否可以抢占其他 Workload。

当抢占 Workload 时，Kueue 会在被抢占 Workload 的 `.status.conditions` 字段中添加类似如下的条目：

```yaml
status:
  - lastTransitionTime: "2025-03-07T21:19:54Z"
    message: '为容纳另一个 workload（UID: 5c023c28-8533-4927-b266-56bca5e310c1，JobUID: 4548c8bd-c399-4027-bb02-6114f3a8cdeb）而被抢占，原因是 ClusterQueue 中的优先级调整'
    observedGeneration: 1
    reason: Preempted
    status: "True"
    type: Evicted
  - lastTransitionTime: "2025-03-07T21:19:54Z"
    message: '为容纳另一个 workload（UID: 5c023c28-8533-4927-b266-56bca5e310c1，JobUID: 4548c8bd-c399-4027-bb02-6114f3a8cdeb）而被抢占，原因是 ClusterQueue 中的优先级调整'
    reason: InClusterQueue
    status: "True"
    type: Preempted
```

`Evicted` 条件表示该 Workload 因 `Preempted` 原因被驱逐，而 `Preempted` 条件则给出了更多关于抢占原因的细节。

可以通过运行 `kubectl get workloads --selector=kueue.x-k8s.io/job-uid=<JobUID> --all-namespaces` 查找抢占者 workload。

## 抢占算法

Kueue 提供了两种抢占算法。它们的主要区别在于：当抢占 ClusterQueue 的使用量已经超过名义配额时，允许从一个 ClusterQueue 抢占到 cohort 中其他 ClusterQueue 的标准不同。两种算法如下：

- **[经典抢占（Classic Preemption）](#经典抢占classic-preemption)**：只有在以下任一情况发生时，cohort 内的抢占才会发生：
  - 新 workload 的 ClusterQueue 使用量在本次准入后将低于名义配额
  - 工作负载的 ClusterQueue 启用了“借用时抢占”（borrowWithinCohort）
  - 所有抢占候选都属于与抢占者相同的 ClusterQueue

  在上述场景下，只有当被抢占 workload 所属的 ClusterQueue 超过其名义配额时，才会考虑抢占以支持来自其他 ClusterQueue 的 workload。
  cohort 内的 ClusterQueue 以先到先得的方式借用资源。
  
  该算法是两者中最轻量的。

- **[公平共享（Fair Sharing）](#公平共享fair-sharing)**：cohort 内有待处理 Workload 的 ClusterQueue 可以抢占 cohort 内其他 Workload，直到抢占 ClusterQueue 获得等量或加权份额的可借用资源。
  可借用资源是 cohort 内所有 ClusterQueue 未使用的名义配额。

## 经典抢占（Classic Preemption）

当一个新 Workload 无法适配未使用配额时，只有在以下任一条件为真时才有资格发起抢占：
- 该 Workload 的请求低于 flavor 的名义配额，或
- 启用了 `borrowWithinCohort`。

### 候选者

抢占候选列表由以下 Workload 组成：
- 属于与抢占者 Workload 相同的 ClusterQueue，并满足抢占者 ClusterQueue 的 `withinClusterQueue` 策略
- 属于 cohort 内其他正在借用的 ClusterQueue，并满足抢占者 ClusterQueue 的 `reclaimWithinCohort` 和 `borrowWithinCohort` 策略

候选列表根据以下优先级进行排序以便于决策：
- cohort 内正在借用的队列中的 Workload
- 优先级最低的 Workload
- 最近被准入的 Workload

### 目标

经典抢占算法根据以下启发式方法将候选者确定为抢占目标：

1. 如果所有候选者都属于目标队列，则 Kueue 会贪婪地选择候选者，直到抢占者 Workload 能够适配，允许 ClusterQueue 的使用量超过名义配额，直至 `borrowingLimit`。这被称为“借用”。

2. 如果启用了 `borrowWithinCohort`，则 Kueue 会贪婪地选择候选者（遵循 `borrowWithinCohort.maxPriorityThreshold` 阈值），直到抢占者 Workload 能够适配，允许借用。

3. 如果目标队列当前的使用量低于名义配额，则 Kueue 会贪婪地选择候选者，直到抢占者 Workload 能够适配，不允许借用。

4. 如果通过上述方法仍无法适配，Kueue 只会贪婪地选择属于抢占 ClusterQueue 的候选者，直到抢占者 Workload 能够适配，允许借用。

算法的最后一步是最小化目标集合。为此，Kueue 会反向遍历初始目标列表，并在抢占者 Workload 仍能适配的情况下，将目标 Workload 从目标列表中移除。

## 公平共享（Fair Sharing）

公平共享引入了 ClusterQueue 份额值和抢占策略的概念。这些与 `withinClusterQueue` 和 `reclaimWithinCohort`（但__不包括__ `borrowWithinCohort`）中设置的抢占策略共同决定在公平共享下，待处理 Workload 是否可以抢占已准入 Workload。公平共享通过抢占实现 cohort 租户间可借用资源的等量或加权分配。

{{< feature-state state="stable" for_version="v0.7" >}}
{{% alert title="注意" color="primary" %}}
自 v0.11 起，公平共享兼容分层 cohort（即有父级的 cohort）。在 V0.9 和 V0.10 中同时使用这些功能是不支持的，会导致未定义行为。
{{% /alert %}}

要启用公平共享，[请使用如下 Kueue 配置](/docs/installation#install-a-custom-configured-release-version)：

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta1
kind: Configuration
fairSharing:
  enable: true
  preemptionStrategies: [LessThanOrEqualToFinalShare, LessThanInitialShare]
```

Kueue 配置中的各属性将在下文中说明。

### ClusterQueue 份额值

启用公平共享后，Kueue 会为每个 ClusterQueue 分配一个数值型份额值，用于总结该 ClusterQueue 借用资源的使用情况，并与同一 cohort 内的其他 ClusterQueue 进行比较。
份额值会根据 ClusterQueue 中定义的 `.spec.fairSharing.weight` 进行加权。

在准入过程中，Kueue 优先从份额值最低的 ClusterQueue 中准入 Workload。
在抢占过程中，Kueue 优先从份额值最高的 ClusterQueue 中抢占 Workload。

你可以在 `.status.fairSharing.weightedShare` 字段或通过查询 [`kueue_cluster_queue_weighted_share` 指标](/docs/reference/metrics#optional-metrics) 获取 ClusterQueue 的份额值。

### 抢占策略

Kueue 配置中的 `preemptionStrategies` 字段表示抢占时，针对目标和抢占 ClusterQueue 的份额值，在抢占特定 Workload 前后应满足哪些约束。

不同的 `preemptionStrategies` 在特定场景下会导致更少或更多的抢占。配置抢占策略时应考虑以下因素：
- 对中断的容忍度，尤其是当单个 Workload 占用大量可借用资源时。
- 收敛速度，即多快达到公平状态。
- 整体利用率，因为某些策略可能会在追求公平的过程中降低集群利用率。

当你定义多个 `preemptionStrategies` 时，抢占算法只有在当前策略下没有更多满足条件的抢占候选且抢占者仍无法适配时，才会使用下一个策略。

`preemptionStrategies` 列表可选值如下：
- `LessThanOrEqualToFinalShare`：只有当抢占 ClusterQueue 在包含抢占者 Workload 后的份额小于等于目标 ClusterQueue 在移除被抢占 Workload 后的份额时，才允许抢占。
  该策略可能会优先抢占目标 ClusterQueue 中较小的 workload，以尽量保持其份额较高，而不考虑优先级或启动时间。
- `LessThanInitialShare`：只有当抢占 ClusterQueue 在包含抢占者 Workload 后的份额严格小于目标 ClusterQueue 的份额时，才允许抢占。
  注意该策略不依赖于被抢占 Workload 的份额使用情况。因此，该策略会优先选择抢占目标 ClusterQueue 内优先级最低、启动时间最新的 workload。
默认策略为 `[LessThanOrEqualToFinalShare, LessThanInitialShare]`

### 算法概述

算法的第一步是识别[抢占候选 Workload](#候选者)，其标准和排序与经典抢占相同，并按 ClusterQueue 分组。

接下来，算法会将上述候选者作为抢占目标进行筛选，流程可总结如下：

```
FindFairPreemptionTargets(X ClusterQueue, W Workload)
  对于每个抢占策略：
    当 W 仍无法适配且存在抢占候选时：
      找到份额值最高的 ClusterQueue Y。
      对于 ClusterQueue Y 中已准入的每个 Workload U：
        如果 Workload U 满足当前抢占策略：
          将 U 加入目标列表
  反向遍历目标列表：
    在 W 仍能适配的情况下，尝试将目标 Workload 从列表中移除。
```
