---
title: "MultiKueue"
date: 2024-11-11
weight: 8
description: >
  Kueue 多集群作业调度。
---

{{< feature-state state="beta" for_version="v0.9" >}}

{{% alert title="Note" color="primary" %}}
`MultiKueue` 目前是一个 Beta 特性，默认启用。

你可以通过编辑 `MultiKueue` **特性门控**来禁用它。关于特性门控配置的详情，
请参阅[安装指南](/zh-CN/docs/installation/#change-the-feature-gates-configuration)。
{{% /alert %}}

MultiKueue 设置由一个管理集群和至少一个工作集群组成。

## 集群角色
### 管理集群

管理者的首要职责包括：
- 建立并维护与工作集群的连接。
- 创建并监控远程对象（工作负载或作业），同时保持本地对象同步。

MultiKueue 准入检查控制器运行在管理集群中，并且还将维护由 `multikueue` 控制的准入检查的 `Active` 状态。

使用 MultiKueue 为 ClusterQueue 的 flavor 设置的配额控制了在特定时间点有多少作业可以被调度。
理想情况下，管理集群中的配额应等于工作集群中的总配额。
如果显著低于工作集群中的总配额，工作集群将利用率不足。
如果显著高于工作集群中的总配额，管理者将在没有机会被接纳的工作集群中调度和监控工作负载。

### 工作集群

工作集群就像一个独立的 Kueue 集群。
工作负载和作业是由运行在管理集群中的 MultiKueue 准入检查控制器创建和删除的。

### 使用 manager 运行工作负载

MultiKueue 支持在使用专用 ClusterQueue 时，在 manager 上运行常规 Job。
然而，当前我们不支持 manager 集群也作为其自身的工作者这种角色共享，
详情请参见[限制](#limitations)。

## 工作负载调度

{{% alert title="注意" color="primary" %}}
MultiKueue 调度机制自 Kueue v0.13 起可用。
{{% /alert %}}

通过允许用户选择内置的调度算法或实现自定义算法，提供了一种灵活的方式来控制工作负载如何在工作集群之间分配。
此机制确保了高效的工作负载放置，同时最小化了跨集群的资源争用和抢占。

`status.nominatedClusterNames` 字段列出了根据调度算法当前正在考虑用于调度工作负载的工作集群，
并且在工作负载等待准入期间会更新。

`status.clusterName` 字段指定了工作负载已成功被接纳的工作集群。
一旦设置了该字段，它将变为不可变，同时 `status.nominatedClusterNames`
字段会被重置，并且不再可能设置它。

这确保了工作负载的集群分配最终确定并防止进一步提名集群。

### 全部一次性（默认模式）：

在此模式下，只要在管理集群中获得了配额预留，工作负载就会立即复制到所有可用的工作集群。
这种方法通过允许所有集群同时竞争工作负载来确保最快的可能接纳速度。

### 增量：

此模式引入了一种逐步调度策略，集群按轮次提名。最
初，所有工作集群按字典顺序排序，并从排序列表中选择最多 3 个集群的一个子集。
工作负载仅复制到这些提名的集群。如果在固定持续时间（5 分钟）内，没有一个提名的集群接纳工作负载，
则会在后续轮次中按照相同的字典顺序增量添加最多 3 个额外的集群。

### 外部（自定义实现）：

在此模式下，工作集群的选择委托给外部控制器。外部控制器负责设置工作负载中的
`.status.nominatedClusterNames` 字段以指定应将其复制到的集群。

MultiKueue 工作负载控制器与提名的集群同步工作负载。

已知限制：
{{% alert title="警告" color="primary" %}}
对于外部控制器修补 `.status.nominatedClusterNames` 字段有两种选项：
* 使用 `kueue-admission` 字段管理器，因为 kueue-admission
  字段管理器负责管理对 `.status.nominatedClusterNames` 字段的更新。
* [启用 `WorkloadRequestUseMergePatch` 特性门控](docs/concepts/workload#workload-updates-by-kueue)，
  这将删除 `.status.nominatedClusterNames` 中的 `kueue-admission` 字段管理器。

如果不这样做，Kueue 将无法接纳 MultiKueue 工作负载。
{{% /alert %}}

## 作业流程

为了让作业能够进行多集群调度，你需要将其分配给使用 MultiKueue 准入检查控制的 ClusterQueue。
Multikueue 系统的工作方式如下：
- 当作业的工作负载在管理集群中获得 QuotaReservation 时，该工作负载的一个副本将在所有配置的工作集群中被创建。
- 当其中一个工作集群接受发送给它的远程工作负载时：
  - 管理者会移除所有其他远程工作负载。
  - 管理者在选定的工作集群中创建作业的一个副本，配置为使用被接受工作负载保留的配额，
    通过设置作业的 `kueue.x-k8s.io/prebuilt-workload-name` 标签。
- 管理者监控远程对象、工作负载和作业，并同步其状态到本地对象中。
- 当远程工作负载被标记为 `Finished` 时：
  - 管理者对对象状态进行最后一次同步。
  - 管理者从工作集群中移除对象。

{{% alert title="Note" color="primary" %}}
默认情况下，只有当工作负载完全被接纳（配额预留且所有准入检查均满足）后，
才会从非选定的工作集群中删除这些工作负载。这允许在工作集群之间并行处理资源请求。
若要恢复为以前的行为，即一旦配额预留就立即删除工作负载，
请禁用 `MultiKueueWaitForWorkloadAdmitted` 特性门控。
{{% /alert %}}

## 支持的作业

MultiKueue 支持多种工作负载，你可以学习如何：
- [调度 Kueue 管理的 Deployment](docs/tasks/run/multikueue/deployment)。
- [调度 Kueue 管理的 batch/Job](docs/tasks/run/multikueue/job)。
- [调度 Kueue 管理的 JobSet](docs/tasks/run/multikueue/jobsets)。
- [调度 Kueue 管理的 Kubeflow Job](docs/tasks/run/multikueue/kubeflow)。
- [调度 Kueue 管理的 KubeRay 工作负载](docs/tasks/run/multikueue/kuberay)。
- [调度 Kueue 管理的 MPIJob](docs/tasks/run/multikueue/mpijob)。
- [调度 Kueue 管理的 AppWrapper](docs/tasks/run/multikueue/appwrapper)。
- [调度 Kueue 管理的普通 Pod](docs/tasks/run/multikueue/plain_pods)。
- [调度 Kueue 管理的 StatefulSet](docs/tasks/run/multikueue/statefulset).
- [调度 Kueue 管理的 LeaderWorkerSet](docs/tasks/run/multikueue/leaderworkerset).
- [调度 Kueue 管理的 外部框架 Job](docs/tasks/run/multikueue/external-frameworks.md)


## 提交作业
在[配置好的 MultiKueue 环境](/zh-CN/docs/tasks/manage/setup_multikueue)中，
你可以向管理集群提交任何 MultiKueue 支持的作业，目标是为 Multikueue 配置的 ClusterQueue。
Kueue 将作业委派给配置的工作集群，无需任何额外的配置更改。

## 接下来是什么？
- 学习如何[设置 MultiKueue 环境](/zh-CN/docs/tasks/manage/setup_multikueue/)
- 学习如何在 MultiKueue 环境中[运行作业](/zh-CN/docs/tasks/run/multikueue)。

## 限制

- 我们当前不支持将管理集群作为其自身的工作者之一运行。
- 对于没有 `managedBy` 支持的作业类型（StatefulSet，LeaderWorkerSet），
  管理集群上的 Job 状态可能无法反映来自工作集群的实际状态。
  这是因为本地 Job 控制器基于本地（特性门控）Pod 持续更新状态。
  工作集群上的 Job 执行不受影响 - 仅管理集群上的状态可见性受限。
  检查工作负载状态以获取准确的准入状态。
