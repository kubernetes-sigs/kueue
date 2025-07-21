---
title: "在多集群环境中运行 Job"
linkTitle: "Kubernetes Job"
weight: 2
date: 2024-11-05
description: 在多集群环境中运行 Job
---

## 在开始之前

请查阅 [MultiKueue 安装指南](/docs/tasks/manage/setup_multikueue) 了解如何正确设置 MultiKueue 集群。

为方便安装和使用，建议至少使用 Kueue v0.8.1。

运行 MultiKueue 的推荐方式取决于集群中 `JobManagedBy` 特性门控的配置。

{{% alert title="Note" color="primary" %}}
`JobManagedBy` 特性门控在 1.30 和 1.31 版本中默认禁用，在 1.32 版本中默认启用。
{{% /alert %}}

### 启用 JobManagedBy 的集群

当集群启用 `JobManagedBy` 时，建议配置 Kueue 启用 `MultiKueueBatchJobWithManagedBy` 特性门控。

启用后，MultiKueue 在工作集群上执行的 Job 的当前状态会实时同步到管理集群。

这样，用户和自动化工具可以在不访问工作集群的情况下，直接跟踪 Job 的状态（.status），从而实现 MultiKueue 的透明化。

### 未启用 JobManagedBy 的集群

当集群未启用 `JobManagedBy` 时，应确保 Kueue 也未启用 `MultiKueueBatchJobWithManagedBy`。

这样可以避免 MultiKueue 与管理集群上的内置 Job 控制器发生冲突。

在此部署模式下的一个限制是，需访问工作集群才能获取作业的实际状态。

你可以通过检查管理集群中 workload 对象的 AC 状态消息，确定运行作业的工作集群。

此外，从管理集群的角度看，作业会一直处于挂起状态，直到其 `Finished`。

## 示例

完成设置后，你可以通过运行以下示例进行测试：

{{< include "examples/jobs/sample-job.yaml" "yaml" >}}
