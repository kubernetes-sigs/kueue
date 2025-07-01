---
title: "核心概念"
linkTitle: "核心概念"
weight: 4
description: >
  Kueue 核心概念
no_list: true
---

此部分文档帮助你了解 Kueue 用来表示集群和工作负载的组件、API 和抽象。

## APIs

### [Resource Flavor](/zh-cn/docs/concepts/resource_flavor)

你可以定义的一个对象，用于描述集群中可用的资源。通常，
`ResourceFlavor` 与一组节点的特征相关联。它可以区分资源的不同特性，如可用性、定价、架构、模型等。

### [Cluster Queue](/docs/concepts/cluster_queue)

一个集群范围的资源，管理资源池，定义使用限制和公平共享规则。

### [Local Queue](/docs/concepts/local_queue)

一个命名空间范围的资源，用于组织属于单一租户内紧密相关的工作负载。

### [Workload](/docs/concepts/workload)

将要运行至完成的应用程序。它是 Kueue 中的**准入**单元，有时被称为**作业**。

### [Workload Priority Class](/docs/concepts/workload_priority_class)

`WorkloadPriorityClass` 为工作负载定义了一个优先级类别，这独立于
[Pod 优先级](https://kubernetes.io/zh-cn/docs/concepts/scheduling-eviction/pod-priority-preemption/)。
`WorkloadPriorityClass` 中的优先级值仅用于管理 [Workloads](#workload) 的排队和抢占。

### [Admission Check](/docs/concepts/admission_check)

一种允许内部或外部组件影响工作负载准入时机的机制。

![Components](/images/queueing-components.svg)

### [Topology Aware Scheduling](/docs/concepts/topology_aware_scheduling)

一种允许调度工作负载以优化 Pod 间网络吞吐量的机制。

## 词汇表

### 配额预留（Quota Reservation）

**配额预留**是 Kueue 调度器在目标
[ClusterQueues ResourceGroups](/zh-cn/docs/concepts/cluster_queue/#resource-groups) 中锁定工作负载所需资源的过程。

配额预留有时被称为**工作负载调度**或**作业调度**，
但不应与 [Pod 调度](https://kubernetes.io/zh-cn/docs/concepts/scheduling-eviction/assign-pod-node/)混淆。

### 准入（Admission）

**准入** 是允许工作负载开始（Pod 被创建）的过程。
当工作负载拥有配额预留并且其所有[准入检查状态](/zh-cn/docs/concepts/admission_check)均为
`Ready` 时，该工作负载即被准入。

### [队列组](/docs/concepts/cluster_queue#cohort)（Cohort）

**队列组**是一组可以互相借用未使用配额的集群队列。

### 排队（Queueing）

**排队**是指工作负载从创建之时起直到被 Kueue 在某个集群队列上准入之前所处的状态。
通常，根据集群队列的公平共享规则，该工作负载将与其他工作负载竞争可用配额。

### [抢占](/zh-cn/docs/concepts/preemption)（Preemption）

**抢占**是驱逐一个或多个已准入工作负载以容纳另一个工作负载的过程。
被驱逐的工作负载可能优先级较低，或者可能是借用了现在由所属集群队列所需的资源。

### [公平共享](/zh-cn/docs/concepts/fair_sharing)（Fair Sharing）

Kueue 中在租户之间公平共享配额的机制。
