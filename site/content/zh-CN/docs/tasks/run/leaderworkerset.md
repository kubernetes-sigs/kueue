---
title: "运行 LeaderWorkerSet"
linkTitle: "LeaderWorkerSet"
date: 2025-02-17
weight: 6
description: 在启用了kueue的环境里运行 LeaderWorkerSet
---

本页面展示了如何利用 Kueue 的调度和服务资源管理能力来运行 [LeaderWorkerSet](https://github.com/kubernetes-sigs/lws)。

我们演示了如何支持调度 LeaderWorkerSets，其中一组 Pods 构成一个单位，由 Workload 表示。这允许按组缩放和缩减 LeaderWorkerSets。

此集成基于 [Plain Pod Group](https://kueue.sigs.k8s.io/docs/tasks/run/plain_pods/) 集成。

本指南适用于 [服务用户](/docs/tasks#serving-user) ，他们具有 Kueue 的基本理解。有关更多信息，请参见 [Kueue 概述](/docs/overview)。

## 开始之前

1. 学习如何 [安装 Kueue 并使用自定义管理器配置](/docs/installation/#install-a-custom-configured-released-version)。

2. 确保您已启用 `leaderworkerset.x-k8s.io/leaderworkerset` 集成，例如：
   ```yaml
   apiVersion: config.kueue.x-k8s.io/v1beta1
   kind: Configuration
   integrations:
     frameworks:
      - "pod"
      - "leaderworkerset.x-k8s.io/leaderworkerset"
   ```
   同时，请参见 [Run Plain Pods](/docs/tasks/run/plain_pods/#before-you-begin)
   了解如何启用和配置 `pod` 集成。

3. 检查 [Administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas) 了解初始 Kueue 设置的详细信息。

## 运行一个由 Kueue 承认的 LeaderWorkerSet

运行 LeaderWorkerSet 时，请考虑以下方面：

### a. 队列选择

LeaderWorkerSet 配置的 `metadata.labels` 部分应指定目标 [local queue](/docs/concepts/local_queue)。

```yaml
metadata:
   labels:
      kueue.x-k8s.io/queue-name: user-queue
```

### b. 配置资源需求
Workload 的资源需求可以在 `spec.template.spec.containers` 中配置。

```yaml
spec:
   leaderWorkerTemplate:
      leaderTemplate:
         spec:
            containers:
               - resources:
                    requests:
                       cpu: "100m"
      workerTemplate:
         spec:
            containers:
               - resources:
                    requests:
                       cpu: "100m"
```

### c. 缩放

您可以对 LeaderWorkerSet `.spec.replicas` 执行缩放操作。

缩放的单位是 LWS 组。通过更改 LWS 中的 `replicas` 数量，您可以创建或删除整个 Pod 组。缩放后，新创建的 Pod 组会通过调度门被挂起，直到相应的 Workload 被承认。

## 示例
以下是一个 LeaderWorkerSet 的示例：

{{< include "examples/serving-workloads/sample-leaderworkerset.yaml" "yaml" >}}

您可以使用以下命令创建 LeaderWorkerSet：

```sh
kubectl create -f sample-leaderworkerset.yaml
```
