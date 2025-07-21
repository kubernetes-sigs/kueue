---
title: "Run Deployment"
linkTitle: "Deployment"
date: 2024-07-25
weight: 6
description: 在启用了kueue的环境里运行 Deployment
---

本页面展示了如何利用 Kueue 的调度和服务资源管理能力来运行部署。
虽然 Kueue 目前不支持将部署作为单一工作负载进行管理，
但仍然可以利用 Kueue 的调度和服务资源管理能力来管理部署的各个 Pod。

我们演示了如何基于 Plain Pod 集成支持在 Kueue 中调度部署，
其中部署中的每个 Pod 都表示为一个独立的 Plain Pod。
这种方法允许对 Pod 进行独立资源管理，从而实现部署的扩缩容。

本指南适用于 [serving users](/docs/tasks#serving-user) 用户，他们具有基本的 Kueue 理解。
有关更多信息，请参阅 [Kueue 概览](/docs/overview)。

## 开始之前

1. 学习如何 [安装 Kueue 并使用自定义管理器配置](/docs/installation/#install-a-custom-configured-released-version)。

2. 按照 [Run Plain Pods](/docs/tasks/run/plain_pods/#before-you-begin)
步骤学习如何启用和配置 `pod` 集成。

3. 检查 [Administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas) 了解初始 Kueue 设置的详细信息。

## 运行 Kueue 管理的部署

在 Kueue 中运行部署时，请考虑以下方面：

### a. 队列选择

部署配置的 `metadata.labels` 部分应指定目标 [local queue](/docs/concepts/local_queue)。

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

您可以对部署进行扩缩容操作。在缩容时，多余的 Pod 会被删除，配额会被释放。在扩容时，会创建新的 Pod，并保持暂停状态，直到其对应的工作负载被准入。如果集群中没有足够的配额，部署可能只会运行部分 Pod。
所以，如果您的负载是业务关键的，
您可以考虑仅为集群队列 `lendingLimit` 中的关键服务负载保留配额。
`lendingLimit` 允许您快速扩容关键服务负载。有关更多 `lendingLimit` 详情，请参阅 [ClusterQueue 页面](docs/concepts/cluster_queue#lendinglimit)。

### d. 限制

- 部署的范围由 pod 集成的 namespace 选择器隐含。没有独立的部署控制。

## 示例

以下是一个部署示例：

{{< include "examples/serving-workloads/sample-deployment.yaml" "yaml" >}}

您可以使用以下命令创建部署：
```sh
kubectl create -f sample-deployment.yaml
```
