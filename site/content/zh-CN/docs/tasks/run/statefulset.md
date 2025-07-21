---
title: "运行 StatefulSet"
linkTitle: "StatefulSet"
date: 2024-07-25
weight: 6
description: 在启用了kueue的环境里运行 StatefulSet
---

本页面展示了如何利用 Kueue 的调度和服务资源管理能力来运行 StatefulSets。

我们演示了如何支持基于 [Plain Pod Group](https://kueue.sigs.k8s.io/docs/tasks/run/plain_pods/) 集成来调度 StatefulSets，其中 StatefulSet 被表示为一个独立的负载。

本指南适用于 [服务用户](/docs/tasks#serving-user) ，他们具有基本的 Kueue 理解。更多信息，请参见 [Kueue 概览](/docs/overview)。

## 在开始之前

1. 学习如何 [安装 Kueue 并配置自定义管理器版本](/docs/installation/#install-a-custom-configured-released-version)。

2. 确保您启用了 v1/statefulset 集成，例如：
   ```yaml
   apiVersion: config.kueue.x-k8s.io/v1beta1
   kind: Configuration
   integrations:
     frameworks:
      - "pod" # required by statefulset
      - "statefulset"
   ```
   同时，请参见 [运行 Plain Pods](/docs/tasks/run/plain_pods/#before-you-begin)
   了解如何启用和配置 `pod` 集成。

3. 检查 [管理集群配额](/docs/tasks/manage/administer_cluster_quotas) 了解初始 Kueue 设置的详细信息。

## 运行被 Kueue 调度的 StatefulSet

在运行 StatefulSet 时，请考虑以下方面：

### a. 队列选择

目标 [本地队列](/docs/concepts/local_queue) 应在 StatefulSet 配置的 `metadata.labels` 部分指定。

```yaml
metadata:
   labels:
      kueue.x-k8s.io/queue-name: user-queue
```

### b. 配置资源需求
工作负载的资源需求可以在 `spec.template.spec.containers` 中配置。

```yaml
spec:
  template:
     spec:
      containers:
       - resources:
           requests:
             cpu: 3
```

### c. 扩展

目前，StatefulSets 的扩展操作不受支持。
这意味着您不能直接通过 Kueue 执行扩缩容操作。

## 示例
以下是一个 StatefulSet 的示例：

{{< include "examples/serving-workloads/sample-statefulset.yaml" "yaml" >}}

您可以使用以下命令创建 StatefulSet：

```sh
kubectl create -f sample-statefulset.yaml
```
