---
title: "核心概念"
linkTitle: "核心概念"
weight: 4
description: >
  Kueue 核心概念
no_list: true
---

本节文档将帮助您了解 Kueue 用于表示集群和工作负载的组件、API 及抽象的相关核心概念。

## APIs {#apis}

### [资源规格](/docs/concepts/resource_flavor) {#resource-flavor}

您可以定义该对象来描述集群中可用的资源。通常，`ResourceFlavor` 与一组节点的特性相关联。它可以区分资源的不同特性，如可用性、价格、架构、型号等。

### [集群队列](/docs/concepts/cluster_queue) {#cluster-queue}

一个集群范围的资源，用于管理一组资源池，定义使用限制和公平共享规则。

### [本地队列](/docs/concepts/local_queue) {#local-queue}

一个命名空间级别的资源，用于将属于同一租户的相关工作负载分组。

### [工作负载](/docs/concepts/workload) {#workload}

将要运行至完成的应用程序。它是 Kueue 中"准入"的单位，有时也称为"作业（job）"。

### [工作负载优先级类](/docs/concepts/workload_priority_class) {#workload-priority-class}

`WorkloadPriorityClass` 定义了工作负载的优先级类，与 [Pod 优先级](https://kubernetes.io/zh-cn/docs/concepts/scheduling-eviction/pod-priority-preemption/) 无关。该优先级值仅用于管理 [工作负载](#workload) 的排队和抢占。

### [准入检查](/docs/concepts/admission_check) {#admission-check}

一种允许内部或外部组件影响工作负载进入时间的机制。

![组件](/images/queueing-components.svg)

### [拓扑感知调度](/docs/concepts/topology_aware_scheduling) {#topology-aware-scheduling}

一种机制，允许调度工作负载时优化 Pod 之间的网络吞吐量。

## 术语表 {#glossary}

### 配额预留 {#quota-reservation}

**配额预留**是 kueue 调度器在目标 [集群队列资源组](/docs/concepts/cluster_queue/#resource-groups) 内锁定工作负载所需资源的过程。

配额预留有时也称为**工作负载调度**或**作业调度**，但不应与 [Pod 调度](https://kubernetes.io/zh-cn/docs/concepts/scheduling-eviction/assign-pod-node/) 混淆。

### 准入 {#admission}

**准入**是允许工作负载启动（Pod 被创建）的过程。当工作负载拥有配额预留且所有[准入检查状态](/docs/concepts/admission_check) 都为 `Ready` 时，该工作负载即被准入。

### [队列组](/docs/concepts/cluster_queue#cohort)

**队列组**是一组可以相互借用未使用配额的集群队列。

### 排队 {#queueing}

**排队**是指工作负载自创建起到被 Kueue 在集群队列中准入前的状态。
通常，工作负载会根据集群队列的公平共享规则，与其他工作负载竞争可用配额。

### [抢占](/docs/concepts/preemption) {#preemption}

**抢占**是为了容纳另一个工作负载而驱逐一个或多个已准入工作负载的过程。被驱逐的工作负载可能优先级较低，或正在借用现在被所属集群队列需要的资源。

### [公平共享](/docs/concepts/fair_sharing) {#fair-sharing}

Kueue 中用于在租户之间公平分配配额的机制。
