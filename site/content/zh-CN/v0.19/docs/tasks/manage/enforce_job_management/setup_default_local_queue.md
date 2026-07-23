---
title: "设置默认的 LocalQueue"
date: 2024-12-12
weight: 10
description: >
  配置默认的 LocalQueue，以满足未指定队列标签的作业的队列标签需求。
---

{{< feature-state state="stable" for_version="v0.17" >}}

此页面描述了如何设置默认 LocalQueue，以确保所有提交到特定命名空间的工作负载由 Kueue 管理，
即使未明确指定 `kueue.x-k8s.io/queue-name` 标签。

## 设置默认 LocalQueue

LocalQueueDefaulting 是一个特性，允许使用一个名为 `default` 的 LocalQueue
作为同命名空间下没有 `kueue.x-k8s.io/queue-name` 标签的工作负载的默认 LocalQueue。

要使用此特性：

- 在命名空间中创建一个名称为 `default` 的 LocalQueue。

就是这样！现在，为了测试此特性，在同一命名空间中创建一个 Job。观察到 Job
被更新为带有 `kueue.x-k8s.io/queue-name: default` 标签。

请注意，在不同命名空间中创建的工作负载或已经具有 `kueue.x-k8s.io/queue-name`
标签的工作负载不会被修改。
