---
title: "运行 StatefulSet"
linkTitle: "StatefulSet"
date: 2024-07-25
weight: 6
description: 将 StatefulSet 作为 Kueue 管理的工作负载运行。
---

本页面展示了如何利用 Kueue 的调度和服务资源管理能力来运行 StatefulSet。

我们演示了如何支持基于 [Plain Pod Group](https://kueue.sigs.k8s.io/docs/tasks/run/plain_pods/)
集成来调度 StatefulSet，其中 StatefulSet 被表示为一个独立的负载。

本指南适用于[服务用户](/zh-CN/docs/tasks#serving-user)他们具有基本的 Kueue 理解。
更多信息，请参见 [Kueue 概览](/zh-CN/docs/overview)。

## 开始之前 {#before-you-begin}

1. 学习如何[安装 Kueue 并配置自定义管理器版本](/zh-CN/docs/installation/#install-a-custom-configured-released-version)。

2. 确保你启用了 v1/statefulset 集成，例如：
   ```yaml
   apiVersion: config.kueue.x-k8s.io/v1beta1
   kind: Configuration
   integrations:
     frameworks:
      - "pod" # required by statefulset
      - "statefulset"
   ```
   同时，请参见[运行 Plain Pod](/zh-CN/docs/tasks/run/plain_pods/#before-you-begin)
   了解如何启用和配置 `pod` 集成。

3. 检查[管理集群配额](/zh-CN/docs/tasks/manage/administer_cluster_quotas)了解初始 Kueue 设置的详细信息。

## 运行被 Kueue 调度的 StatefulSet {#running-a-statefulset-admitted-by-kueue}

在运行 StatefulSet 时，请考虑以下方面：

### a. 队列选择 {#a-queue-selection}

目标[本地队列](/zh-CN/docs/concepts/local_queue)应在 StatefulSet 配置的 `metadata.labels` 部分指定。

```yaml
metadata:
   labels:
      kueue.x-k8s.io/queue-name: user-queue
```

### b. 配置资源需求 {#b-configure-the-resource-needs}
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

### c. 扩缩 {#c-scaling}

目前，StatefulSet 的扩缩操作不受支持。
这意味着你不能直接通过 Kueue 执行扩缩容操作。

## 示例 {#examples}
以下是一个 StatefulSet 的示例：

{{< include "examples/serving-workloads/sample-statefulset.yaml" "yaml" >}}

你可以使用以下命令创建 StatefulSet：

```sh
kubectl create -f sample-statefulset.yaml
```
