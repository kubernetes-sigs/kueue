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

## 支持的作业

MultiKueue 支持多种工作负载，你可以学习如何：
- [调度 Kueue 管理的 Deployment](docs/tasks/run/multikueue/deployment)。
- [调度 Kueue 管理的 batch/Job](docs/tasks/run/multikueue/job)。
- [调度 Kueue 管理的 JobSet](docs/tasks/run/multikueue/jobset)。
- [调度 Kueue 管理的 Kubeflow Job](docs/tasks/run/multikueue/kubeflow)。
- [调度 Kueue 管理的 KubeRay 工作负载](docs/tasks/run/multikueue/kuberay)。
- [调度 Kueue 管理的 MPIJob](docs/tasks/run/multikueue/mpijob)。
- [调度 Kueue 管理的 AppWrapper](docs/tasks/run/multikueue/appwrapper)。
- [调度 Kueue 管理的普通 Pod](docs/tasks/run/multikueue/plain_pods)。

## 提交作业
在[配置好的 MultiKueue 环境](/zh-CN/docs/tasks/manage/setup_multikueue)中，
你可以向管理集群提交任何 MultiKueue 支持的作业，目标是为 Multikueue 配置的 ClusterQueue。
Kueue 将作业委派给配置的工作集群，无需任何额外的配置更改。

## 接下来是什么？
- 学习如何[设置 MultiKueue 环境](/zh-CN/docs/tasks/manage/setup_multikueue/)
- 学习如何在 MultiKueue 环境中[运行作业](/zh-CN/docs/tasks/run/multikueue)。
