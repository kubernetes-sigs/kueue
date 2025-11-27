---
title: "运行 Deployment"
linkTitle: "Deployment"
date: 2024-07-25
weight: 6
description: >
  将 Deployment 作为 Kueue 管理的工作负载运行。
---

本页面展示了在运行 Deployment 时如何利用 Kueue 的调度和资源管理能力。
虽然 Kueue 尚不支持将 Deployment 作为单个工作负载进行管理，
但仍然可以利用 Kueue 的调度和资源管理能力来管理 Deployment 中的各个 Pod。

我们演示了如何基于 Plain Pod 集成在 Kueue 中支持调度 Deployment，
其中 Deployment 中的每个 Pod 都被表示为一个独立的 Plain Pod。
这种方法允许对 Pod 进行独立的资源管理，从而实现 Deployment 的扩容和缩容。

本指南适用于对 Kueue 有基本了解的[服务用户](/zh-CN/docs/tasks#serving-user)。
更多信息，请参见 [Kueue 概述](/zh-CN/docs/overview)。

## 开始之前

1. 学习如何[使用自定义管理器配置来安装 Kueue](/docs/installation/#install-a-custom-configured-released-version)。

2. 按照[运行 Plain Pod](/zh-CN/docs/tasks/run/plain_pods/#before-you-begin)中的步骤学习如何启用和配置 `pod` 集成。

3. 查看[管理集群配额](/zh-CN/docs/tasks/manage/administer_cluster_quotas)了解初始 Kueue 设置的详细信息。

## 运行由 Kueue 准入的 Deployment

在 Kueue 上运行 Deployment 时，需要考虑以下方面：

### a. 队列选择

目标[本地队列](/zh-CN/docs/concepts/local_queue)应在 Deployment 配置的 `metadata.labels` 部分中指定。

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. 配置资源需求

工作负载的资源需求可以在 `spec.template.spec.containers` 中配置。

```yaml
    - resources:
        requests:
          cpu: 3
```

### c. 扩缩容

您可以对 Deployment 执行扩容或缩容操作。
在缩容时，多余的 Pod 会被删除，配额会被释放。
在扩容时，会创建新的 Pod，这些 Pod 会保持挂起状态，直到相应的工作负载被准入。
如果集群中没有足够的配额，Deployment 可能只运行部分 Pod。
因此，如果您的工作负载对业务至关重要，
您可以考虑通过 ClusterQueue 的 `lendingLimit` 仅为服务工作负载保留配额。
`lendingLimit` 允许您快速扩容关键的服务工作负载。
有关 `lendingLimit` 的更多详细信息，请参见 [ClusterQueue 页面](/zh-CN/docs/concepts/cluster_queue#lendinglimit)。

### d. 限制

- Deployment 的范围由 `pod` 集成的命名空间选择器所默示。不能独立控制 Deployment。

## 示例

以下是一个示例 Deployment：

{{< include "examples/serving-workloads/sample-deployment.yaml" "yaml" >}}

您可以使用以下命令创建 Deployment：
```sh
kubectl create -f sample-deployment.yaml
```
