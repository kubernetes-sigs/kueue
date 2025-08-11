---
title: "运行 AppWrapper"
linkTitle: "AppWrapper"
date: 2025-01-08
weight: 6
description: 在 Kueue 上运行 AppWrapper。
---

本页面展示了在运行 [AppWrapper](https://project-codeflare.github.io/appwrapper/)
时，如何利用 Kueue 的调度和资源管理能力。

AppWrapper 提供了一种灵活且与工作负载无关的机制，使 Kueue 能够将一组 Kubernetes 资源作为一个单一的逻辑单元进行管理，
而无需这些资源的控制器具备任何 Kueue 特定的支持。

AppWrapper 旨在通过提供额外的自动故障检测和恢复机制来增强工作负载的健壮性。
AppWrapper 控制器会监控工作负载的健康状况，
如果主要资源控制器在指定的截止时间内未采取纠正措施，AppWrapper 控制器将协调工作负载级别的重试和资源删除，
以确保工作负载要么恢复到健康状态，要么被干净地从集群中移除，并释放其配额供其他工作负载使用。

本指南适用于对 Kueue 有基本了解的[批用户](/zh-CN/docs/tasks#batch-user)。更多信息请参见 [Kueue 概述](/zh-CN/docs/overview)。

## 开始之前 {#before-you-begin}

请查阅[管理集群配额](/zh-CN/docs/tasks/manage/administer_cluster_quotas)以了解 Kueue 的初始设置详情。

为简化设置，请确保你使用的是 Kueue v0.11.0 和 AppWrapper v1.1.1 及以上版本。

有关 AppWrapper Operator 的安装和配置详情，请参见 [AppWrapper 快速入门指南](https://project-codeflare.github.io/appwrapper/quick-start/)。

## AppWrapper 定义 {#appwrapper-definition}

在 Kueue 上运行 AppWrapper 时，请注意以下方面：

### a. 队列选择 {#a-queue-selection}

目标[本地队列](/zh-CN/docs/concepts/local_queue)应在 AppWrapper 的 `metadata.labels` 部分指定。

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. 配置资源需求 {#b-configure-the-resource-needs}

工作负载的资源需求通过组合每个 Wrapper 组件的资源需求来计算。

## 包含 PyTorchJob 的 AppWrapper 示例 {#example-appwrapper-containing-a-pytorchjob}

AppWrapper 如下所示：

{{< include "examples/appwrapper/pytorch-sample.yaml" "yaml" >}}

{{% alert title="注意" color="primary" %}}
上述示例来自[这里](https://raw.githubusercontent.com/project-codeflare/appwrapper/refs/heads/main/samples/wrapped-pytorch-job.yaml)，
仅更改了 `queue-name` 标签。
{{% /alert %}}

## 包含 Deployment 的 AppWrapper 示例 {#example-appwrapper-containing-a-deployment}

AppWrapper 如下所示：

{{< include "examples/appwrapper/deployment-sample.yaml" "yaml" >}}
