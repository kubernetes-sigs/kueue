---
title: "在多集群中运行 LeaderWorkerSet"
linkTitle: "LeaderWorkerSet"
weight: 10
date: 2026-01-16
description: >
  运行 MultiKueue 调度的 LeaderWorkerSet。
---

## 在你开始之前

查阅 [MultiKueue 安装指南](/docs/tasks/manage/setup_multikueue)以了解如何正确设置 MultiKueue 集群。

## 工作原理

LeaderWorkerSet 每个副本创建一个 Workload，这在 MultiKueue 中需要特殊处理。

来自 LeaderWorkerSet 的所有 Workload 会被原子性地分配到同一个工作集群：

1. 每个工作负载等待所有 Workload 组成员存在于管理集群上。
2. 主 Workload（索引 0）被提名并分配到目标工作集群。
3. 一旦远程 Workload 被工作集群接受，主要的 `ClusterName` 就会被设置。
4. 所有其他 Workload 组成员根据 `ClusterName` 调度到相同的工作集群。
5. 在所有 Workload 同步完成后，LeaderWorkerSet 会被在工作集群上创建出来。

这确保了 LeaderWorkerSet 的所有副本都会在同一工作集群上一起运行。

## 局限性

LeaderWorkerSet 当前不支持 `managedBy` 字段。

由于这一限制，要获取 LeaderWorkerSet 的实际状态，你需要访问工作集群。

## 示例

这是一个示例 LeaderWorkerSet：

{{< include "examples/serving-workloads/sample-leaderworkerset.yaml" "yaml" >}}

你可以使用以下命令创建 LeaderWorkerSet：

```sh
kubectl create -f sample-leaderworkerset.yaml
```
